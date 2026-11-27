package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lib/pq"
	topic "github.com/markhuangai/dense-mem/internal/topics/contract"
	"gorm.io/gorm"
)

var maintenanceSourceKinds = []topic.SourceKind{topic.EvidenceSource}

func (s *Store) DiscoverMaintenance(ctx context.Context, turn topic.MaintenanceTurn, limit int) (result topic.MaintenanceDiscoveryResult, err error) {
	started := time.Now()
	defer func() { result.Duration = time.Since(started) }()
	if limit < 1 || limit > topic.MaintenancePageSize {
		return result, topic.ErrInvalid
	}
	stats := newMaintenanceDiscoveryStats()
	ctx = context.WithValue(ctx, maintenanceDiscoveryStatsKey{}, stats)
	err = s.withScope(ctx, turn.TeamID, false, func(tx *gorm.DB, fence scope) error {
		if fence.SpaceID != turn.SpaceID || fence.Generation != turn.Generation {
			return topic.ErrLeaseLost
		}
		if err := checkMaintenanceTurn(tx, turn); err != nil {
			return err
		}
		var kind int
		var cursor string
		var due bool
		if err := tx.Raw(`SELECT discovery_kind,discovery_cursor,next_discovery_at<=clock_timestamp() FROM topic_maintenance_teams WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? AND lease_token=?::uuid FOR UPDATE`, fence.TeamID, fence.SpaceID, fence.Generation, turn.LeaseToken).Row().Scan(&kind, &cursor, &due); err != nil {
			return err
		}
		if due && kind >= len(maintenanceSourceKinds) {
			kind, cursor = 0, ""
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
			known, err := knownMaintenanceSources(tx, fence, handles)
			if err != nil {
				return err
			}
			for _, handle := range handles {
				if err := refreshDiscoveredSource(tx, fence, handle, known[handle.ID]); err != nil {
					return err
				}
				stats.counts.InitialSources++
				cursor = handle.ID
			}
			if !more {
				kind++
				cursor = ""
			}
			if err := tx.Exec(`UPDATE topic_maintenance_teams SET discovery_kind=?,discovery_cursor=?,
				next_discovery_at=CASE WHEN ?<1 THEN next_discovery_at ELSE
				clock_timestamp()+COALESCE((SELECT (policy->>'cadence_hours')::int FROM topic_maintenance_windows AS win
				JOIN topic_maintenance_runs AS run ON run.window_id=win.window_id WHERE run.run_id=?::uuid),12)*interval '1 hour' END
				WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? AND lease_token=?::uuid`, kind, cursor, kind, turn.RunID, fence.TeamID, fence.SpaceID, fence.Generation, turn.LeaseToken).Error; err != nil {
				return err
			}
		}
		stats.markerExpansion = true
		return expandMaintenanceMarkers(tx, fence, limit)
	})
	result.Attempted = stats.counts
	if err == nil {
		result.Committed, result.Counts = true, stats.counts
	}
	return result, err
}

func maintenanceSourcePage(tx *gorm.DB, fence scope, kind topic.SourceKind, cursor string, limit int) ([]topic.SourceHandle, error) {
	if kind != topic.EvidenceSource {
		return nil, topic.ErrInvalid
	}
	query := maintenanceEvidencePageSQL
	rows, err := tx.Raw(query, fence.TeamID, fence.SpaceID, fence.Generation, cursor, limit).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []topic.SourceHandle{}
	for rows.Next() {
		handle := topic.SourceHandle{Kind: kind}
		if err := rows.Scan(&handle.ID, &handle.Version); err != nil {
			return nil, err
		}
		result = append(result, handle)
	}
	return result, rows.Err()
}

func knownMaintenanceSources(tx *gorm.DB, fence scope, handles []topic.SourceHandle) (map[string]int64, error) {
	known := make(map[string]int64, len(handles))
	if len(handles) == 0 {
		return known, nil
	}
	ids := make([]string, 0, len(handles))
	for _, handle := range handles {
		ids = append(ids, handle.ID)
	}
	if err := tx.Exec(`UPDATE topic_maintenance_sources SET last_assignment_input_hash='',status='pending',reason='',
		pending_at=CASE WHEN status='pending' THEN pending_at ELSE clock_timestamp() END,
		revision=revision+1,updated_at=clock_timestamp()
		WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? AND source_kind='evidence'
		  AND source_id=ANY(?::text[]) AND eligible AND last_assignment_input_hash IS NULL
		  AND status IN ('organized','pending')`,
		fence.TeamID, fence.SpaceID, fence.Generation, pq.Array(ids)).Error; err != nil {
		return nil, err
	}
	rows, err := tx.Raw(`SELECT source_id,source_version FROM topic_maintenance_sources
		WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? AND source_kind='evidence' AND source_id=ANY(?::text[])`,
		fence.TeamID, fence.SpaceID, fence.Generation, pq.Array(ids)).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var version int64
		if err := rows.Scan(&id, &version); err != nil {
			return nil, err
		}
		known[id] = version
	}
	return known, rows.Err()
}

func refreshDiscoveredSource(tx *gorm.DB, fence scope, handle topic.SourceHandle, version int64) error {
	if version == handle.Version {
		return nil
	}
	return refreshMaintenanceSource(tx, fence, handle, false)
}

func refreshMaintenanceSource(tx *gorm.DB, fence scope, handle topic.SourceHandle, force bool) error {
	snapshot, err := readSource(tx, fence, handle)
	if err != nil && !errors.Is(err, topic.ErrSourceStale) {
		return err
	}
	if errors.Is(err, topic.ErrSourceStale) {
		latest, latestErr := latestMaintenanceHandle(tx, fence, handle)
		if latestErr != nil {
			return latestErr
		}
		if latest != handle {
			handle = latest
			snapshot, err = readSource(tx, fence, handle)
			if err != nil && !errors.Is(err, topic.ErrSourceStale) {
				return err
			}
		}
	}
	fingerprint, err := topic.SourceFingerprint(snapshot)
	if err != nil {
		return err
	}
	status, reason := "pending", ""
	if !snapshot.Eligible {
		status, reason = "unavailable", "source_unavailable"
	}
	equivalenceStatus := "pending"
	if !snapshot.Eligible {
		equivalenceStatus = "unavailable"
	}
	text := []rune(topic.SourceDisplayText(snapshot))
	_, vocabularyWords := topic.VocabularyQuery([]topic.SourceSnapshot{snapshot})
	if len(text) > 8192 {
		text = text[:8192]
	}
	// A pending revision may already be inside a live provider call with an older Topic shortlist.
	write := tx.Exec(`INSERT INTO topic_maintenance_sources(team_id,shared_space_id,space_generation,source_kind,source_id,source_version,fingerprint,meaning_key,search_tsv,vocabulary_words,eligible,status,reason,equivalence_status,equivalence_fingerprint)
	 VALUES(?::uuid,?::uuid,?,?,?,?,?,?,to_tsvector('simple',?),?::text[],?,?,?,?,?)
	 ON CONFLICT(team_id,shared_space_id,space_generation,source_kind,source_id) DO UPDATE SET
	 source_version=EXCLUDED.source_version,fingerprint=EXCLUDED.fingerprint,meaning_key=EXCLUDED.meaning_key,search_tsv=EXCLUDED.search_tsv,vocabulary_words=EXCLUDED.vocabulary_words,eligible=EXCLUDED.eligible,
	 equivalence_status=CASE WHEN topic_maintenance_sources.fingerprint<>EXCLUDED.fingerprint THEN EXCLUDED.equivalence_status ELSE topic_maintenance_sources.equivalence_status END,
	 equivalence_fingerprint=CASE WHEN topic_maintenance_sources.fingerprint<>EXCLUDED.fingerprint THEN EXCLUDED.equivalence_fingerprint ELSE topic_maintenance_sources.equivalence_fingerprint END,
	 equivalence_last_run_id=CASE WHEN topic_maintenance_sources.fingerprint<>EXCLUDED.fingerprint THEN NULL ELSE topic_maintenance_sources.equivalence_last_run_id END,
	 revision=topic_maintenance_sources.revision+1,status=EXCLUDED.status,reason=EXCLUDED.reason,
	 pending_at=CASE WHEN topic_maintenance_sources.status='pending' THEN topic_maintenance_sources.pending_at ELSE clock_timestamp() END,
	 updated_at=clock_timestamp() WHERE topic_maintenance_sources.fingerprint<>EXCLUDED.fingerprint OR (? AND (
	 topic_maintenance_sources.status<>'pending' OR EXISTS (
	   SELECT 1 FROM topic_maintenance_batches AS batch
	   WHERE (batch.team_id,batch.shared_space_id,batch.space_generation)=(EXCLUDED.team_id,EXCLUDED.shared_space_id,EXCLUDED.space_generation)
	     AND batch.status='running' AND batch.lease_until>clock_timestamp()
	     AND batch.revisions->>(EXCLUDED.source_kind || ':' || EXCLUDED.source_id)=topic_maintenance_sources.revision::text
	 )))`, fence.TeamID, fence.SpaceID, fence.Generation, handle.Kind, handle.ID, handle.Version, fingerprint, snapshot.MeaningKey, string(text), pq.Array(vocabularyWords), snapshot.Eligible, status, reason, equivalenceStatus, fingerprint, force)
	if write.Error == nil && write.RowsAffected > 0 && status == "pending" {
		observeMaintenanceSource(tx.Statement.Context, fence, handle, "source_requeued", nil)
	}
	return write.Error
}

func latestMaintenanceHandle(tx *gorm.DB, fence scope, handle topic.SourceHandle) (topic.SourceHandle, error) {
	if handle.Kind != topic.EvidenceSource {
		return handle, topic.ErrInvalid
	}
	return handle, nil
}

func refreshMaintenanceInvalidation(tx *gorm.DB, fence scope, handle topic.SourceHandle, definition bool) error {
	if err := refreshMaintenanceSource(tx, fence, handle, definition); err != nil {
		return err
	}
	if !definition {
		if err := enqueueMaintenanceDependencies(tx, fence, handle); err != nil {
			return err
		}

	}
	return nil
}

func enqueueMaintenanceDependencies(tx *gorm.DB, fence scope, handle topic.SourceHandle) error {
	return tx.Exec(`INSERT INTO topic_maintenance_markers(team_id,shared_space_id,space_generation,anchor_kind,anchor_id,target_kind,target_id)
 SELECT ?::uuid,?::uuid,?,'source_dependency',?,'definition',? WHERE EXISTS(
 SELECT 1 FROM topic_source_dependencies AS dependency JOIN topic_record_heads AS head
 ON head.team_id=dependency.team_id AND head.shared_space_id=dependency.shared_space_id AND head.space_generation=dependency.space_generation AND head.record_id=dependency.record_id AND head.version=dependency.record_version
 WHERE dependency.team_id=?::uuid AND dependency.shared_space_id=?::uuid AND dependency.space_generation=? AND dependency.source_kind=? AND dependency.source_id=? AND NOT head.retired)
 ON CONFLICT(team_id,shared_space_id,space_generation,anchor_kind,anchor_id) DO UPDATE SET marker_sequence=nextval('topic_maintenance_marker_seq'),cursor=''`, fence.TeamID, fence.SpaceID, fence.Generation, topic.SourceKey(handle), handle.ID, fence.TeamID, fence.SpaceID, fence.Generation, handle.Kind, handle.ID).Error
}

func maintenanceMarkerSources(tx *gorm.DB, fence scope, marker maintenanceMarker, limit int) ([]topic.SourceHandle, error) {
	result := []topic.SourceHandle{}
	appendRows := func(query string, args ...any) error {
		rows, err := tx.Raw(query, args...).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var handle topic.SourceHandle
			if err := rows.Scan(&handle.Kind, &handle.ID, &handle.Version); err != nil {
				return err
			}
			result = append(result, handle)
		}
		return rows.Err()
	}
	if marker.TargetKind == "definition" {
		kind, id, _ := strings.Cut(marker.Cursor, ":")
		recordID, sourceKind, sourceID := marker.TargetID, "", ""
		if marker.AnchorKind == "source_dependency" {
			recordID = ""
			sourceKind, _, _ = strings.Cut(marker.AnchorID, ":")
			sourceID = marker.TargetID
		}
		err := appendRows(maintenanceDefinitionPageSQL, fence.TeamID, fence.SpaceID, fence.Generation, recordID, sourceKind, sourceID, kind, id, limit)
		return result, err
	}
	direct := topic.SourceHandle{ID: marker.TargetID, Version: 1}
	switch marker.TargetKind {
	case "evidence":
		direct.Kind = topic.EvidenceSource
	case "source", "ingest":
	default:
		return nil, fmt.Errorf("%w: unknown invalidation kind", topic.ErrInvalid)
	}
	if direct.Kind != "" && topic.SourceKey(direct) > marker.Cursor {
		latest, err := latestMaintenanceHandle(tx, fence, direct)
		if err != nil {
			return nil, err
		}
		result = append(result, latest)
	}
	if len(result) == limit || marker.TargetKind == "evidence" {
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
	return result, nil
}

func maintenanceLexicalQuery(snapshot topic.SourceSnapshot) string {
	text := topic.SourceDisplayText(snapshot)
	words := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r > 127)
	})
	if len(words) > 64 {
		words = words[:64]
	}
	return topic.LooseVocabularyQuery(words)
}
