package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"gorm.io/gorm"
)

var maintenanceSourceKinds = []ontology.SourceKind{ontology.EntitySource, ontology.PredicateSource, ontology.EvidenceSource, ontology.RelationshipSource}

func (s *Store) DiscoverMaintenance(ctx context.Context, turn ontology.MaintenanceTurn, limit int) error {
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
		return expandMaintenanceMarkers(tx, fence, limit)
	})
}

func maintenanceSourcePage(tx *gorm.DB, fence scope, kind ontology.SourceKind, cursor string, limit int) ([]ontology.SourceHandle, error) {
	var query string
	switch kind {
	case ontology.EntitySource:
		query = maintenanceEntityPageSQL
	case ontology.EvidenceSource:
		query = `SELECT fragment_id::text AS id,1::bigint FROM evidence_fragments WHERE team_id=?::uuid AND space_id=?::uuid AND space_generation=? AND fragment_id>COALESCE(NULLIF(?,'')::uuid,'00000000-0000-0000-0000-000000000000'::uuid) ORDER BY fragment_id LIMIT ?`
	case ontology.RelationshipSource:
		query = `SELECT relationship_id::text AS id,version FROM relationship_records WHERE team_id=?::uuid AND space_id=?::uuid AND space_generation=? AND relationship_id>COALESCE(NULLIF(?,'')::uuid,'00000000-0000-0000-0000-000000000000'::uuid) ORDER BY relationship_id LIMIT ?`
	case ontology.PredicateSource:
		query = eligiblePredicateSelect + ` AND definition.predicate_key>? ORDER BY definition.predicate_key,definition.version DESC LIMIT ?`
	default:
		return nil, ontology.ErrInvalid
	}
	rows, err := tx.Raw(query, fence.TeamID, fence.SpaceID, fence.Generation, cursor, limit).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []ontology.SourceHandle{}
	for rows.Next() {
		handle := ontology.SourceHandle{Kind: kind}
		if err := rows.Scan(&handle.ID, &handle.Version); err != nil {
			return nil, err
		}
		result = append(result, handle)
	}
	return result, rows.Err()
}

func refreshMaintenanceSource(tx *gorm.DB, fence scope, handle ontology.SourceHandle, force bool) error {
	if force {
		var body []byte
		err := tx.Raw(`SELECT assessment.body FROM ontology_maintenance_sources AS source
		 JOIN ontology_assessments AS assessment ON assessment.team_id=source.team_id AND assessment.shared_space_id=source.shared_space_id AND assessment.space_generation=source.space_generation AND assessment.assessment_id=source.assessment_id
		 WHERE source.team_id=?::uuid AND source.shared_space_id=?::uuid AND source.space_generation=? AND source.source_kind=? AND source.source_id=? AND source.status IN ('organized','failed','ambiguous','budget_deferred') LIMIT 1`, fence.TeamID, fence.SpaceID, fence.Generation, handle.Kind, handle.ID).Row().Scan(&body)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil {
			receipt, err := decodeOrganization(body)
			if err != nil {
				return err
			}
			current, err := (&Store{}).organizationInputsCurrent(tx, fence, receipt)
			if err != nil {
				return err
			}
			force = !current
		}
	}
	snapshot, err := readSource(tx, fence, handle)
	if err != nil && !errors.Is(err, ontology.ErrSourceStale) {
		return err
	}
	if errors.Is(err, ontology.ErrSourceStale) {
		latest, latestErr := latestMaintenanceHandle(tx, fence, handle)
		if latestErr != nil {
			return latestErr
		}
		if latest != handle {
			handle = latest
			snapshot, err = readSource(tx, fence, handle)
			if err != nil && !errors.Is(err, ontology.ErrSourceStale) {
				return err
			}
		}
	}
	fingerprint, err := ontology.SourceFingerprint(snapshot)
	if err != nil {
		return err
	}
	status, reason := "pending", ""
	if !snapshot.Eligible {
		status, reason = "unavailable", "source_unavailable"
	}
	text := []rune(ontology.SourceDisplayText(snapshot))
	if len(text) > 8192 {
		text = text[:8192]
	}
	return tx.Exec(`INSERT INTO ontology_maintenance_sources(team_id,shared_space_id,space_generation,source_kind,source_id,source_version,fingerprint,meaning_key,search_tsv,eligible,status,reason)
	 VALUES(?::uuid,?::uuid,?,?,?,?,?,?,to_tsvector('simple',?),?,?,?)
	 ON CONFLICT(team_id,shared_space_id,space_generation,source_kind,source_id) DO UPDATE SET
	 source_version=EXCLUDED.source_version,fingerprint=EXCLUDED.fingerprint,meaning_key=EXCLUDED.meaning_key,search_tsv=EXCLUDED.search_tsv,eligible=EXCLUDED.eligible,
	 revision=ontology_maintenance_sources.revision+1,status=EXCLUDED.status,reason=EXCLUDED.reason,
	 pending_at=CASE WHEN ontology_maintenance_sources.status IN ('pending','budget_deferred') THEN ontology_maintenance_sources.pending_at ELSE clock_timestamp() END,
	 updated_at=clock_timestamp() WHERE ontology_maintenance_sources.fingerprint<>EXCLUDED.fingerprint OR ?`, fence.TeamID, fence.SpaceID, fence.Generation, handle.Kind, handle.ID, handle.Version, fingerprint, snapshot.MeaningKey, string(text), snapshot.Eligible, status, reason, force).Error
}

func latestMaintenanceHandle(tx *gorm.DB, fence scope, handle ontology.SourceHandle) (ontology.SourceHandle, error) {
	var query string
	switch handle.Kind {
	case ontology.EntitySource:
		query = `SELECT version FROM entity_records WHERE team_id=?::uuid AND space_id=?::uuid AND space_generation=? AND entity_id=?::uuid`
	case ontology.RelationshipSource:
		query = `SELECT version FROM relationship_records WHERE team_id=?::uuid AND space_id=?::uuid AND space_generation=? AND relationship_id=?::uuid`
	case ontology.PredicateSource:
		query = `SELECT version FROM (` + eligiblePredicateSelect + ` AND definition.predicate_key=? ORDER BY definition.predicate_key,definition.version DESC) AS live`
	case ontology.EvidenceSource:
		return handle, nil
	default:
		return handle, ontology.ErrInvalid
	}
	var version int64
	err := tx.Raw(query, fence.TeamID, fence.SpaceID, fence.Generation, handle.ID).Row().Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		return handle, nil
	}
	if err != nil {
		return handle, err
	}
	handle.Version = version
	return handle, nil
}

type maintenanceMarker struct {
	AnchorKind, AnchorID, TargetKind, TargetID, Cursor string
	Sequence                                           int64
}

func expandMaintenanceMarkers(tx *gorm.DB, fence scope, limit int) error {
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
			if err := refreshMaintenanceInvalidation(tx, fence, handle, marker.TargetKind == "definition"); err != nil {
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

func deleteMaintenanceMarker(tx *gorm.DB, fence scope, marker maintenanceMarker) error {
	return tx.Exec(`DELETE FROM ontology_maintenance_markers WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? AND anchor_kind=? AND anchor_id=? AND marker_sequence=?`, fence.TeamID, fence.SpaceID, fence.Generation, marker.AnchorKind, marker.AnchorID, marker.Sequence).Error
}

func refreshMaintenanceInvalidation(tx *gorm.DB, fence scope, handle ontology.SourceHandle, definition bool) error {
	if err := refreshMaintenanceSource(tx, fence, handle, definition); err != nil {
		return err
	}
	if !definition {
		if err := enqueueMaintenanceDependencies(tx, fence, handle); err != nil {
			return err
		}
		if handle.Kind == ontology.RelationshipSource {
			var predicate ontology.SourceHandle
			predicate.Kind = ontology.PredicateSource
			err := tx.Raw(`SELECT predicate_key,predicate_version FROM relationship_records WHERE team_id=?::uuid AND space_id=?::uuid AND space_generation=? AND relationship_id=?::uuid`, fence.TeamID, fence.SpaceID, fence.Generation, handle.ID).Row().Scan(&predicate.ID, &predicate.Version)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if err == nil {
				return refreshMaintenanceInvalidation(tx, fence, predicate, false)
			}
		}
	}
	return nil
}

func enqueueMaintenanceDependencies(tx *gorm.DB, fence scope, handle ontology.SourceHandle) error {
	return tx.Exec(`INSERT INTO ontology_maintenance_markers(team_id,shared_space_id,space_generation,anchor_kind,anchor_id,target_kind,target_id)
 SELECT ?::uuid,?::uuid,?,'source_dependency',?,'definition',? WHERE EXISTS(
 SELECT 1 FROM ontology_source_dependencies AS dependency JOIN ontology_record_heads AS head
 ON head.team_id=dependency.team_id AND head.shared_space_id=dependency.shared_space_id AND head.space_generation=dependency.space_generation AND head.record_id=dependency.record_id AND head.version=dependency.record_version
 WHERE dependency.team_id=?::uuid AND dependency.shared_space_id=?::uuid AND dependency.space_generation=? AND dependency.source_kind=? AND dependency.source_id=? AND NOT head.retired)
 ON CONFLICT(team_id,shared_space_id,space_generation,anchor_kind,anchor_id) DO UPDATE SET marker_sequence=nextval('ontology_maintenance_marker_seq'),cursor=''`, fence.TeamID, fence.SpaceID, fence.Generation, ontology.SourceKey(handle), handle.ID, fence.TeamID, fence.SpaceID, fence.Generation, handle.Kind, handle.ID).Error
}

func maintenanceMarkerSources(tx *gorm.DB, fence scope, marker maintenanceMarker, limit int) ([]ontology.SourceHandle, error) {
	result := []ontology.SourceHandle{}
	appendRows := func(query string, args ...any) error {
		rows, err := tx.Raw(query, args...).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var handle ontology.SourceHandle
			if err := rows.Scan(&handle.Kind, &handle.ID, &handle.Version); err != nil {
				return err
			}
			result = append(result, handle)
		}
		return rows.Err()
	}
	if marker.TargetKind == "definition" {
		kind, id, _ := strings.Cut(marker.Cursor, ":")
		err := appendRows(maintenanceDefinitionPageSQL, fence.TeamID, fence.SpaceID, fence.Generation, kind, id, limit)
		return result, err
	}
	direct := ontology.SourceHandle{ID: marker.TargetID, Version: 1}
	switch marker.TargetKind {
	case "entity":
		direct.Kind = ontology.EntitySource
	case "predicate":
		direct.Kind = ontology.PredicateSource
	case "relationship":
		direct.Kind = ontology.RelationshipSource
	case "evidence":
		direct.Kind = ontology.EvidenceSource
	case "source", "ingest":
	default:
		return nil, fmt.Errorf("%w: unknown invalidation kind", ontology.ErrInvalid)
	}
	if direct.Kind != "" && ontology.SourceKey(direct) > marker.Cursor {
		latest, err := latestMaintenanceHandle(tx, fence, direct)
		if err != nil {
			return nil, err
		}
		result = append(result, latest)
	}
	if len(result) == limit || marker.TargetKind == "entity" || marker.TargetKind == "relationship" {
		return result, nil
	}
	cursorKind, cursorID, _ := strings.Cut(marker.Cursor, ":")
	if marker.TargetKind == "source" || marker.TargetKind == "ingest" {
		if cursorKind <= "evidence" {
			after := "00000000-0000-0000-0000-000000000000"
			if cursorKind == "evidence" {
				after = cursorID
			}
			column := "source_id"
			if marker.TargetKind == "ingest" {
				column = "ingest_id"
			}
			err := appendRows(`SELECT 'evidence',fragment_id::text AS id,1::bigint FROM evidence_fragments WHERE team_id=?::uuid AND space_id=?::uuid AND space_generation=? AND `+column+`=?::uuid AND fragment_id>?::uuid ORDER BY fragment_id LIMIT ?`, fence.TeamID, fence.SpaceID, fence.Generation, marker.TargetID, after, limit-len(result))
			if err != nil {
				return nil, err
			}
		}
	}
	if len(result) == limit || cursorKind > "relationship" {
		return result, nil
	}
	after := "00000000-0000-0000-0000-000000000000"
	if cursorKind == "relationship" {
		after = cursorID
	}
	filter := `relationship.predicate_key=?`
	if marker.TargetKind != "predicate" {
		column := "fragment_id"
		if marker.TargetKind == "source" {
			column = "source_id"
		}
		if marker.TargetKind == "ingest" {
			column = "ingest_id"
		}
		filter = `EXISTS(SELECT 1 FROM relationship_evidence_supports AS support JOIN evidence_fragments AS fragment ON fragment.team_id=support.team_id AND fragment.fragment_id=support.fragment_id
   WHERE support.team_id=relationship.team_id AND support.space_id=relationship.space_id AND support.space_generation=relationship.space_generation AND support.relationship_id=relationship.relationship_id AND fragment.` + column + `=?::uuid)`
	}
	err := appendRows(`SELECT 'relationship',relationship_id::text AS id,version FROM relationship_records AS relationship WHERE team_id=?::uuid AND space_id=?::uuid AND space_generation=? AND relationship_id>?::uuid AND `+filter+` ORDER BY relationship_id LIMIT ?`, fence.TeamID, fence.SpaceID, fence.Generation, after, marker.TargetID, limit-len(result))
	return result, err
}

func maintenanceLexicalQuery(snapshot ontology.SourceSnapshot) string {
	text := ontology.SourceDisplayText(snapshot)
	words := strings.FieldsFunc(text, func(r rune) bool {
		return !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r > 127)
	})
	if len(words) > 64 {
		words = words[:64]
	}
	return strings.Join(words, " OR ")
}
