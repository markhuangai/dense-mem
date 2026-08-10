package repository

import (
	"context"
	"database/sql"
	"time"

	"gorm.io/gorm"
)

const searchConvergenceIncidentLimit = 100

func readSearchConvergenceIncidents(ctx context.Context, tx *gorm.DB, contractID string, dimensions int) ([]EmbeddingFailureIncident, int64, bool, error) {
	var incidentCount int64
	if err := tx.WithContext(ctx).Raw(`
		SELECT count(*)
		FROM embedding_failure_incidents AS incident
		JOIN teams AS team
		  ON team.id = incident.team_id
		 AND team.status = 'active'
		 AND team.deleted_at IS NULL
		WHERE incident.embedding_contract_id = ?::uuid
		  AND incident.embedding_dimensions = ?
		  AND incident.status IN ('open', 'recovering')
	`, contractID, dimensions).Scan(&incidentCount).Error; err != nil {
		return nil, 0, false, err
	}
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT incident.incident_id::text, incident.team_id::text,
		       COALESCE(team.name, ''), incident.embedding_contract_id::text,
		       incident.embedding_dimensions, incident.source_kind,
		       incident.failure_class, incident.failure_code, incident.status,
		       incident.affected_job_count, incident.first_seen_at,
		       incident.last_seen_at, incident.recovering_at, incident.resolved_at
		FROM embedding_failure_incidents AS incident
		JOIN teams AS team
		  ON team.id = incident.team_id
		 AND team.status = 'active'
		 AND team.deleted_at IS NULL
		WHERE incident.embedding_contract_id = ?::uuid
		  AND incident.embedding_dimensions = ?
		  AND incident.status IN ('open', 'recovering')
		ORDER BY incident.last_seen_at DESC, incident.incident_id ASC
		LIMIT ?
	`, contractID, dimensions, searchConvergenceIncidentLimit).Rows()
	if err != nil {
		return nil, 0, false, err
	}
	defer rows.Close()
	incidents := make([]EmbeddingFailureIncident, 0, minInt64(incidentCount, searchConvergenceIncidentLimit))
	for rows.Next() {
		var item EmbeddingFailureIncident
		var recoveringAt, resolvedAt sql.NullTime
		if err := rows.Scan(
			&item.IncidentID, &item.TeamID, &item.TeamName,
			&item.EmbeddingContractID, &item.EmbeddingDimensions,
			&item.SourceKind, &item.FailureClass, &item.FailureCode,
			&item.Status, &item.AffectedJobCount, &item.FirstSeenAt,
			&item.LastSeenAt, &recoveringAt, &resolvedAt,
		); err != nil {
			return nil, 0, false, err
		}
		if recoveringAt.Valid {
			value := recoveringAt.Time.UTC()
			item.RecoveringAt = &value
		}
		if resolvedAt.Valid {
			value := resolvedAt.Time.UTC()
			item.ResolvedAt = &value
		}
		item.Age = time.Since(item.LastSeenAt)
		item.Guidance = embeddingFailureGuidance(item.FailureCode)
		incidents = append(incidents, item)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, false, err
	}
	return incidents, incidentCount, incidentCount > int64(len(incidents)), nil
}

func minInt64(value int64, limit int) int {
	if value < int64(limit) {
		return int(value)
	}
	return limit
}
