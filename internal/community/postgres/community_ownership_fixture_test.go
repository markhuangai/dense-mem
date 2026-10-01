//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type communityOwnershipFixture struct {
	store                                       *Store
	adminDB, appDB                              *gorm.DB
	rls                                         *storagepostgres.RLS
	counters                                    *communityOwnershipCounters
	teamID, ownerA, ownerB, otherTeam, expandID string
	run                                         *CommunityRun
	publication                                 CommunitySnapshotPublishInput
	sources                                     []CommunitySourceInput
	evidenceIDs                                 []string
	labels                                      map[string]string
}

func communityOwnershipID(index int) string {
	return fmt.Sprintf("50000000-0000-4000-8000-%012d", index+1)
}

func newCommunityOwnershipFixture(t testing.TB) *communityOwnershipFixture {
	t.Helper()
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	t.Cleanup(cleanup)
	ctx := context.Background()
	f := &communityOwnershipFixture{adminDB: adminDB, appDB: appDB, rls: rls, counters: &communityOwnershipCounters{}, labels: map[string]string{}}
	f.teamID = createLedgerTeam(t, adminDB, rls, "community-ownership-a")
	f.ownerA = createLedgerProfile(t, adminDB, rls, f.teamID, "community-owner-a")
	f.ownerB = createLedgerProfile(t, adminDB, rls, f.teamID, "community-owner-b")
	f.otherTeam = createLedgerTeam(t, adminDB, rls, "community-ownership-c")
	createLedgerProfile(t, adminDB, rls, f.otherTeam, "community-owner-c")
	f.store = NewStore(communityOwnershipCountedDB(appDB, f.counters), rls)
	semantic := knowledgepostgres.NewStore(appDB, rls, knowledgepostgres.ConflictRuntimeConfig{})
	subject := createSemanticEntity(t, ctx, semantic, f.teamID, f.ownerA, "project", "Community subject")
	expansion := createSemanticEntity(t, ctx, semantic, f.teamID, f.ownerA, "product", "Expansion entity")
	f.expandID = expansion.EntityID
	f.labels[f.teamID] = "team-a"
	f.labels[subject.EntityID] = "subject"
	f.labels[expansion.EntityID] = "expansion"
	members := []CommunityMembershipInput{}
	for i := range 26 {
		owner := f.ownerA
		if i%2 == 1 {
			owner = f.ownerB
		}
		object := createSemanticEntity(t, ctx, semantic, f.teamID, owner, "product", fmt.Sprintf("Community object %02d", i))
		content := fmt.Sprintf("Community subject uses Community object %02d.", i)
		ingest := createSemanticIngest(t, ctx, semantic, f.teamID, owner, fmt.Sprintf("community-source-%02d", i), content)
		decision := applySemanticDecision(t, ctx, semantic, ApplyRelationshipDecisionInput{
			TeamID: f.teamID, OwnerProfileID: owner, IngestID: ingest.IngestID,
			SubjectEntityID: subject.EntityID, PredicateKey: "uses", ObjectEntityID: object.EntityID,
			Support: &EvidenceSupportInput{FragmentID: ingest.Evidence[0].FragmentID,
				SourceGroupKey: fmt.Sprintf("community-source-%02d", i), SpanEnd: len(content), Authority: "primary"},
		})
		relationship := decision.Relationship
		require.Equal(t, "active", relationship.Status)
		f.sources = append(f.sources, CommunitySourceInput{RelationshipID: relationship.RelationshipID,
			OwnerProfileID: owner, RelationshipVersion: relationship.Version, SourceRank: i,
			SemanticGroupKey: relationship.SemanticGroupKey})
		f.evidenceIDs = append(f.evidenceIDs, ingest.Evidence[0].FragmentID)
		f.labels[relationship.RelationshipID] = fmt.Sprintf("relationship-%02d", i)
		f.labels[relationship.SemanticGroupKey] = fmt.Sprintf("group-%02d", i)
		f.labels[ingest.Evidence[0].FragmentID] = fmt.Sprintf("evidence-%02d", i)
		f.labels[object.EntityID] = fmt.Sprintf("object-%02d", i)
		if i < 7 {
			members = append(members, CommunityMembershipInput{EntityID: object.EntityID, Rank: i, MembershipScore: 1, SourceCount: 1})
		}
	}
	run, err := f.store.ClaimCommunityRun(ctx, CommunityRunClaimInput{
		TeamID: f.teamID, WindowKey: "community-ownership", LeaseUntil: time.Now().UTC().Add(time.Hour),
		AlgorithmKind: "louvain", AlgorithmVersion: "v2", ProfileVersion: "postgres-v2",
		ConfigurationHash: "fixture-config", SourceFingerprint: "fixture-sources", MaxNodes: 5000, MaxEdges: 20000,
	})
	require.NoError(t, err)
	f.run = run
	f.publication = CommunitySnapshotPublishInput{
		TeamID: f.teamID, RunID: run.RunID, AlgorithmKind: "louvain", AlgorithmVersion: "v2", ProfileVersion: "postgres-v2",
		ConfigurationHash: "fixture-config", SourceFingerprint: "fixture-sources", NodeCount: 26, EdgeCount: 26,
	}
	counts := []int{1, 2, 2, 3, 4, 10, 10, 50, 9, 8, 7, 6}
	for i, count := range counts {
		sources := append([]CommunitySourceInput(nil), f.sources[4:]...)
		if i < 4 {
			sources = append([]CommunitySourceInput{f.sources[i]}, sources...)
		}
		memberships := append([]CommunityMembershipInput(nil), members...)
		if i == 4 {
			memberships = append(memberships, CommunityMembershipInput{EntityID: f.expandID, Rank: 8, MembershipScore: 1, SourceCount: 1})
		}
		f.publication.Communities = append(f.publication.Communities, CommunityPublishRecord{
			CommunityID: communityOwnershipID(i), Ordinal: i, Summary: "Community snapshot marker", SummaryVersion: "fixture-summary",
			MemberCount: count, SourceCount: len(sources), TopEntities: []string{}, TopPredicates: []string{"uses"}, SourceFingerprint: "fixture-sources",
			Memberships: memberships, Sources: sources,
		})
	}
	require.NoError(t, f.store.PublishCommunitySnapshot(ctx, f.publication))
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE relationship_records SET created_at = '2026-01-01T00:00:00Z' WHERE team_id = ?::uuid`, f.teamID).Error
	}))
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`ANALYZE community_records, community_sources, community_memberships,
   relationship_records, relationship_evidence_supports, relationship_support_decision_events,
   entity_names, value_records, evidence_quarantines, evidence_sources,
   evidence_lifecycle_events, memory_spaces`).Error
	}))
	return f
}

func (f *communityOwnershipFixture) mixedInput() CommunityRecallInput {
	return CommunityRecallInput{TeamID: f.teamID, Query: "marker", Limit: 10,
		ReturnedEvidenceIDs: []string{f.evidenceIDs[0]}, KnownEvidenceIDs: []string{f.evidenceIDs[1]},
		KnownRelationshipIDs: []string{f.sources[2].RelationshipID}, SeedRelationshipIDs: []string{f.sources[3].RelationshipID},
		ExpandFromEntityIDs: []string{f.expandID},
	}
}

func communityOwnershipIDs(records []CommunityRecallRecord) []string {
	out := make([]string, 0, len(records))
	for _, record := range records {
		out = append(out, record.CommunityID)
	}
	return out
}

func (f *communityOwnershipFixture) signature(records []CommunityRecallRecord) string {
	encoded, err := json.Marshal(records)
	if err != nil {
		panic(err)
	}
	var result any
	if err := json.Unmarshal(encoded, &result); err != nil {
		panic(err)
	}
	var normalize func(any) any
	normalize = func(value any) any {
		switch v := value.(type) {
		case string:
			if label, ok := f.labels[v]; ok {
				return label
			}
			return v
		case []any:
			for i := range v {
				v[i] = normalize(v[i])
			}
			return v
		case map[string]any:
			for key := range v {
				v[key] = normalize(v[key])
			}
			return v
		}
		return value
	}
	canonical, err := json.Marshal(normalize(result))
	if err != nil {
		panic(err)
	}
	return string(canonical)
}
