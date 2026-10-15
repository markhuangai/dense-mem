package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"

	privacy "github.com/markhuangai/dense-mem/internal/privacy/contract"
)

const privateMigrationBatchSize = 500

// These are projections or context-bearing histories, rather than independent
// writer evidence; mixed versions must remain sealed after the split.
var privateMigrationSealedTables = map[string]bool{
	"audit_log":               true,
	"entity_correction_plans": true, "entity_correction_events": true,
	"remember_attempt_diagnostics": true, "remember_failure_artifacts": true, "remember_invocation_diagnostics": true,
	"semantic_assessments": true, "review_tasks": true,
	"session_extraction_checkpoints": true, "session_submission_diagnostics": true,
	"relationship_cross_references": true, "recall_feedback_events": true,
}

var privateMigrationLinkedTables = map[string]bool{
	"entity_records": true, "value_records": true, "search_documents": true,
}

type privateMigrationColumn struct {
	Name, Type string
	Generated  bool
}
type privateMigrationForeignKey struct {
	Name, Parent, Definition string
	Columns, ParentColumns   []string
}
type privateMigrationTable struct {
	Name        string
	Columns     []privateMigrationColumn
	Key         []string
	ForeignKeys []privateMigrationForeignKey
}
type privateMigrationRecord struct {
	Table                                *privateMigrationTable
	Key                                  string
	Team, SourceSpace, Owner, Membership string
	Generation                           int64
	Fields                               map[string]json.RawMessage
	References                           []*privateMigrationRecord
	Destinations                         map[string]bool
	Sealed                               bool
	RestoreConflict                      bool
	RestoredValidTo                      json.RawMessage
}
type privateMigrationSpace struct {
	ID, Team, Kind, OwnerMembership, State string
	Generation                             int64
	Archive                                bool
	Writers                                map[string]bool
}
type privateMigrationPlan struct {
	Report       *privacy.PrivateMemoryMigrationReport
	Tables       map[string]*privateMigrationTable
	Records      []*privateMigrationRecord
	Spaces       map[string]*privateMigrationSpace
	Memberships  map[string]string
	Destinations map[string]string
}

func privateMigrationIsSealed(table string) bool {
	return privateMigrationSealedTables[table] || strings.HasPrefix(table, "community_") ||
		strings.HasPrefix(table, "dream_") || strings.HasPrefix(table, "hypothes") ||
		strings.HasPrefix(table, "relationship_conflict_") || strings.HasPrefix(table, "evidence_conflict_")
}

func inspectPrivateMigrationSchema(tx *gorm.DB) (map[string]*privateMigrationTable, error) {
	tables := map[string]*privateMigrationTable{}
	for _, name := range privateMemoryMigrationTables() {
		tables[name] = &privateMigrationTable{Name: name}
	}
	rows, err := tx.Raw(`SELECT c.relname, a.attname, format_type(a.atttypid,a.atttypmod), a.attgenerated <> ''
  FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
  JOIN pg_attribute a ON a.attrelid=c.oid
  WHERE n.nspname='public' AND c.relname=ANY(?::text[]) AND a.attnum>0 AND NOT a.attisdropped
  ORDER BY c.relname,a.attnum`, pq.Array(privateMemoryMigrationTables())).Rows()
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var table string
		var c privateMigrationColumn
		if err = rows.Scan(&table, &c.Name, &c.Type, &c.Generated); err != nil {
			rows.Close()
			return nil, err
		}
		tables[table].Columns = append(tables[table].Columns, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = tx.Raw(`SELECT child.relname,k.conname,k.contype::text,COALESCE(parent.relname,''),pg_get_constraintdef(k.oid),
  ARRAY(SELECT a.attname FROM unnest(k.conkey) WITH ORDINALITY AS key(attnum,ord)
   JOIN pg_attribute a ON a.attrelid=k.conrelid AND a.attnum=key.attnum ORDER BY key.ord),
  ARRAY(SELECT a.attname FROM unnest(k.confkey) WITH ORDINALITY AS key(attnum,ord)
   JOIN pg_attribute a ON a.attrelid=k.confrelid AND a.attnum=key.attnum ORDER BY key.ord)
  FROM pg_constraint k JOIN pg_class child ON child.oid=k.conrelid JOIN pg_namespace n ON n.oid=child.relnamespace
  LEFT JOIN pg_class parent ON parent.oid=k.confrelid
  WHERE n.nspname='public' AND child.relname=ANY(?::text[]) AND k.contype IN ('p','f') ORDER BY child.relname,k.conname`, pq.Array(privateMemoryMigrationTables())).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var name, kind string
		var f privateMigrationForeignKey
		var columns, parents pq.StringArray
		if err = rows.Scan(&name, &f.Name, &kind, &f.Parent, &f.Definition, &columns, &parents); err != nil {
			return nil, err
		}
		f.Columns = columns
		f.ParentColumns = parents
		if kind == "p" {
			tables[name].Key = columns
		} else {
			tables[name].ForeignKeys = append(tables[name].ForeignKeys, f)
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	for _, t := range tables {
		if len(t.Key) == 0 {
			return nil, fmt.Errorf("%w: missing primary key for %s", privacy.ErrPrivateMemoryMigrationBlocked, t.Name)
		}
	}
	return tables, nil
}

func planPrivateMemoryMigration(ctx context.Context, tx *gorm.DB) (*privateMigrationPlan, error) {
	report := &privacy.PrivateMemoryMigrationReport{Status: "pending", PlannerVersion: privacy.PrivateMemoryPlannerVersion, Tables: []privacy.PrivateMemoryMigrationTable{}, Mappings: []privacy.PrivateMemoryWriterMapping{}, Blockers: []string{}}
	p := &privateMigrationPlan{Report: report, Spaces: map[string]*privateMigrationSpace{}, Memberships: map[string]string{}, Destinations: map[string]string{}}
	var exists bool
	if err := tx.Raw(`SELECT to_regclass('public.memory_spaces') IS NOT NULL`).Row().Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		digest := sha256.Sum256([]byte(report.PlannerVersion + "\nfresh\n"))
		report.SourceFingerprint = "sha256:" + hex.EncodeToString(digest[:])
		return p, nil
	}
	if _, err := validatePrivateMemoryManifestTx(ctx, tx); err != nil {
		return nil, err
	}
	tables, err := inspectPrivateMigrationSchema(tx)
	if err != nil {
		return nil, err
	}
	p.Tables = tables
	fingerprint := sha256.New()
	fmt.Fprintln(fingerprint, report.PlannerVersion)
	if err := hashPrivateMigrationSchema(tx, fingerprint, tables); err != nil {
		return nil, err
	}
	for _, name := range []string{"memory_spaces", "ownership_aliases", "team_memberships", "membership_grants", "actor_identities", "private_memory_legal_holds", "private_memory_erasure_operations"} {
		filter := ""
		if name == "memory_spaces" {
			filter = " WHERE kind <> 'private'"
		}
		if _, err := hashPrivateMemoryRows(tx, fingerprint, name, `SELECT md5((to_jsonb(row)-'owner_membership_id'-'migration_archive')::text) AS row_digest FROM `+pq.QuoteIdentifier(name)+` row`+filter+` ORDER BY row_digest`); err != nil {
			return nil, err
		}
	}
	if _, err := hashPrivateMemoryRows(tx, fingerprint, "credential_authority", `SELECT md5(jsonb_build_array(id,team_id,actor_identity_id,owner_identity_id,status,revoked_at,expires_at,scopes)::text) AS row_digest FROM credentials ORDER BY row_digest`); err != nil {
		return nil, err
	}
	if err := p.loadOwners(tx); err != nil {
		return nil, err
	}
	for _, name := range privateMemoryMigrationTables() {
		table := tables[name]
		from, spaceExpression, generationExpression := privateMigrationSource(table)
		query := `SELECT to_jsonb(row), ` + spaceExpression + `::text, ` + generationExpression + ` FROM ` + from + ` JOIN memory_spaces space ON space.id=` + spaceExpression + ` AND space.team_id=row.team_id WHERE space.kind <> 'team_shared' ORDER BY ` + migrationKeySQL("row", table.Key)
		rows, err := tx.WithContext(ctx).Raw(query).Rows()
		if err != nil {
			return nil, err
		}
		var count int64
		for rows.Next() {
			var raw []byte
			record := &privateMigrationRecord{Table: table, Destinations: map[string]bool{}}
			if err := rows.Scan(&raw, &record.SourceSpace, &record.Generation); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scan %s migration record: %w", name, err)
			}
			var full map[string]json.RawMessage
			if err := json.Unmarshal(raw, &full); err != nil {
				rows.Close()
				return nil, err
			}
			// Added placement columns on inherited histories are not changes to the inspected source payload.
			if name == "remember_attempt_events" || name == "remember_attempt_diagnostics" || name == "remember_failure_artifacts" || name == "semantic_assessments" {
				delete(full, "space_id")
				delete(full, "space_generation")
			}
			canonical, _ := json.Marshal(full)
			fmt.Fprintf(fingerprint, "%s\n%s\n", name, canonical)
			record.Key = migrationFieldKey(full, table.Key)
			record.Team = migrationString(full["team_id"])
			record.Fields = privateMigrationIdentityFields(table, full)
			record.Owner = migrationString(full["owner_profile_id"])
			if record.Owner == "" {
				record.Owner = migrationString(full["alias_owner_profile_id"])
			}
			if record.Owner == "" {
				record.Owner = migrationString(full["author_profile_id"])
			}
			if record.Owner != "" {
				record.Membership = p.Memberships[record.Team+":"+record.Owner]
			}
			p.Records = append(p.Records, record)
			count++
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		report.Tables = append(report.Tables, privacy.PrivateMemoryMigrationTable{Name: name, PrivateRows: count})
	}
	if err := p.resolveRoutes(); err != nil {
		return nil, err
	}
	if err := p.planConflictRestoration(tx); err != nil {
		return nil, err
	}
	sort.Strings(report.Blockers)
	report.Blockers = slices.Compact(report.Blockers)
	if len(report.Blockers) > 100 {
		report.Blockers = append(report.Blockers[:100], "additional_blockers_omitted")
	}
	report.SourceFingerprint = "sha256:" + hex.EncodeToString(fingerprint.Sum(nil))
	return p, nil
}

func (p *privateMigrationPlan) loadOwners(tx *gorm.DB) error {
	rows, err := tx.Raw(`SELECT alias.team_id::text,alias.legacy_owner_id::text,membership.id::text
  FROM ownership_aliases alias JOIN team_memberships membership ON membership.team_id=alias.team_id AND membership.actor_identity_id=alias.canonical_identity_id
  ORDER BY alias.team_id,alias.legacy_owner_id,membership.id`).Rows()
	if err != nil {
		return err
	}
	for rows.Next() {
		var team, owner, member string
		if err = rows.Scan(&team, &owner, &member); err != nil {
			rows.Close()
			return err
		}
		key := team + ":" + owner
		if previous, ok := p.Memberships[key]; ok && previous != member {
			p.Report.Blockers = append(p.Report.Blockers, "ambiguous_owner_alias:"+key)
		} else {
			p.Memberships[key] = member
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	rows, err = tx.Raw(`SELECT space.id::text,space.team_id::text,space.kind,space.generation,space.lifecycle_state,
  COALESCE((SELECT membership.id::text FROM team_memberships membership
   WHERE membership.team_id=space.team_id AND membership.actor_identity_id=COALESCE(
    (to_jsonb(space)->>'owner_profile_id')::uuid,
    (SELECT credential.actor_identity_id FROM credentials credential WHERE credential.id=(to_jsonb(space)->>'owner_credential_id')::uuid AND credential.team_id=space.team_id))),to_jsonb(space)->>'owner_membership_id',''),COALESCE((to_jsonb(space)->>'migration_archive')::boolean,false)
  FROM memory_spaces space WHERE kind <> 'team_shared' ORDER BY CASE kind WHEN 'credential_private' THEN 0 WHEN 'profile_private' THEN 1 ELSE 2 END,id`).Rows()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		s := &privateMigrationSpace{Writers: map[string]bool{}}
		if err := rows.Scan(&s.ID, &s.Team, &s.Kind, &s.Generation, &s.State, &s.OwnerMembership, &s.Archive); err != nil {
			return err
		}
		p.Spaces[s.ID] = s
		if s.OwnerMembership == "" {
			p.Report.Blockers = append(p.Report.Blockers, "space_owner_unresolved:"+s.ID)
			continue
		}
		if s.State != "retired" && !s.Archive {
			if _, ok := p.Destinations[s.OwnerMembership]; !ok {
				p.Destinations[s.OwnerMembership] = s.ID
			}
		}
	}
	return rows.Err()
}

func privateMigrationSource(table *privateMigrationTable) (string, string, string) {
	from := pq.QuoteIdentifier(table.Name) + ` AS row`
	switch table.Name {
	case "audit_log":
		return from, "row.memory_space_id", "space.generation"
	case "remember_attempt_events", "remember_attempt_diagnostics", "remember_failure_artifacts":
		return from + ` JOIN remember_attempts origin ON origin.team_id=row.team_id AND origin.attempt_id=row.attempt_id AND origin.owner_profile_id=row.owner_profile_id`, "origin.space_id", "origin.space_generation"
	case "semantic_assessments":
		return from + ` JOIN knowledge_ingests origin ON origin.team_id=row.team_id AND origin.ingest_id=row.attempt_id AND origin.owner_profile_id=row.owner_profile_id`, "origin.space_id", "origin.space_generation"
	default:
		return from, "row.space_id", "row.space_generation"
	}
}

func privateMigrationIdentityFields(table *privateMigrationTable, full map[string]json.RawMessage) map[string]json.RawMessage {
	fields := map[string]json.RawMessage{}
	for _, column := range table.Columns {
		if column.Type == "uuid" || column.Type == "uuid[]" || slices.Contains([]string{"source_kind", "name_kind", "valid_to", "created_at", "action", "processing_state", "version"}, column.Name) || slices.Contains(table.Key, column.Name) {
			if value, exists := full[column.Name]; exists {
				fields[column.Name] = value
			}
		}
	}
	for _, fk := range table.ForeignKeys {
		for _, name := range fk.Columns {
			if value, exists := full[name]; exists {
				fields[name] = value
			}
		}
	}
	for _, column := range privateMigrationJSONReferences[table.Name] {
		var value any
		if json.Unmarshal(full[column], &value) == nil {
			fields["_refs_"+column], _ = json.Marshal(privateMigrationReferenceIDs(value))
		}
	}
	return fields
}

func migrationString(value json.RawMessage) string {
	var out string
	_ = json.Unmarshal(value, &out)
	return out
}
func migrationFieldKey(fields map[string]json.RawMessage, columns []string) string {
	values := make([]json.RawMessage, len(columns))
	for i, column := range columns {
		values[i] = fields[column]
		if len(values[i]) == 0 {
			values[i] = json.RawMessage(`null`)
		}
	}
	raw, _ := json.Marshal(values)
	return string(raw)
}
func migrationKeySQL(alias string, columns []string) string {
	parts := make([]string, len(columns))
	for i, c := range columns {
		parts[i] = alias + "." + pq.QuoteIdentifier(c)
	}
	return "jsonb_build_array(" + strings.Join(parts, ",") + ")"
}
func migrationID(namespace, identity string) string {
	return uuid.NewSHA1(uuid.MustParse(namespace), []byte("private-memory.v2.7.0\n"+identity)).String()
}

func (p *privateMigrationPlan) resolveRoutes() error {
	indexes := map[string]map[string]*privateMigrationRecord{}
	for _, t := range p.Tables {
		for _, fk := range t.ForeignKeys {
			if p.Tables[fk.Parent] == nil {
				continue
			}
			signature := fk.Parent + ":" + strings.Join(fk.ParentColumns, ",")
			if indexes[signature] != nil {
				continue
			}
			index := map[string]*privateMigrationRecord{}
			indexes[signature] = index
			for _, r := range p.Records {
				if r.Table.Name == fk.Parent {
					index[migrationFieldKey(r.Fields, fk.ParentColumns)] = r
				}
			}
		}
	}
	for _, r := range p.Records {
		space := p.Spaces[r.SourceSpace]
		if space == nil {
			return fmt.Errorf("migration source space missing")
		}
		if r.Membership != "" && !privateMigrationIsSealed(r.Table.Name) {
			space.Writers[r.Membership] = true
		}
		if !privateMigrationIsSealed(r.Table.Name) && !privateMigrationLinkedTables[r.Table.Name] && r.Membership == "" {
			p.Report.Blockers = append(p.Report.Blockers, "writer_attribution_missing:"+r.Table.Name+":"+r.Key)
		}
		for _, fk := range r.Table.ForeignKeys {
			index := indexes[fk.Parent+":"+strings.Join(fk.ParentColumns, ",")]
			if index == nil {
				continue
			}
			key := migrationFieldKey(r.Fields, fk.Columns)
			if strings.Contains(key, "null") {
				continue
			}
			parent := index[key]
			if parent == nil {
				if !privateMigrationIsSealed(r.Table.Name) {
					p.Report.Blockers = append(p.Report.Blockers, "support_outside_legacy_private_space:"+r.Table.Name+":"+fk.Name)
				}
				continue
			}
			if parent.SourceSpace != r.SourceSpace {
				p.Report.Blockers = append(p.Report.Blockers, "cross_private_space_reference:"+r.Table.Name+":"+fk.Name)
				continue
			}
			r.References = append(r.References, parent)
		}
	}
	p.linkPrivateMigrationSupport()
	p.attributePrivateMigrationNodes()
	if err := p.linkPrivateMigrationProjections(); err != nil {
		return err
	}
	for _, r := range p.Records {
		source := p.Spaces[r.SourceSpace]
		r.Sealed = source.State != "active" || (len(source.Writers) > 1 && (privateMigrationIsSealed(r.Table.Name) ||
			(r.Table.Name == "relationship_correction_submissions" && migrationString(r.Fields["processing_state"]) != "completed")))
		if r.Sealed {
			continue
		}
		if r.Membership != "" {
			r.Destinations[r.Membership] = true
		} else if len(source.Writers) <= 1 && !privateMigrationLinkedTables[r.Table.Name] {
			member := source.OwnerMembership
			for writer := range source.Writers {
				member = writer
			}
			r.Destinations[member] = true
		}
	}
	changed := true
	for changed {
		changed = false
		for _, r := range p.Records {
			if r.Sealed {
				continue
			}
			for member := range r.Destinations {
				for _, parent := range r.References {
					if parent.Sealed || parent.Destinations[member] {
						continue
					}
					parent.Destinations[member] = true
					changed = true
				}
			}
		}
	}
	for _, r := range p.Records {
		if len(r.Destinations) == 0 {
			r.Sealed = true
		}
		for member := range r.Destinations {
			if p.Destinations[member] == "" {
				p.Destinations[member] = migrationID(r.Team, "membership:"+member)
			}
		}

	}
	for _, spaceID := range sortedMigrationKeys(p.Spaces) {
		s := p.Spaces[spaceID]
		for _, member := range sortedMigrationKeys(s.Writers) {
			p.Report.MappingCount++
			if len(p.Report.Mappings) < privateMemoryPreflightMappingLimit {
				owner := ""
				for _, r := range p.Records {
					if r.SourceSpace == s.ID && r.Membership == member {
						owner = r.Owner
						break
					}
				}
				p.Report.Mappings = append(p.Report.Mappings, privacy.PrivateMemoryWriterMapping{TeamID: s.Team, SourceSpaceID: s.ID, SourceGeneration: s.Generation, OwnerID: owner, OwnerMembershipID: member})
			}
		}
	}
	for _, move := range p.archiveParentMoves(p.moves()) {
		if move.Copy {
			p.Report.SupportCopyCount++
		}
		if !move.Sealed && move.SourceKey != move.DestinationKey && slices.Contains([]string{"evidence_fragments", "relationship_records", "entity_records", "value_records"}, move.Table) {
			p.Report.HandleMappingCount++
		}
	}
	p.Report.MappingsTruncated = p.Report.MappingCount > int64(len(p.Report.Mappings))
	sort.Strings(p.Report.Blockers)
	p.Report.Blockers = slices.Compact(p.Report.Blockers)
	if len(p.Report.Blockers) > 100 {
		p.Report.Blockers = append(p.Report.Blockers[:100], "additional_blockers_omitted")
	}
	return nil
}

func privateMemoryMigrationTables() []string {
	tables := append(PrivateMemoryErasureManifest(), "audit_log")
	sort.Strings(tables)
	return tables
}

func sortedMigrationKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (p *privateMigrationPlan) linkPrivateMigrationProjections() error {
	byID := map[string]*privateMigrationRecord{}
	for _, r := range p.Records {
		for _, column := range r.Table.Key {
			if column != "team_id" && column != "space_id" {
				if id := migrationString(r.Fields[column]); id != "" {
					byID[r.Team+":"+r.Table.Name+":"+id] = r
				}
			}
		}
	}
	for _, r := range p.Records {
		var parent *privateMigrationRecord
		switch r.Table.Name {
		case "entity_names":
			if migrationString(r.Fields["name_kind"]) != "canonical" || string(r.Fields["valid_to"]) != "null" {
				continue
			}
			parent = byID[r.Team+":entity_records:"+migrationString(r.Fields["entity_id"])]
		case "search_documents":
			table := ""
			switch migrationString(r.Fields["source_kind"]) {
			case "evidence":
				table = "evidence_fragments"
			case "relationship":
				table = "relationship_records"
			default:
				continue
			}
			parent = byID[r.Team+":"+table+":"+migrationString(r.Fields["source_id"])]
			if parent != nil {
				r.References = append(r.References, parent)
			}
		default:
			continue
		}
		if parent == nil || parent.SourceSpace != r.SourceSpace {
			return fmt.Errorf("%w: projection source outside its legacy private space", privacy.ErrPrivateMemoryMigrationBlocked)
		}
		parent.References = append(parent.References, r)
	}
	return nil
}

func (p *privateMigrationPlan) attributePrivateMigrationNodes() {
	type nodeKey struct{ space, kind, id, createdAt string }
	type nodeWriter struct {
		membership string
		ambiguous  bool
	}
	writers := map[nodeKey]nodeWriter{}
	for _, candidate := range p.Records {
		if candidate.Membership == "" || len(p.Spaces[candidate.SourceSpace].Writers) <= 1 {
			continue
		}
		var kind, id string
		switch candidate.Table.Name {
		case "entity_resolution_events":
			if migrationString(candidate.Fields["action"]) != "create" {
				continue
			}
			kind, id = "entity_records", string(candidate.Fields["entity_id"])
		case "relationship_observations":
			kind, id = "value_records", string(candidate.Fields["object_value_id"])
		default:
			continue
		}
		key := nodeKey{candidate.SourceSpace, kind, id, string(candidate.Fields["created_at"])}
		writer := writers[key]
		if writer.membership != "" && writer.membership != candidate.Membership {
			writer.ambiguous = true
		}
		writer.membership = candidate.Membership
		writers[key] = writer
	}
	for _, record := range p.Records {
		if record.Table.Name != "entity_records" && record.Table.Name != "value_records" {
			continue
		}
		source := p.Spaces[record.SourceSpace]
		if len(source.Writers) <= 1 {
			for member := range source.Writers {
				record.Membership = member
			}
			continue
		}
		id := record.Fields["entity_id"]
		if record.Table.Name == "value_records" {
			id = record.Fields["value_id"]
		}
		writer := writers[nodeKey{record.SourceSpace, record.Table.Name, string(id), string(record.Fields["created_at"])}]
		if writer.membership == "" || writer.ambiguous {
			p.Report.Blockers = append(p.Report.Blockers, "ambiguous_node_writer:"+record.Table.Name+":"+record.Key)
			continue
		}
		record.Membership = writer.membership
	}
}

// Current lifecycle and support histories travel with the records whose eligibility they define.
func (p *privateMigrationPlan) linkPrivateMigrationSupport() {
	reverse := map[string]string{
		"relationship_evidence_supports":       "relationship_records",
		"relationship_support_decision_events": "relationship_evidence_supports",
		"relationship_transition_events":       "relationship_records",
		"evidence_lifecycle_events":            "evidence_fragments",
		"evidence_quarantines":                 "evidence_fragments",
		"evidence_security_events":             "evidence_fragments",
		"evidence_security_signals":            "evidence_fragments",
	}
	ids := map[string]*privateMigrationRecord{}
	for _, record := range p.Records {
		for _, col := range record.Table.Key {
			if col != "team_id" && col != "space_id" {
				id := migrationString(record.Fields[col])
				if _, err := uuid.Parse(id); err == nil {
					ids[record.Team+":"+id] = record
				}
			}
		}
	}
	for _, record := range p.Records {
		if table := reverse[record.Table.Name]; table != "" {
			for _, parent := range record.References {
				if parent.Table.Name == table {
					parent.References = append(parent.References, record)
				}
			}
		}
		for key, raw := range record.Fields {
			if !strings.HasPrefix(key, "_refs_") {
				continue
			}
			var refs []string
			if json.Unmarshal(raw, &refs) != nil {
				continue
			}
			for _, id := range refs {
				parent := ids[record.Team+":"+id]
				if parent == nil || parent.SourceSpace != record.SourceSpace {
					if !privateMigrationIsSealed(record.Table.Name) {
						p.Report.Blockers = append(p.Report.Blockers, "logical_reference_outside_legacy_private_space:"+record.Table.Name+":"+key)
					}
					continue
				}
				record.References = append(record.References, parent)
			}
		}
	}
}
