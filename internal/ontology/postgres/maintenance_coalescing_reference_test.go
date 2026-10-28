//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"errors"

	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"gorm.io/gorm"
)

// These sequential drivers are frozen from base 09d16398005f0a8ba3ca0fe6c31e08c4341eca9f for isolated paired measurements.
func (s *Store) discoverSequentialMaintenance(ctx context.Context, turn ontology.MaintenanceTurn, limit int) error {
	if limit < 1 || limit > ontology.MaintenancePageSize {
		return ontology.ErrInvalid
	}
	return s.withScope(ctx, turn.TeamID, false, func(tx *gorm.DB, fence scope) error {
		if fence.SpaceID != turn.SpaceID || fence.Generation != turn.Generation {
			return ontology.ErrLeaseLost
		}
		if err := checkMaintenanceTurn(tx, turn); err != nil {
			return err
		}
		var kind int
		var cursor string
		if err := tx.Raw(`SELECT discovery_kind,discovery_cursor FROM ontology_maintenance_teams WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? AND lease_token=?::uuid FOR UPDATE`, fence.TeamID, fence.SpaceID, fence.Generation, turn.LeaseToken).Row().Scan(&kind, &cursor); err != nil {
			return err
		}
		if kind < len(maintenanceSourceKinds) {
			handles, err := maintenanceSourcePage(tx, fence, maintenanceSourceKinds[kind], cursor, limit+1)
			if err != nil {
				return err
			}
			more := len(handles) > limit
			if more {
				handles = handles[:limit]
			}
			for _, handle := range handles {
				if err := refreshMaintenanceSource(tx, fence, handle, false); err != nil {
					return err
				}
				cursor = handle.ID
			}
			if !more {
				kind++
				cursor = ""
			}
			if err := tx.Exec(`UPDATE ontology_maintenance_teams SET discovery_kind=?,discovery_cursor=? WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? AND lease_token=?::uuid`, kind, cursor, fence.TeamID, fence.SpaceID, fence.Generation, turn.LeaseToken).Error; err != nil {
				return err
			}
		}
		return expandSequentialMaintenanceMarkers(tx, fence, limit)
	})
}

func expandSequentialMaintenanceMarkers(tx *gorm.DB, fence scope, limit int) error {
	for remaining := limit; remaining > 0; {
		var marker maintenanceMarker
		err := tx.Raw(`SELECT anchor_kind,anchor_id,target_kind,target_id,marker_sequence,cursor FROM ontology_maintenance_markers WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? ORDER BY marker_sequence LIMIT 1 FOR UPDATE`, fence.TeamID, fence.SpaceID, fence.Generation).Row().Scan(&marker.AnchorKind, &marker.AnchorID, &marker.TargetKind, &marker.TargetID, &marker.Sequence, &marker.Cursor)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
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
		more := len(handles) > remaining
		if more {
			handles = handles[:remaining]
		}
		for _, handle := range handles {
			observeMaintenanceSource(tx.Statement.Context, fence, handle, "source_selected", nil)
			err := refreshMaintenanceInvalidation(tx, fence, handle, marker.TargetKind == "definition")
			observeMaintenanceSource(tx.Statement.Context, fence, handle, "source_finished", err)
			if err != nil {
				return err
			}
			marker.Cursor = ontology.SourceKey(handle)
			remaining--
		}
		if more {
			return tx.Exec(`UPDATE ontology_maintenance_markers SET cursor=? WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? AND anchor_kind=? AND anchor_id=? AND marker_sequence=?`, marker.Cursor, fence.TeamID, fence.SpaceID, fence.Generation, marker.AnchorKind, marker.AnchorID, marker.Sequence).Error
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
