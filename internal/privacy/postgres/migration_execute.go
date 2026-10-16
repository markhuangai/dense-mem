package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/lib/pq"
	"gorm.io/gorm"

	privacy "github.com/markhuangai/dense-mem/internal/privacy/contract"
)

type privateMigrationMove struct {
	ID, Team, Table, SourceKey, SourceSpace, DestinationSpace, DestinationKey, Membership string
	SourceGeneration, Generation                                                          int64
	Owners                                                                                []string
	Replacements                                                                          map[string]json.RawMessage
	Copy, Sealed                                                                          bool
}

// MigratePrivateMemory runs only after the ordered maintenance boundary has
// stopped ordinary writers; each committed batch and mapping is restartable.
func (r *PrivateMemoryRepositoryImpl) MigratePrivateMemory(ctx context.Context, fingerprint string) error {
	if !strings.HasPrefix(fingerprint, "sha256:") || len(fingerprint) != 71 {
		return fmt.Errorf("%w: source fingerprint is required", privacy.ErrPrivateMemoryMigrationBlocked)
	}
	var phase, expected string
	if err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT phase,COALESCE(source_fingerprint,'') FROM private_memory_migration_state WHERE singleton`).Row().Scan(&phase, &expected)
	}); err != nil {
		return err
	}
	if expected != "" && expected != fingerprint {
		return fmt.Errorf("%w: source fingerprint differs from the retained migration", privacy.ErrPrivateMemoryMigrationBlocked)
	}
	if phase == "complete" {
		if expected == "" {
			return fmt.Errorf("%w: database already initialized without legacy private history", privacy.ErrPrivateMemoryMigrationBlocked)
		}
		return nil
	}
	if phase == "ready" {
		if expected != "" {
			return nil
		}
		return r.privateMigrationTx(ctx, func(tx *gorm.DB) error {
			plan, err := planPrivateMemoryMigration(ctx, tx)
			if err != nil {
				return err
			}
			fresh := sha256.Sum256([]byte(privacy.PrivateMemoryPlannerVersion + "\nfresh\n"))
			if plan.Report.SourceFingerprint != fingerprint {
				var teams int64
				if err := tx.Raw(`SELECT count(*) FROM teams`).Row().Scan(&teams); err != nil {
					return err
				}
				if teams != 0 || fingerprint != "sha256:"+hex.EncodeToString(fresh[:]) {
					return privacy.ErrPrivateMemoryMigrationBlocked
				}
			}
			if len(plan.Report.Blockers) > 0 {
				return privacy.ErrPrivateMemoryMigrationBlocked
			}
			plan.Report.SourceFingerprint = fingerprint
			raw, err := json.Marshal(plan.Report)
			if err != nil {
				return err
			}
			return tx.Exec(`UPDATE private_memory_migration_state SET source_fingerprint=?,report=?::jsonb WHERE singleton AND phase='ready'`, fingerprint, string(raw)).Error
		})
	}
	if phase == "pending" {
		var plan *privateMigrationPlan
		err := r.rls.WithSystemReadOnlyRepeatableTx(ctx, r.db, func(tx *gorm.DB) error { var err error; plan, err = planPrivateMemoryMigration(ctx, tx); return err })
		if err != nil {
			return err
		}
		if len(plan.Report.Blockers) > 0 || plan.Report.SourceFingerprint != fingerprint {
			return fmt.Errorf("%w: rerun preflight against the stopped database", privacy.ErrPrivateMemoryMigrationBlocked)
		}
		if err := r.stagePrivateMigration(ctx, plan); err != nil {
			return err
		}
	}
	for _, copies := range []bool{true, false} {
		for {
			count, err := r.movePrivateMigrationBatch(ctx, copies)
			if err != nil {
				return err
			}
			if count == 0 {
				break
			}
		}
	}
	for {
		count, err := r.rebuildPrivateMigrationBatch(ctx)
		if err != nil {
			return err
		}
		if count == 0 {
			break
		}
	}
	return r.finishPrivateMigration(ctx)
}

func (r *PrivateMemoryRepositoryImpl) privateMigrationTx(ctx context.Context, fn func(*gorm.DB) error) error {
	return r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		if err := tx.Exec(`SELECT pg_advisory_xact_lock(573,2700)`).Error; err != nil {
			return err
		}
		if err := tx.Exec(`SELECT set_config('app.tx_mode','migration',true)`).Error; err != nil {
			return err
		}
		return fn(tx)
	})
}

func (p *privateMigrationPlan) moves() []*privateMigrationMove {
	moves := make([]*privateMigrationMove, 0, len(p.Records))
	byRecord := map[*privateMigrationRecord]map[string]*privateMigrationMove{}
	for _, record := range p.Records {
		members := sortedMigrationKeys(record.Destinations)
		if record.Sealed {
			members = []string{p.Spaces[record.SourceSpace].OwnerMembership}
		}
		primary := record.Membership
		if !slices.Contains(members, primary) {
			primary = members[0]
		}
		byRecord[record] = map[string]*privateMigrationMove{}
		for _, member := range members {
			destination := p.Destinations[member]
			generation := int64(1)
			if space := p.Spaces[destination]; space != nil {
				generation = space.Generation
				if len(space.Writers) > 1 {
					generation++
				}
			}
			if record.Sealed {
				destination = migrationID(record.SourceSpace, "archive")
				generation = record.Generation
			}
			move := &privateMigrationMove{Team: record.Team, Table: record.Table.Name, SourceKey: record.Key, SourceSpace: record.SourceSpace, SourceGeneration: record.Generation,
				DestinationSpace: destination, Generation: generation, Membership: member, Copy: member != primary, Sealed: record.Sealed, Replacements: map[string]json.RawMessage{}}
			move.ID = migrationID(record.SourceSpace, record.Table.Name+":"+record.Key+":"+destination)
			if record.Membership != "" && !(privateMigrationIsSealed(record.Table.Name) && len(p.Spaces[record.SourceSpace].Writers) > 1) {
				move.Owners = []string{record.Membership}
			} else {
				move.Owners = sortedMigrationKeys(p.Spaces[record.SourceSpace].Writers)
			}
			if len(move.Owners) == 0 {
				move.Owners = []string{p.Spaces[record.SourceSpace].OwnerMembership}
			}
			if record.RestoreConflict && !move.Sealed {
				move.Replacements["status"] = json.RawMessage(`"active"`)
				move.Replacements["valid_to"] = record.RestoredValidTo
				move.Replacements["recorded_to"] = json.RawMessage(`null`)
				var version int
				_ = json.Unmarshal(record.Fields["version"], &version)
				move.Replacements["version"], _ = json.Marshal(version + 1)
				move.Replacements["_restore_conflict"] = json.RawMessage(`true`)
			}
			move.Replacements["space_id"], _ = json.Marshal(destination)
			move.Replacements["space_generation"], _ = json.Marshal(generation)
			if record.Table.Name == "audit_log" {
				move.Replacements["memory_space_id"] = move.Replacements["space_id"]
				delete(move.Replacements, "space_id")
				delete(move.Replacements, "space_generation")
			}
			if move.Copy {
				for _, column := range record.Table.Columns {
					if column.Type == "uuid" && column.Name != "team_id" && column.Name != "space_id" && slices.Contains(record.Table.Key, column.Name) {
						move.Replacements[column.Name], _ = json.Marshal(migrationID(destination, "record:"+migrationString(record.Fields[column.Name])))
					}
				}
			}
			moves = append(moves, move)
			byRecord[record][member] = move
		}
	}
	// Composite primary keys can inherit a remapped parent identity.
	for iteration := 0; iteration < len(p.Tables); iteration++ {
		changed := false
		for record, targets := range byRecord {
			for member, move := range targets {
				if move.Sealed {
					continue
				}
				for _, fk := range record.Table.ForeignKeys {
					for _, parent := range record.References {
						if parent.Table.Name != fk.Parent || migrationFieldKey(record.Fields, fk.Columns) != migrationFieldKey(parent.Fields, fk.ParentColumns) {
							continue
						}
						parentMove := byRecord[parent][member]
						if parentMove == nil {
							continue
						}
						for i, column := range fk.Columns {
							value := parent.Fields[fk.ParentColumns[i]]
							if replaced, ok := parentMove.Replacements[fk.ParentColumns[i]]; ok {
								value = replaced
							}
							if string(move.Replacements[column]) != string(value) {
								move.Replacements[column] = value
								changed = true
							}
						}
					}
				}
			}
		}
		if !changed {
			break
		}
	}
	for record, targets := range byRecord {
		for _, move := range targets {
			fields := map[string]json.RawMessage{}
			for k, v := range record.Fields {
				fields[k] = v
			}
			for k, v := range move.Replacements {
				fields[k] = v
			}
			move.DestinationKey = migrationFieldKey(fields, record.Table.Key)
		}
	}
	return moves
}

func (r *PrivateMemoryRepositoryImpl) stagePrivateMigration(ctx context.Context, p *privateMigrationPlan) error {
	moves := p.archiveParentMoves(p.moves())
	if err := p.addMoveReferences(moves); err != nil {
		return err
	}
	if err := r.privateMigrationTx(ctx, func(tx *gorm.DB) error {
		var expected string
		if err := tx.Raw(`SELECT COALESCE(source_fingerprint,'') FROM private_memory_migration_state WHERE singleton FOR UPDATE`).Row().Scan(&expected); err != nil {
			return err
		}
		if expected != "" && expected != p.Report.SourceFingerprint {
			return privacy.ErrPrivateMemoryMigrationBlocked
		}
		raw, err := json.Marshal(p.Report)
		if err != nil {
			return err
		}
		if err := tx.Exec(`UPDATE private_memory_migration_state SET source_fingerprint=?,report=?::jsonb,updated_at=now() WHERE singleton AND phase='pending'`, p.Report.SourceFingerprint, string(raw)).Error; err != nil {
			return err
		}
		for _, move := range moves {
			if p.Spaces[move.DestinationSpace] != nil {
				continue
			}
			state := "active"
			if move.Sealed {
				state = "sealed"
			}
			if err := tx.Exec(`INSERT INTO memory_spaces(id,team_id,kind,owner_membership_id,generation,lifecycle_state,migration_archive)
    VALUES(?::uuid,?::uuid,'private',?::uuid,?,?,?) ON CONFLICT(id) DO NOTHING`, move.DestinationSpace, move.Team, move.Membership, move.Generation, state, move.Sealed).Error; err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	for start := 0; start < len(moves); start += privateMigrationBatchSize {
		batch := moves[start:min(start+privateMigrationBatchSize, len(moves))]
		if err := r.privateMigrationTx(ctx, func(tx *gorm.DB) error {
			for _, move := range batch {
				raw, err := json.Marshal(move.Replacements)
				if err != nil {
					return err
				}
				if err := tx.Exec(`INSERT INTO private_memory_migration_records(record_id,team_id,source_table,source_key,source_space_id,source_generation,original_membership_ids,destination_space_id,destination_generation,destination_key,destination_membership_id,replacements,supporting_copy,sealed)
     VALUES(?::uuid,?::uuid,?,?::jsonb,?::uuid,?,?::uuid[],?::uuid,?,?::jsonb,?::uuid,?::jsonb,?,?) ON CONFLICT(record_id) DO NOTHING`, move.ID, move.Team, move.Table, move.SourceKey, move.SourceSpace, move.SourceGeneration, pq.Array(move.Owners), move.DestinationSpace, move.Generation, move.DestinationKey, move.Membership, string(raw), move.Copy, move.Sealed).Error; err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return r.privateMigrationTx(ctx, func(tx *gorm.DB) error {
		for _, table := range p.Tables {
			for _, fk := range table.ForeignKeys {
				if p.Tables[fk.Parent] == nil {
					continue
				}
				if err := tx.Exec(`INSERT INTO private_memory_migration_constraints(table_name,constraint_name,definition) VALUES(?,?,?) ON CONFLICT DO NOTHING`, table.Name, fk.Name, fk.Definition).Error; err != nil {
					return err
				}
				if err := tx.Exec(`ALTER TABLE ` + pq.QuoteIdentifier(table.Name) + ` DROP CONSTRAINT IF EXISTS ` + pq.QuoteIdentifier(fk.Name)).Error; err != nil {
					return err
				}
			}
		}
		return tx.Exec(`UPDATE private_memory_migration_state SET phase='planned',updated_at=now() WHERE singleton`).Error
	})
}

func migrationMutationColumns(table *privateMigrationTable) []string {
	var columns []string
	for _, c := range table.Columns {
		if !c.Generated {
			columns = append(columns, c.Name)
		}
	}
	return columns
}

func (r *PrivateMemoryRepositoryImpl) movePrivateMigrationBatch(ctx context.Context, copies bool) (int, error) {
	count := 0
	err := r.privateMigrationTx(ctx, func(tx *gorm.DB) error {
		tables, err := inspectPrivateMigrationSchema(tx)
		if err != nil {
			return err
		}
		var name string
		if err := tx.Raw(`SELECT COALESCE(min(source_table),'') FROM private_memory_migration_records WHERE NOT completed AND supporting_copy=?`, copies).Row().Scan(&name); err != nil {
			return err
		}
		if name == "" {
			return nil
		}
		phase := "moving"
		if copies {
			phase = "copying"
		}
		if err := tx.Exec(`UPDATE private_memory_migration_state SET phase=?,updated_at=now() WHERE singleton`, phase).Error; err != nil {
			return err
		}
		table := tables[name]
		if table == nil {
			return privacy.ErrPrivateMemoryMigrationBlocked
		}
		rows, err := tx.Raw(`SELECT record_id::text,source_key::text,destination_key::text,replacements,supporting_copy,sealed
   FROM private_memory_migration_records WHERE source_table=? AND NOT completed AND supporting_copy=? ORDER BY record_id LIMIT ? FOR UPDATE`, name, copies, privateMigrationBatchSize).Rows()
		if err != nil {
			return err
		}
		moves := []*privateMigrationMove{}
		for rows.Next() {
			m := &privateMigrationMove{Table: name}
			var raw []byte
			if err = rows.Scan(&m.ID, &m.SourceKey, &m.DestinationKey, &raw, &m.Copy, &m.Sealed); err != nil {
				rows.Close()
				return err
			}
			if err = json.Unmarshal(raw, &m.Replacements); err != nil {
				rows.Close()
				return err
			}
			moves = append(moves, m)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		restore, err := privateMigrationSuspendTriggers(tx, table.Name)
		if err != nil {
			return err
		}
		if err := tx.Exec(`CREATE POLICY private_memory_migration_batch ON ` + pq.QuoteIdentifier(name) + ` USING(current_setting('app.tx_mode',true)='migration') WITH CHECK(current_setting('app.tx_mode',true)='migration')`).Error; err != nil {
			return err
		}
		for _, move := range moves {
			var raw []byte
			if err := tx.Raw(`SELECT to_jsonb(row) FROM `+pq.QuoteIdentifier(name)+` row WHERE `+migrationKeySQL("row", table.Key)+`=?::jsonb`, move.SourceKey).Row().Scan(&raw); err != nil {
				return err
			}
			var body map[string]json.RawMessage
			if err := json.Unmarshal(raw, &body); err != nil {
				return err
			}
			for key, value := range move.Replacements {
				if !strings.HasPrefix(key, "_") {
					body[key] = value
				}
			}
			if err := applyPrivateMigrationReferences(move, body); err != nil {
				return err
			}
			if move.Copy && !move.Sealed && migrationString(body["idempotency_key"]) != "" {
				body["idempotency_key"], _ = json.Marshal("migration-support:" + move.ID)
			}
			if move.Copy && name == "remember_attempts" {
				body["idempotency_key"], _ = json.Marshal("migration-support:" + move.ID)
				body["public_result"] = json.RawMessage(`{}`)
			}
			if move.Copy && name == "knowledge_ingests" {
				body["idempotency_key"] = json.RawMessage(`""`)
				body["source_summary"] = json.RawMessage(`""`)
				body["proposal"] = json.RawMessage(`{}`)
				body["metadata"] = json.RawMessage(`{"migration_support_container":true}`)
				body["status"] = json.RawMessage(`"completed"`)
				if string(body["completed_at"]) == "null" {
					body["completed_at"] = body["created_at"]
				}
			}
			data, err := json.Marshal(body)
			if err != nil {
				return err
			}
			columns := migrationMutationColumns(table)
			quoted := make([]string, len(columns))
			selected := make([]string, len(columns))
			for i, c := range columns {
				quoted[i] = pq.QuoteIdentifier(c)
				selected[i] = "mapped." + quoted[i]
			}
			query := `INSERT INTO ` + pq.QuoteIdentifier(name) + ` (` + strings.Join(quoted, ",") + `) SELECT ` + strings.Join(selected, ",") + ` FROM jsonb_populate_record(NULL::` + pq.QuoteIdentifier(name) + `,?::jsonb) mapped`
			args := []any{string(data)}
			if !move.Copy {
				assign := make([]string, len(columns))
				for i, c := range quoted {
					assign[i] = c + "=" + selected[i]
				}
				query = `UPDATE ` + pq.QuoteIdentifier(name) + ` row SET ` + strings.Join(assign, ",") + ` FROM jsonb_populate_record(NULL::` + pq.QuoteIdentifier(name) + `,?::jsonb) mapped WHERE ` + migrationKeySQL("row", table.Key) + `=?::jsonb`
				args = append(args, move.SourceKey)
			}
			result := tx.Exec(query, args...)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return fmt.Errorf("migration row changed: %s", name)
			}
			if err := tx.Exec(`UPDATE private_memory_migration_records SET completed=true WHERE record_id=?::uuid`, move.ID).Error; err != nil {
				return err
			}
			count++
		}
		if err := tx.Exec(`DROP POLICY private_memory_migration_batch ON ` + pq.QuoteIdentifier(name)).Error; err != nil {
			return err
		}
		return restore()
	})
	return count, err
}

func privateMigrationSuspendTriggers(tx *gorm.DB, table string) (func() error, error) {
	type trigger struct{ Name, State string }
	var triggers []trigger
	rows, err := tx.Raw(`SELECT tgname,tgenabled::text FROM pg_trigger WHERE tgrelid=?::regclass AND NOT tgisinternal AND tgenabled<>'D' ORDER BY tgname`, "public."+table).Rows()
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var t trigger
		if err = rows.Scan(&t.Name, &t.State); err != nil {
			rows.Close()
			return nil, err
		}
		triggers = append(triggers, t)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, t := range triggers {
		if err := tx.Exec(`ALTER TABLE ` + pq.QuoteIdentifier(table) + ` DISABLE TRIGGER ` + pq.QuoteIdentifier(t.Name)).Error; err != nil {
			return nil, err
		}
	}
	return func() error {
		for _, t := range triggers {
			mode := "ENABLE"
			if t.State == "A" {
				mode = "ENABLE ALWAYS"
			}
			if t.State == "R" {
				mode = "ENABLE REPLICA"
			}
			if err := tx.Exec(`ALTER TABLE ` + pq.QuoteIdentifier(table) + ` ` + mode + ` TRIGGER ` + pq.QuoteIdentifier(t.Name)).Error; err != nil {
				return err
			}
		}
		return nil
	}, nil
}

func (r *PrivateMemoryRepositoryImpl) finishPrivateMigration(ctx context.Context) error {
	return r.privateMigrationTx(ctx, func(tx *gorm.DB) error {
		var remaining int64
		if err := tx.Raw(`SELECT count(*) FROM private_memory_migration_records WHERE NOT completed`).Row().Scan(&remaining); err != nil {
			return err
		}
		if remaining != 0 {
			return errors.New("private migration still has unfinished rows")
		}
		rows, err := tx.Raw(`SELECT table_name,constraint_name,definition FROM private_memory_migration_constraints ORDER BY table_name,constraint_name`).Rows()
		if err != nil {
			return err
		}
		type constraint struct{ Table, Name, Definition string }
		var constraints []constraint
		for rows.Next() {
			var c constraint
			if err := rows.Scan(&c.Table, &c.Name, &c.Definition); err != nil {
				rows.Close()
				return err
			}
			constraints = append(constraints, c)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, c := range constraints {
			if err := tx.Exec(`ALTER TABLE ` + pq.QuoteIdentifier(c.Table) + ` ADD CONSTRAINT ` + pq.QuoteIdentifier(c.Name) + ` ` + c.Definition).Error; err != nil {
				return err
			}
		}
		if err := tx.Exec(`UPDATE memory_spaces space SET generation=dest.generation FROM (SELECT destination_space_id,max(destination_generation) generation FROM private_memory_migration_records GROUP BY destination_space_id) dest WHERE space.id=dest.destination_space_id`).Error; err != nil {
			return err
		}
		return tx.Exec(`UPDATE private_memory_migration_state SET phase='ready',updated_at=now() WHERE singleton`).Error
	})
}

func (p *privateMigrationPlan) archiveParentMoves(moves []*privateMigrationMove) []*privateMigrationMove {
	seen := map[string]bool{}
	for _, move := range moves {
		seen[move.ID] = true
	}
	for _, record := range p.Records {
		if !record.Sealed {
			continue
		}
		archive := migrationID(record.SourceSpace, "archive")
		for _, fk := range record.Table.ForeignKeys {
			if !slices.Contains(fk.Columns, "space_id") {
				continue
			}
			for _, parent := range record.References {
				if parent.Table.Name != fk.Parent || parent.Sealed || migrationFieldKey(record.Fields, fk.Columns) != migrationFieldKey(parent.Fields, fk.ParentColumns) {
					continue
				}
				id := migrationID(parent.SourceSpace, parent.Table.Name+":"+parent.Key+":"+archive)
				if seen[id] {
					continue
				}
				seen[id] = true
				replacement := map[string]json.RawMessage{}
				replacement["space_id"], _ = json.Marshal(archive)
				replacement["space_generation"], _ = json.Marshal(record.Generation)
				fields := map[string]json.RawMessage{}
				for key, value := range parent.Fields {
					fields[key] = value
				}
				for key, value := range replacement {
					fields[key] = value
				}
				moves = append(moves, &privateMigrationMove{ID: id, Team: parent.Team, Table: parent.Table.Name, SourceKey: parent.Key, SourceSpace: parent.SourceSpace, SourceGeneration: parent.Generation,
					DestinationSpace: archive, Generation: record.Generation, DestinationKey: migrationFieldKey(fields, parent.Table.Key), Membership: p.Spaces[parent.SourceSpace].OwnerMembership,
					Owners: []string{parent.Membership}, Replacements: replacement, Copy: true, Sealed: true})
			}
		}
	}
	return moves
}
