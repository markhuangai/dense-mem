package repository

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestSemanticTraceRelationshipHydratesLineageAndBoundedGraph(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamA := createLedgerTeam(t, adminDB, rls, "trace-team-a")
	ownerA := createLedgerProfile(t, adminDB, rls, teamA, "trace-owner-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamA, "trace-owner-b")
	teamB := createLedgerTeam(t, adminDB, rls, "trace-team-b")
	createLedgerProfile(t, adminDB, rls, teamB, "trace-owner-other")
	ledgerRepo := NewLedgerRepository(appDB, rls)
	semanticRepo := NewSemanticRepository(appDB, rls)
	searchRepo := NewSearchRepository(appDB, rls)

	mark := createSemanticEntity(t, ctx, semanticRepo, teamA, ownerA, "person", "Mark Huang")
	denseMem := createSemanticEntity(t, ctx, semanticRepo, teamA, ownerA, "project", "Dense-Mem")
	postgres := createSemanticEntity(t, ctx, semanticRepo, teamA, ownerA, "product", "PostgreSQL")
	releaseDate, err := semanticRepo.UpsertValue(ctx, UpsertValueInput{
		TeamID:         teamA,
		OwnerProfileID: ownerA,
		ValueType:      "date",
		CanonicalValue: "2026-07-17",
		Display:        "July 17, 2026",
	})
	require.NoError(t, err)

	firstIngest := createSemanticIngest(t, ctx, ledgerRepo, teamA, ownerA,
		"trace mark works on dense mem", "Mark Huang works on Dense-Mem.")
	first := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:          teamA,
		OwnerProfileID:  ownerA,
		IngestID:        firstIngest.IngestID,
		SubjectEntityID: mark.EntityID,
		PredicateKey:    "works_on",
		ObjectEntityID:  denseMem.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID:     firstIngest.Evidence[0].FragmentID,
			SourceGroupKey: "conversation:trace:first",
			SpanStart:      0,
			SpanEnd:        len("Mark Huang works on Dense-Mem."),
			Quote:          "Mark Huang works on Dense-Mem.",
			Authority:      "primary",
		},
	})
	require.NotNil(t, first.Relationship)

	secondIngest := createSemanticIngest(t, ctx, ledgerRepo, teamA, ownerA,
		"trace dense mem release", "Dense-Mem released on July 17, 2026.")
	released := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:          teamA,
		OwnerProfileID:  ownerA,
		IngestID:        secondIngest.IngestID,
		SubjectEntityID: denseMem.EntityID,
		PredicateKey:    "released",
		ObjectValueID:   releaseDate.ValueID,
		PromoteToFact:   true,
		Support: &EvidenceSupportInput{
			FragmentID:     secondIngest.Evidence[0].FragmentID,
			SourceGroupKey: "conversation:trace:release",
			SpanStart:      0,
			SpanEnd:        len("Dense-Mem released on July 17, 2026."),
			Quote:          "Dense-Mem released on July 17, 2026.",
			Authority:      "primary",
		},
	})
	require.NotNil(t, released.Relationship)

	candidateIngest := createSemanticIngest(t, ctx, ledgerRepo, teamA, ownerA,
		"trace candidate uses postgres", "Dense-Mem might use PostgreSQL.")
	candidate := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:          teamA,
		OwnerProfileID:  ownerA,
		IngestID:        candidateIngest.IngestID,
		SubjectEntityID: denseMem.EntityID,
		PredicateKey:    "uses",
		ObjectEntityID:  postgres.EntityID,
		EvidenceVerdict: "insufficient",
	})
	require.NotNil(t, candidate.Relationship)

	ownerBIngest := createSemanticIngest(t, ctx, ledgerRepo, teamA, ownerB,
		"trace owner b challenge", "Profile B challenges Mark Huang works on Dense-Mem.")
	ownerBRelationship := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:          teamA,
		OwnerProfileID:  ownerB,
		IngestID:        ownerBIngest.IngestID,
		SubjectEntityID: mark.EntityID,
		PredicateKey:    "works_on",
		ObjectEntityID:  denseMem.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID:     ownerBIngest.Evidence[0].FragmentID,
			SourceGroupKey: "conversation:trace:owner-b",
			SpanStart:      0,
			SpanEnd:        len("Profile B challenges Mark Huang works on Dense-Mem."),
			Quote:          "Profile B challenges Mark Huang works on Dense-Mem.",
			Authority:      "primary",
		},
	})
	_, err = semanticRepo.AppendCrossReference(ctx, AppendCrossReferenceInput{
		TeamID:                    teamA,
		AuthorProfileID:           ownerB,
		SourceRelationshipID:      ownerBRelationship.Relationship.RelationshipID,
		SourceRelationshipVersion: ownerBRelationship.Relationship.Version,
		TargetRelationshipID:      first.Relationship.RelationshipID,
		TargetRelationshipVersion: first.Relationship.Version,
		Kind:                      "challenges",
		VerificationEventID:       ownerBRelationship.VerificationEventID,
	})
	require.NoError(t, err)

	_, err = searchRepo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID:         teamA,
		OwnerProfileID: ownerA,
		SourceKind:     "relationship",
		SourceID:       first.Relationship.RelationshipID,
		SourceVersion:  int64(first.Relationship.Version),
		DocumentText:   "Mark Huang works on Dense-Mem.",
		DocumentHash:   "sha256:trace-relationship",
	})
	require.NoError(t, err)

	err = rls.WithTeamProfileTx(ctx, appDB, teamA, ownerA, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO entity_correction_events (
			    team_id, owner_profile_id, action, survivor_entity_id,
			    selected_observation_ids, reason
			) VALUES (
			    ?::uuid, ?::uuid, 'merge', ?::uuid,
			    ARRAY[?::uuid], 'trace regression coverage'
			)
		`, teamA, ownerA, mark.EntityID, first.ObservationID).Error
	})
	require.NoError(t, err)

	trace, err := semanticRepo.TraceRelationship(ctx, TraceRelationshipInput{
		TeamID:                  teamA,
		RelationshipID:          first.Relationship.RelationshipID,
		MaxEdges:                1,
		MaxEvents:               20,
		MaxFragmentContentRunes: 12,
	})
	require.NoError(t, err)
	require.NotNil(t, trace.Relationship)
	assert.Equal(t, first.Relationship.RelationshipID, trace.Relationship.RelationshipID)
	assert.Equal(t, "Mark Huang", trace.Relationship.SubjectName)
	assert.Equal(t, "Dense-Mem", trace.Relationship.ObjectEntityName)
	require.NotEmpty(t, trace.Observations)
	require.NotEmpty(t, trace.EvidenceSupports)
	require.NotEmpty(t, trace.SupportDecisionEvents)
	require.NotEmpty(t, trace.VerificationEvents)
	require.NotEmpty(t, trace.Transitions)
	require.NotEmpty(t, trace.CrossProfileReferences)
	require.NotEmpty(t, trace.IdentityCorrections)
	require.NotEmpty(t, trace.SearchDocuments)
	require.NotEmpty(t, trace.EmbeddingJobs)
	require.NotEmpty(t, trace.EvidenceFragments)
	assert.True(t, trace.EvidenceFragments[0].ContentTruncated)
	assert.Equal(t, "Mark Huang w", trace.EvidenceFragments[0].Content)
	assert.Len(t, trace.SemanticEdges, 1)
	assert.True(t, trace.Truncated)
	assert.Equal(t, "max_edges", trace.StoppedReason)
	assert.Contains(t, trace.VisitedEntityIDs, mark.EntityID)
	assert.NotContains(t, trace.VisitedEntityIDs, postgres.EntityID, "candidate relationship endpoints must not appear in active graph context")

	noContent := false
	trace, err = semanticRepo.TraceRelationship(ctx, TraceRelationshipInput{
		TeamID:                 teamA,
		RelationshipID:         first.Relationship.RelationshipID,
		IncludeEvidenceContent: &noContent,
	})
	require.NoError(t, err)
	require.NotEmpty(t, trace.EvidenceFragments)
	assert.Empty(t, trace.EvidenceFragments[0].Content)

	_, err = semanticRepo.TraceRelationship(ctx, TraceRelationshipInput{
		TeamID:         teamB,
		RelationshipID: first.Relationship.RelationshipID,
	})
	require.Error(t, err)
}

func TestSemanticGraphReadsEntityValueEdgesAndNodeDetail(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "graph-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "graph-owner")
	ledgerRepo := NewLedgerRepository(appDB, rls)
	semanticRepo := NewSemanticRepository(appDB, rls)

	denseMem := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "project", "Dense-Mem")
	postgres := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "product", "PostgreSQL")
	releaseDate, err := semanticRepo.UpsertValue(ctx, UpsertValueInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		ValueType:      "date",
		CanonicalValue: "2026-07-17",
		Display:        "July 17, 2026",
	})
	require.NoError(t, err)

	usesIngest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"graph dense mem uses postgres", "Dense-Mem uses PostgreSQL.")
	uses := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        usesIngest.IngestID,
		SubjectEntityID: denseMem.EntityID,
		PredicateKey:    "uses",
		ObjectEntityID:  postgres.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID:     usesIngest.Evidence[0].FragmentID,
			SourceGroupKey: "conversation:graph:uses",
			SpanStart:      0,
			SpanEnd:        len("Dense-Mem uses PostgreSQL."),
			Authority:      "primary",
		},
	})
	require.NotNil(t, uses.Relationship)

	releaseIngest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"graph dense mem released", "Dense-Mem released on July 17, 2026.")
	released := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        releaseIngest.IngestID,
		SubjectEntityID: denseMem.EntityID,
		PredicateKey:    "released",
		ObjectValueID:   releaseDate.ValueID,
		PromoteToFact:   true,
		Support: &EvidenceSupportInput{
			FragmentID:     releaseIngest.Evidence[0].FragmentID,
			SourceGroupKey: "conversation:graph:released",
			SpanStart:      0,
			SpanEnd:        len("Dense-Mem released on July 17, 2026."),
			Authority:      "primary",
		},
	})
	require.NotNil(t, released.Relationship)

	graph, err := semanticRepo.SemanticGraph(ctx, SemanticGraphQuery{
		TeamID: teamID,
		Query:  "dense-mem",
		Limit:  20,
	})
	require.NoError(t, err)
	assert.Len(t, graph.Edges, 2)
	assertGraphHasNode(t, graph.Nodes, "entity", denseMem.EntityID, "Dense-Mem")
	assertGraphHasNode(t, graph.Nodes, "entity", postgres.EntityID, "PostgreSQL")
	assertGraphHasNode(t, graph.Nodes, "value", releaseDate.ValueID, "July 17, 2026")

	valueOnly, err := semanticRepo.SemanticGraph(ctx, SemanticGraphQuery{
		TeamID: teamID,
		Types:  []string{"value"},
		Limit:  20,
	})
	require.NoError(t, err)
	assert.Empty(t, valueOnly.Edges, "graph edges require Entity source nodes even when value nodes exist")

	local, err := semanticRepo.SemanticGraph(ctx, SemanticGraphQuery{
		TeamID:     teamID,
		Scope:      "local",
		AnchorType: "entity",
		AnchorID:   denseMem.EntityID,
		Depth:      1,
		Limit:      1,
	})
	require.NoError(t, err)
	assert.Len(t, local.Edges, 1)
	assert.True(t, local.Truncated)

	node, err := semanticRepo.SemanticGraphNodeDetail(ctx, SemanticGraphNodeDetailInput{
		TeamID:   teamID,
		NodeType: "entity",
		NodeID:   denseMem.EntityID,
	})
	require.NoError(t, err)
	assert.Equal(t, "Dense-Mem", node.Title)
	assert.Equal(t, "project", node.Body)

	valueNode, err := semanticRepo.SemanticGraphNodeDetail(ctx, SemanticGraphNodeDetailInput{
		TeamID:   teamID,
		NodeType: "value",
		NodeID:   releaseDate.ValueID,
	})
	require.NoError(t, err)
	assert.Equal(t, "July 17, 2026", valueNode.Title)
}

func TestSemanticGraphLocalDepthDefaultsToTwoAndCapsAtFive(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "graph-depth-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "graph-depth-owner")
	ledgerRepo := NewLedgerRepository(appDB, rls)
	semanticRepo := NewSemanticRepository(appDB, rls)

	entities := make([]*EntityRecord, 7)
	for index := range entities {
		entities[index] = createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "concept", fmt.Sprintf("Depth Node %d", index))
	}
	for index := 0; index < len(entities)-1; index++ {
		content := fmt.Sprintf("Depth Node %d uses Depth Node %d.", index, index+1)
		ingest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerID, fmt.Sprintf("graph-depth-%d", index), content)
		applied := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
			TeamID: teamID, OwnerProfileID: ownerID, IngestID: ingest.IngestID,
			SubjectEntityID: entities[index].EntityID, PredicateKey: "uses", ObjectEntityID: entities[index+1].EntityID,
			Support: &EvidenceSupportInput{
				FragmentID: ingest.Evidence[0].FragmentID, SourceGroupKey: fmt.Sprintf("conversation:graph-depth:%d", index),
				SpanStart: 0, SpanEnd: len(content), Authority: "primary",
			},
		})
		require.NotNil(t, applied.Relationship)
	}

	defaults, err := semanticRepo.SemanticGraph(ctx, SemanticGraphQuery{
		TeamID: teamID, Scope: "local", AnchorType: "entity", AnchorID: entities[0].EntityID, Limit: 181,
	})
	require.NoError(t, err)
	assert.Equal(t, 2, defaults.Depth)
	assert.Equal(t, 181, defaults.Limit)
	assert.Len(t, defaults.Edges, 2)
	assert.Contains(t, semanticGraphNodeIDs(defaults.Nodes), entities[2].EntityID)
	assert.NotContains(t, semanticGraphNodeIDs(defaults.Nodes), entities[3].EntityID)

	capped, err := semanticRepo.SemanticGraph(ctx, SemanticGraphQuery{
		TeamID: teamID, Scope: "local", AnchorType: "entity", AnchorID: entities[0].EntityID, Depth: 99, Limit: 181,
	})
	require.NoError(t, err)
	assert.Equal(t, 5, capped.Depth)
	assert.Equal(t, 181, capped.Limit)
	assert.Len(t, capped.Edges, 5)
	assert.Contains(t, semanticGraphNodeIDs(capped.Nodes), entities[5].EntityID)
	assert.NotContains(t, semanticGraphNodeIDs(capped.Nodes), entities[6].EntityID)
}
