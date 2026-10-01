package postgres

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/lib/pq"
)

func scanCommunityRun(rows *sql.Rows) (*CommunityRun, error) {
	var completedAt sql.NullTime
	run := CommunityRun{}
	if err := rows.Scan(&run.TeamID, &run.RunID, &run.WindowKey, &run.Status,
		&run.AlgorithmKind, &run.AlgorithmVersion, &run.ProfileVersion,
		&run.ConfigurationHash, &run.SourceFingerprint, &run.NodeCount,
		&run.EdgeCount, &run.CommunityCount, &run.MaxNodes, &run.MaxEdges,
		&run.Error, &run.StartedAt, &completedAt, &run.Claimed); err != nil {
		return nil, err
	}
	if completedAt.Valid {
		run.CompletedAt = &completedAt.Time
	}
	return &run, nil
}

func scanCommunityRecords(rows *sql.Rows) ([]CommunityRecord, error) {
	out := []CommunityRecord{}
	for rows.Next() {
		record := CommunityRecord{}
		var topEntities, topPredicates pq.StringArray
		var supersededAt sql.NullTime
		if err := rows.Scan(&record.TeamID, &record.CommunityID, &record.LogicalCommunityID,
			&record.RunID,
			&record.Ordinal, &record.Status, &record.Summary,
			&record.SummaryVersion, &record.MemberCount, &record.SourceCount,
			&topEntities, &topPredicates, &record.SourceFingerprint,
			&record.StaleReason, &record.CreatedAt, &record.UpdatedAt,
			&supersededAt); err != nil {
			return nil, err
		}
		record.TopEntities = []string(topEntities)
		record.TopPredicates = []string(topPredicates)
		if supersededAt.Valid {
			record.SupersededAt = &supersededAt.Time
		}
		out = append(out, record)
	}
	return out, rows.Err()
}

func marshalCommunitySnapshot(value []map[string]any) ([]byte, error) {
	if value == nil {
		value = []map[string]any{}
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal community source snapshot: %w", err)
	}
	return encoded, nil
}
