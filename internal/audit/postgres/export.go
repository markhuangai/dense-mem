package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/markhuangai/dense-mem/internal/audit/contract"
	"gorm.io/gorm"
)

var _ contract.ExportStore = (*Store)(nil)

func (s *Store) ReadExport(ctx context.Context, teamID string, after *contract.ExportPosition, limit int) (contract.ExportRead, error) {
	result := contract.ExportRead{Entries: make([]contract.ExportEntry, 0)}
	if s == nil || s.db == nil || s.rls == nil {
		return result, errors.New("audit export: system transaction is required")
	}
	err := s.rls.WithSystemTx(ctx, s.db, func(tx *gorm.DB) error {
		if err := tx.WithContext(ctx).Exec("SET LOCAL statement_timeout = '5s'").Error; err != nil {
			return err
		}
		if err := tx.WithContext(ctx).Raw("SELECT pg_snapshot_xmin(pg_current_snapshot())::text").Row().Scan(&result.Frontier); err != nil {
			return err
		}
		where := "insertion_xid < $1::xid8"
		args := []any{result.Frontier}
		if teamID != "" {
			args = append(args, teamID)
			where += fmt.Sprintf(" AND team_id = $%d::uuid", len(args))
		}
		if after != nil {
			n := len(args)
			args = append(args, after.XID, after.Timestamp, after.ID)
			where += fmt.Sprintf(" AND (insertion_xid, timestamp, id) > ($%d::xid8, $%d::timestamptz, $%d::uuid)", n+1, n+2, n+3)
		}
		args = append(args, limit)
		query := fmt.Sprintf(`SELECT insertion_xid::text, timestamp, id::text, team_id::text, operation, entity_type,
			CASE WHEN length(entity_id) <= 36 THEN entity_id ELSE '' END, actor_profile_id::text, actor_role
			FROM audit_log WHERE %s ORDER BY insertion_xid, timestamp, id LIMIT $%d`, where, len(args))
		rows, err := tx.WithContext(ctx).Raw(query, args...).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var entry contract.ExportEntry
			var team, actor, role sql.NullString
			if err := rows.Scan(&entry.Position.XID, &entry.Position.Timestamp, &entry.Position.ID, &team, &entry.Operation, &entry.EntityType, &entry.EntityID, &actor, &role); err != nil {
				return err
			}
			if team.Valid {
				entry.TeamID = &team.String
			}
			if actor.Valid {
				entry.ActorID = &actor.String
			}
			entry.ActorRole = role.String
			result.Entries = append(result.Entries, entry)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		delayedWhere := "insertion_xid >= $1::xid8"
		delayedArgs := []any{result.Frontier}
		if teamID != "" {
			delayedWhere += " AND team_id = $2::uuid"
			delayedArgs = append(delayedArgs, teamID)
		}
		return tx.WithContext(ctx).Raw("SELECT EXISTS (SELECT 1 FROM audit_log WHERE "+delayedWhere+")", delayedArgs...).Row().Scan(&result.Delayed)
	})
	return result, err
}
