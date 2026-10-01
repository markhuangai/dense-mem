//go:build integration

package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/requestctx"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

type traceOwnershipFixture struct {
	teamID, ownerID, spaceID string
	store                    *Store
	db                       *gorm.DB
	rls                      *storagepostgres.RLS
	ctx                      context.Context
	content                  string
	identities               *strings.Replacer
}

func traceOwnershipID(kind, index int) string {
	return fmt.Sprintf("00000000-0000-4000-8000-%012d", kind*10000+index)
}

func newTraceOwnershipFixture(t testing.TB) *traceOwnershipFixture {
	t.Helper()
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	t.Cleanup(cleanup)
	f := &traceOwnershipFixture{
		teamID: createLedgerTeam(t, adminDB, rls, "trace-ownership"),
		db:     appDB, rls: rls, content: strings.Repeat("界🙂", 4000) + "尾",
	}
	f.ownerID = createLedgerProfile(t, adminDB, rls, f.teamID, "trace-owner")
	f.ctx = requestctx.WithActor(context.Background(), requestctx.Actor{
		TeamID: uuid.MustParse(f.teamID), OwnerID: uuid.MustParse(f.ownerID), Grants: []string{"read"},
	})
	clock := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	contentHash := sha256.Sum256([]byte(f.content))
	require.NoError(t, rls.WithSystemTx(context.Background(), adminDB, func(tx *gorm.DB) error {
		if err := tx.Raw(`SELECT dense_mem_team_shared_space(?::uuid)::text`, f.teamID).Row().Scan(&f.spaceID); err != nil {
			return err
		}
		for _, query := range []struct {
			sql  string
			args []any
		}{
			{`INSERT INTO team_predicate_definitions (
				team_id, predicate_key, version, aliases, allowed_subject_kinds,
				allowed_object_kinds, relationship_kind, current_cardinality,
				lifecycle_state, origin, metadata, created_at)
				SELECT ?::uuid, predicate_key, version, aliases, allowed_subject_kinds,
				allowed_object_kinds, relationship_kind, current_cardinality,
				lifecycle_state, 'built_in', metadata, ?
				FROM predicate_definitions WHERE predicate_key = 'uses' AND version = 1`,
				[]any{f.teamID, clock}},
			{`INSERT INTO entity_records (team_id, entity_id, entity_kind, space_id, created_at, updated_at)
				SELECT ?::uuid, ('00000000-0000-4000-8000-' || lpad((10000+n)::text, 12, '0'))::uuid,
				'concept', ?::uuid, ?, ? FROM generate_series(0, 101) AS n`,
				[]any{f.teamID, f.spaceID, clock, clock}},
			{`INSERT INTO relationship_records (
				team_id, relationship_id, owner_profile_id, semantic_group_key,
				subject_entity_id, predicate_key, predicate_version, object_entity_id,
				relationship_kind, current_cardinality, status, polarity,
				support_count, source_group_count, space_id, created_at, updated_at)
				SELECT ?::uuid, ('00000000-0000-4000-8000-' || lpad((20000+n)::text, 12, '0'))::uuid,
				?::uuid, 'trace-ownership-' || n, ?::uuid, 'uses', 1,
				('00000000-0000-4000-8000-' || lpad((10000+n)::text, 12, '0'))::uuid,
				'state', 'many', 'active', '+', 1, 1, ?::uuid, ?, ?
				FROM generate_series(1, 101) AS n`,
				[]any{f.teamID, f.ownerID, traceOwnershipID(1, 0), f.spaceID, clock, clock}},
			{`INSERT INTO knowledge_ingests (
				team_id, ingest_id, owner_profile_id, idempotency_key, request_hash,
				status, completed_at, space_id, created_at, updated_at)
				VALUES (?::uuid, ?::uuid, ?::uuid, 'trace-ownership', 'trace-ownership',
				'completed', ?, ?::uuid, ?, ?)`,
				[]any{f.teamID, traceOwnershipID(3, 0), f.ownerID, clock, f.spaceID, clock, clock}},
			{`INSERT INTO evidence_fragments (
				team_id, fragment_id, ingest_id, owner_profile_id, evidence_index,
				content, content_hash, space_id, created_at)
				VALUES (?::uuid, ?::uuid, ?::uuid, ?::uuid, 0, ?, ?, ?::uuid, ?)`,
				[]any{f.teamID, traceOwnershipID(4, 0), traceOwnershipID(3, 0), f.ownerID,
					f.content, "sha256:" + hex.EncodeToString(contentHash[:]), f.spaceID, clock}},
			{`INSERT INTO relationship_observations (
				team_id, observation_id, relationship_id, ingest_id, owner_profile_id,
				subject_ref, original_predicate, object_ref, subject_entity_id,
				predicate_key, predicate_version, object_entity_id, space_id, created_at)
				VALUES (?::uuid, ?::uuid, ?::uuid, ?::uuid, ?::uuid, 'Subject', 'uses', 'Object',
				?::uuid, 'uses', 1, ?::uuid, ?::uuid, ?)`,
				[]any{f.teamID, traceOwnershipID(5, 0), traceOwnershipID(2, 1), traceOwnershipID(3, 0),
					f.ownerID, traceOwnershipID(1, 0), traceOwnershipID(1, 1), f.spaceID, clock}},
			{`INSERT INTO verification_events (
				team_id, verification_event_id, observation_id, owner_profile_id,
				evidence_verdict, rationale, space_id, created_at)
				SELECT ?::uuid, ('00000000-0000-4000-8000-' || lpad((60000+n)::text, 12, '0'))::uuid,
				?::uuid, ?::uuid, CASE n % 4 WHEN 0 THEN 'entailed' WHEN 1 THEN 'contradicted'
				WHEN 2 THEN 'insufficient' ELSE NULL END, 'trace ownership', ?::uuid,
				?::timestamptz + n * interval '1 microsecond' FROM generate_series(0, 500) AS n`,
				[]any{f.teamID, traceOwnershipID(5, 0), f.ownerID, f.spaceID, clock}},
			{`INSERT INTO relationship_evidence_supports (
				team_id, support_id, relationship_id, observation_id, verification_event_id,
				fragment_id, owner_profile_id, source_group_key, span_start, span_end,
				quote, authority, space_id, created_at)
				VALUES (?::uuid, ?::uuid, ?::uuid, ?::uuid, ?::uuid, ?::uuid, ?::uuid,
				'trace-ownership', 0, 1, '界', 'primary', ?::uuid, ?)`,
				[]any{f.teamID, traceOwnershipID(7, 0), traceOwnershipID(2, 1), traceOwnershipID(5, 0),
					traceOwnershipID(6, 0), traceOwnershipID(4, 0), f.ownerID, f.spaceID, clock}},
			{`INSERT INTO relationship_support_decision_events (
				team_id, support_decision_id, support_id, relationship_id, owner_profile_id,
				actor_profile_id, decision, reason, space_id, created_at)
				VALUES (?::uuid, ?::uuid, ?::uuid, ?::uuid, ?::uuid, ?::uuid,
				'grant', 'trace ownership', ?::uuid, ?)`,
				[]any{f.teamID, traceOwnershipID(8, 0), traceOwnershipID(7, 0), traceOwnershipID(2, 1),
					f.ownerID, f.ownerID, f.spaceID, clock}},
			{`INSERT INTO relationship_transition_events (
				team_id, transition_id, relationship_id, owner_profile_id, from_status,
				to_status, reason, support_decision_id, space_id, created_at)
				VALUES (?::uuid, ?::uuid, ?::uuid, ?::uuid, 'pending_evidence',
				'active', 'trace ownership', ?::uuid, ?::uuid, ?)`,
				[]any{f.teamID, traceOwnershipID(9, 0), traceOwnershipID(2, 1), f.ownerID,
					traceOwnershipID(8, 0), f.spaceID, clock}},
		} {
			if err := tx.Exec(query.sql, query.args...).Error; err != nil {
				return err
			}
		}
		return nil
	}))
	f.store = newTraceStoreForTest(appDB, rls)
	f.identities = strings.NewReplacer(f.teamID, "<team>", f.ownerID, "<owner>", f.spaceID, "<space>")
	return f
}

func (f *traceOwnershipFixture) signature(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	// Only independently generated actor/space identities vary between disposable databases.
	sum := sha256.Sum256([]byte(f.identities.Replace(string(encoded))))
	return hex.EncodeToString(sum[:]), nil
}
