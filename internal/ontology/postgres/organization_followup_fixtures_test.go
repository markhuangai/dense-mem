package postgres

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func (f *ontologyFixture) organizationEvidenceAt(t *testing.T, owner int, text, created, timezone string) ontology.SourceHandle {
	t.Helper()
	metadata := map[string]any{}
	if timezone != "" {
		metadata["timezone"] = timezone
	}
	seed := f.organizationEvidence(t, owner, text, metadata)
	ingestID, fragmentID := uuid.NewString(), uuid.NewString()
	require.NoError(t, f.rls.WithSystemTx(context.Background(), f.admin, func(tx *gorm.DB) error {
		if err := tx.Exec(`INSERT INTO knowledge_ingests
			(ingest_id,team_id,owner_profile_id,space_id,space_generation,request_hash,source_summary,status,proposal,metadata,created_at,updated_at,completed_at)
			SELECT ?::uuid,team_id,owner_profile_id,space_id,space_generation,request_hash,source_summary,status,proposal,metadata,?::timestamptz,?::timestamptz,?::timestamptz
			FROM knowledge_ingests WHERE team_id=?::uuid AND ingest_id=(SELECT ingest_id FROM evidence_fragments WHERE team_id=?::uuid AND fragment_id=?::uuid)`, ingestID, created, created, created, f.team, f.team, seed.ID).Error; err != nil {
			return err
		}
		return tx.Exec(`INSERT INTO evidence_fragments
			(fragment_id,team_id,ingest_id,owner_profile_id,space_id,space_generation,evidence_index,content,content_hash,source_type,authority,source_ref,labels,metadata,force_insert,created_at)
			SELECT ?::uuid,team_id,?::uuid,owner_profile_id,space_id,space_generation,0,content,content_hash,source_type,authority,source_ref,labels,metadata,true,?::timestamptz
			FROM evidence_fragments WHERE team_id=?::uuid AND fragment_id=?::uuid`, fragmentID, ingestID, created, f.team, seed.ID).Error
	}))
	_, err := f.knowledge.RetractEvidence(f.actor(owner, "member"), knowledge.RetractEvidenceInput{TeamID: f.team, OwnerProfileID: f.owners[owner], EvidenceIDs: []string{seed.ID}, Reason: "replace timestamp fixture seed", IdempotencyKey: uuid.NewString(), RequestHash: testHash("replace timestamp fixture")})
	require.NoError(t, err)
	_, err = f.store.ReadSources(context.Background(), f.team, []ontology.SourceHandle{seed})
	require.ErrorIs(t, err, ontology.ErrSourceStale)
	return ontology.SourceHandle{Kind: ontology.EvidenceSource, ID: fragmentID, Version: 1}
}

func seedStaleVocabulary(t *testing.T, f *ontologyFixture, staleCount, currentCount int) []ontology.Record {
	t.Helper()
	ctx := context.Background()
	support := f.organizationEvidence(t, 0, "Historical vocabulary support.", nil)
	dependency := f.source(t, support)
	page, err := f.store.ListRecords(ctx, f.team, "", "", 1)
	require.NoError(t, err)
	revision := page.Revision
	var changes []ontology.Change
	var current []ontology.Record
	publish := func() {
		result, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication(uuid.NewString(), revision, changes...))
		require.NoError(t, err)
		revision = result.Revision
		changes = nil
	}
	for index := 0; index < staleCount+currentCount; index++ {
		record := testTopic(fmt.Sprintf("obsolete-%04d", index))
		record.ID = fmt.Sprintf("00000000-0000-4000-8000-%012d", index+1)
		record.Definition.Description = strings.Repeat("PostgreSQL relational storage ", 8)
		record.Sources = []ontology.SourceDependency{dependency}
		if index >= staleCount {
			n := index - staleCount
			record.ID = fmt.Sprintf("ffffffff-ffff-4fff-8fff-%012d", n+1)
			record.Definition.Key = fmt.Sprintf("relational-store-%04d", n)
			record.Definition.Label = fmt.Sprintf("Current concept %04d", n)
			record.Definition.Description = "PostgreSQL relational storage"
			record.Sources = nil
			current = append(current, record)
		}
		changes = append(changes, ontology.Change{Record: record})
		if len(changes) == ontology.MaxChanges {
			publish()
		}
	}
	if len(changes) > 0 {
		publish()
	}
	_, err = f.knowledge.RetractEvidence(f.actor(0, "member"), knowledge.RetractEvidenceInput{TeamID: f.team, OwnerProfileID: f.owners[0], EvidenceIDs: []string{support.ID}, Reason: "withdraw historical vocabulary support", IdempotencyKey: uuid.NewString(), RequestHash: testHash("withdraw vocabulary")})
	require.NoError(t, err)
	return current
}
