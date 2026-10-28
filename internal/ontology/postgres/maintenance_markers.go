package postgres

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"gorm.io/gorm"
)

type maintenanceMarker struct {
	AnchorKind, AnchorID, TargetKind, TargetID, Cursor string
	Sequence                                           int64
}

func maintenanceMarkerWindow(tx *gorm.DB, fence scope, limit int) ([]maintenanceMarker, error) {
	rows, err := tx.Raw(`SELECT anchor_kind,anchor_id,target_kind,target_id,marker_sequence,cursor FROM ontology_maintenance_markers
	 WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? ORDER BY marker_sequence LIMIT ?`, fence.TeamID, fence.SpaceID, fence.Generation, limit).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var markers []maintenanceMarker
	for rows.Next() {
		var marker maintenanceMarker
		if err := rows.Scan(&marker.AnchorKind, &marker.AnchorID, &marker.TargetKind, &marker.TargetID, &marker.Sequence, &marker.Cursor); err != nil {
			return nil, err
		}
		markers = append(markers, marker)
	}
	return markers, rows.Err()
}

func claimMaintenanceMarker(tx *gorm.DB, fence scope, marker maintenanceMarker) (bool, error) {
	var claimed bool
	err := tx.Raw(`SELECT true FROM ontology_maintenance_markers WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=?
	 AND anchor_kind=? AND anchor_id=? AND marker_sequence=? AND cursor=? FOR UPDATE SKIP LOCKED`, fence.TeamID, fence.SpaceID, fence.Generation,
		marker.AnchorKind, marker.AnchorID, marker.Sequence, marker.Cursor).Row().Scan(&claimed)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return claimed, err
}

func expandMaintenanceMarkers(tx *gorm.DB, fence scope, limit int) error {
	markers, err := maintenanceMarkerWindow(tx, fence, ontology.MaintenancePageSize)
	if err != nil {
		return err
	}
	remaining := limit
	for index, marker := range markers {
		if remaining == 0 {
			break
		}
		claimed, err := claimMaintenanceMarker(tx, fence, marker)
		if err != nil || !claimed {
			return err
		}
		if marker.TargetKind == "definition" {
			group := []maintenanceMarker{marker}
			for _, next := range markers[index+1:] {
				if len(group) == ontology.MaintenanceDefinitionGroupSize || next.TargetKind != "definition" {
					break
				}
				claimed, err := claimMaintenanceMarker(tx, fence, next)
				if err != nil {
					return err
				}
				if !claimed {
					break
				}
				group = append(group, next)
			}
			return expandMaintenanceDefinitionGroup(tx, fence, group, remaining)
		}
		if marker.TargetKind == "team" || marker.TargetKind == "space" {
			if err := tx.Exec(`UPDATE ontology_maintenance_teams SET discovery_kind=0,discovery_cursor='' WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=?`, fence.TeamID, fence.SpaceID, fence.Generation).Error; err != nil {
				return err
			}
			if err := deleteMaintenanceMarker(tx, fence, marker); err != nil {
				return err
			}
			remaining--
			continue
		}
		handles, err := maintenanceMarkerSources(tx, fence, marker, remaining+1)
		if err != nil {
			return err
		}
		recordMaintenanceMatches(tx.Statement.Context, len(handles))
		more := len(handles) > remaining
		if more {
			handles = handles[:remaining]
		}
		cursor := marker.Cursor
		for _, handle := range handles {
			if err := refreshSelectedMaintenanceSource(tx, fence, handle, false); err != nil {
				return err
			}
			cursor = ontology.SourceKey(handle)
			remaining--
		}
		if more {
			return advanceMaintenanceMarker(tx, fence, marker, cursor)
		}
		if err := deleteMaintenanceMarker(tx, fence, marker); err != nil {
			return err
		}
		if len(handles) == 0 {
			remaining--
		}
	}
	return nil
}

func refreshSelectedMaintenanceSource(tx *gorm.DB, fence scope, handle ontology.SourceHandle, definition bool) error {
	observeMaintenanceSource(tx.Statement.Context, fence, handle, "source_selected", nil)
	err := refreshMaintenanceInvalidation(tx, fence, handle, definition)
	observeMaintenanceSource(tx.Statement.Context, fence, handle, "source_finished", err)
	return err
}

func advanceMaintenanceMarker(tx *gorm.DB, fence scope, marker maintenanceMarker, cursor string) error {
	return tx.Exec(`UPDATE ontology_maintenance_markers SET cursor=? WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=?
	 AND anchor_kind=? AND anchor_id=? AND marker_sequence=? AND cursor=?`, cursor, fence.TeamID, fence.SpaceID, fence.Generation,
		marker.AnchorKind, marker.AnchorID, marker.Sequence, marker.Cursor).Error
}

func deleteMaintenanceMarker(tx *gorm.DB, fence scope, marker maintenanceMarker) error {
	deleted := tx.Exec(`DELETE FROM ontology_maintenance_markers WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? AND anchor_kind=? AND anchor_id=? AND marker_sequence=?`, fence.TeamID, fence.SpaceID, fence.Generation, marker.AnchorKind, marker.AnchorID, marker.Sequence)
	if stats := discoveryStats(tx.Statement.Context); stats != nil && deleted.Error == nil {
		stats.counts.CompletedMarkers += int(deleted.RowsAffected)
	}
	return deleted.Error
}

type definitionMarkerInput struct {
	Index      int    `json:"marker_index"`
	RecordID   string `json:"record_id"`
	SourceKind string `json:"source_kind"`
	SourceID   string `json:"source_id"`
	AfterKind  string `json:"after_kind"`
	AfterID    string `json:"after_id"`
}

type definitionMarkerPage struct {
	Sources   []ontology.SourceHandle
	Cursors   []string
	Exhausted []bool
}

func maintenanceDefinitionGroupPage(tx *gorm.DB, fence scope, markers []maintenanceMarker, limit int) (definitionMarkerPage, error) {
	input := make([]definitionMarkerInput, len(markers))
	page := definitionMarkerPage{Cursors: make([]string, len(markers)), Exhausted: make([]bool, len(markers))}
	for i, marker := range markers {
		input[i] = definitionMarkerInput{Index: i, RecordID: marker.TargetID}
		input[i].AfterKind, input[i].AfterID, _ = strings.Cut(marker.Cursor, ":")
		if marker.AnchorKind == "source_dependency" {
			input[i].RecordID, input[i].SourceID = "", marker.TargetID
			input[i].SourceKind, _, _ = strings.Cut(marker.AnchorID, ":")
		}
		page.Cursors[i] = marker.Cursor
	}
	body, err := json.Marshal(input)
	if err != nil {
		return page, err
	}
	rows, err := tx.Raw(maintenanceDefinitionGroupSQL, string(body), fence.TeamID, fence.SpaceID, fence.Generation, limit+1, limit).Rows()
	if err != nil {
		return page, err
	}
	defer rows.Close()
	matches, covered := make([]int, len(markers)), make([]int, len(markers))
	last := ""
	for rows.Next() {
		var index int
		var handle ontology.SourceHandle
		var selected bool
		if err := rows.Scan(&index, &handle.Kind, &handle.ID, &handle.Version, &selected); err != nil {
			return page, err
		}
		matches[index]++
		recordMaintenanceMatches(tx.Statement.Context, 1)
		if selected {
			key := ontology.SourceKey(handle)
			covered[index]++
			page.Cursors[index] = key
			if key != last {
				page.Sources = append(page.Sources, handle)
				last = key
			}
		}
	}
	if err := rows.Err(); err != nil {
		return page, err
	}
	for i := range markers {
		page.Exhausted[i] = matches[i] <= limit && covered[i] == matches[i]
	}
	return page, nil
}

func expandMaintenanceDefinitionGroup(tx *gorm.DB, fence scope, markers []maintenanceMarker, limit int) error {
	page, err := maintenanceDefinitionGroupPage(tx, fence, markers, limit)
	if err != nil {
		return err
	}
	for _, handle := range page.Sources {
		if err := refreshSelectedMaintenanceSource(tx, fence, handle, true); err != nil {
			return err
		}
	}
	for i, marker := range markers {
		if page.Exhausted[i] {
			err = deleteMaintenanceMarker(tx, fence, marker)
		} else if page.Cursors[i] != marker.Cursor {
			err = advanceMaintenanceMarker(tx, fence, marker, page.Cursors[i])
		}
		if err != nil {
			return err
		}
	}
	return nil
}
