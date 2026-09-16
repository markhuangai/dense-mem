//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/domain"
	graphcontract "github.com/markhuangai/dense-mem/internal/graph/contract"
	graphpostgres "github.com/markhuangai/dense-mem/internal/graph/postgres"
	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	privacypostgres "github.com/markhuangai/dense-mem/internal/privacy/postgres"
	rememberservice "github.com/markhuangai/dense-mem/internal/remember/service"
)

func TestRememberIdentityCharacterizationMeasuresFragmentationAndProvenance(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "identity-characterization", 3, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "identity-characterization-team")
	ownerA := createLedgerProfile(t, adminDB, rls, teamID, "identity-characterization-owner-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamID, "identity-characterization-owner-b")
	ownerC := createLedgerProfile(t, adminDB, rls, teamID, "identity-characterization-owner-c")
	otherTeam := createLedgerTeam(t, adminDB, rls, "identity-characterization-other-team")
	otherOwner := createLedgerProfile(t, adminDB, rls, otherTeam, "identity-characterization-other-owner")
	semantic := NewStore(appDB, rls, ConflictRuntimeConfig{})

	knownHarbor := createSemanticEntity(t, ctx, semantic, teamID, ownerA, "product", "Harbor")
	knownPine := createSemanticEntity(t, ctx, semantic, teamID, ownerA, "project", "Pine")
	input := identityCharacterizationRememberInput(teamID, ownerA, knownHarbor.EntityID, knownPine.EntityID)
	plan, err := semantic.PlanRememberEmbeddings(ctx, input)
	require.NoError(t, err)
	result, err := semantic.CommitRememberWithEmbeddings(ctx, input, rememberTestEmbeddings(plan, false))
	require.NoError(t, err)
	require.Equal(t, "completed", result.Outcome)
	require.Len(t, result.RelationshipResults, 3)

	type entityRow struct {
		MentionRef string
		EntityID   string
		Occurrence string
		Kind       string
		Name       string
	}
	var entities []entityRow
	entitiesByRef := make(map[string]string)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		rows, err := tx.Raw(`
			SELECT event.mention_ref, event.entity_id::text, event.occurrence_id::text, record.entity_kind, canonical.display_name
			FROM entity_resolution_events AS event
			JOIN entity_records AS record
			  ON record.team_id = event.team_id AND record.entity_id = event.entity_id
			JOIN entity_names AS canonical
			  ON canonical.team_id = record.team_id AND canonical.entity_id = record.entity_id
			 AND canonical.name_kind = 'canonical' AND canonical.valid_to IS NULL
			WHERE event.team_id = ?::uuid AND event.ingest_id = ?::uuid
			ORDER BY event.mention_ref, event.resolution_event_id
		`, teamID, input.IngestID).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var row entityRow
			if err := rows.Scan(&row.MentionRef, &row.EntityID, &row.Occurrence, &row.Kind, &row.Name); err != nil {
				return err
			}
			entities = append(entities, row)
			entitiesByRef[row.MentionRef] = row.EntityID
			require.NotEmpty(t, row.Occurrence, "every accepted grounding must retain occurrence lineage")
		}
		return rows.Err()
	}))
	require.Len(t, entities, 7)

	idsByNameKind := make(map[string][]string)
	for _, entity := range entities {
		key := entity.Name + "/" + entity.Kind
		idsByNameKind[key] = append(idsByNameKind[key], entity.EntityID)
	}
	for key := range idsByNameKind {
		idsByNameKind[key] = uniqueSortedStrings(idsByNameKind[key])
	}
	require.Len(t, idsByNameKind["Cedar/project"], 2, "repeated Cedar mentions create separate project identities")
	require.Len(t, idsByNameKind["Cedar/product"], 1, "kind differences remain distinct")
	require.Len(t, idsByNameKind["Harbor/product"], 2, "a create and an explicit known identity are retained")
	require.Len(t, idsByNameKind["Pine/project"], 1, "the explicit Pine control reuses one identity")
	require.Contains(t, idsByNameKind["Harbor/product"], knownHarbor.EntityID)
	require.Contains(t, idsByNameKind["Pine/project"], knownPine.EntityID)

	graph, err := graphpostgres.NewStore(appDB, rls).SemanticGraph(ctx, graphcontract.Query{TeamID: teamID, Limit: 20})
	require.NoError(t, err)
	require.Len(t, graph.Edges, 3)
	expectedEdges := map[string]struct{}{
		"entity:" + entitiesByRef["cedar:one"] + "->entity:" + entitiesByRef["harbor:one"]: {},
		"entity:" + entitiesByRef["cedar:two"] + "->entity:" + knownPine.EntityID:          {},
		"entity:" + knownHarbor.EntityID + "->entity:" + knownPine.EntityID:                {},
	}
	for _, edge := range graph.Edges {
		delete(expectedEdges, edge.Source+"->"+edge.Target)
	}
	require.Empty(t, expectedEdges, "graph endpoints must preserve the durable identity mapping")
	var pineNode *graphcontract.Node
	for index := range graph.Nodes {
		if graph.Nodes[index].ID == knownPine.EntityID {
			pineNode = &graph.Nodes[index]
			break
		}
	}
	require.NotNil(t, pineNode)
	require.Equal(t, "Pine", pineNode.Title)
	require.Equal(t, "project", pineNode.Body)
	require.Equal(t, ownerA, pineNode.OwnerProfileID)

	traceRepo := newKnowledgeTraceFixtureStore(appDB, rls)
	expectedSupports := map[string]struct {
		start int
		end   int
		quote string
	}{
		"r:cedar-harbor": {start: 0, end: len("Cedar uses Harbor."), quote: "Cedar uses Harbor."},
		"r:cedar-pine":   {start: len("Cedar uses Harbor. "), end: len("Cedar uses Harbor. Cedar uses Pine."), quote: "Cedar uses Pine."},
		"r:harbor-pine":  {start: len("Cedar uses Harbor. Cedar uses Pine. "), end: len(input.Evidence[0].Content), quote: "Harbor uses Pine."},
	}
	for _, relationship := range result.RelationshipResults {
		require.NotNil(t, relationship.Relationship)
		require.Equal(t, "active", relationship.Relationship.Status)
		trace, err := traceRepo.TraceRelationship(ctx, TraceRelationshipInput{TeamID: teamID, RelationshipID: relationship.Relationship.RelationshipID})
		require.NoError(t, err)
		require.NotNil(t, trace.Relationship)
		require.Len(t, trace.EvidenceSupports, 1)
		require.Len(t, trace.EvidenceFragments, 1)
		require.Equal(t, ownerA, trace.Relationship.OwnerProfileID)
		require.Equal(t, input.Evidence[0].Content, trace.EvidenceFragments[0].Content)
		support := trace.EvidenceSupports[0]
		expected, ok := expectedSupports[support.SourceGroupKey]
		require.True(t, ok, "unexpected support source group %q", support.SourceGroupKey)
		require.Equal(t, expected.start, support.SpanStart)
		require.Equal(t, expected.end, support.SpanEnd)
		require.Equal(t, expected.quote, support.Quote)
		require.NotEmpty(t, support.OccurrenceID)
		require.Equal(t, support.OccurrenceID, trace.EvidenceFragments[0].OccurrenceID)
	}

	catalog, err := semantic.ListSubmissionAssessmentEntityCatalog(ctx, SubmissionAssessmentEntityCatalogInput{
		TeamID: teamID, OwnerProfileID: ownerB,
		Entities: []SubmissionAssessmentEntityCatalogTarget{{Ref: "harbor", Surface: "Harbor", EntityKind: "product"}}, CandidateLimit: 20,
	})
	require.NoError(t, err)
	require.Len(t, catalog.Groups, 1)
	require.Len(t, catalog.Groups[0].Candidates, 2)
	knownCatalog, err := semantic.ListSubmissionAssessmentEntityCatalog(ctx, SubmissionAssessmentEntityCatalogInput{
		TeamID: teamID, OwnerProfileID: ownerB,
		Entities: []SubmissionAssessmentEntityCatalogTarget{{Ref: "known-harbor", Surface: "Harbor", EntityKind: "product", KnownEntityID: knownHarbor.EntityID}}, CandidateLimit: 20,
	})
	require.NoError(t, err)
	require.Len(t, knownCatalog.Groups[0].Candidates, 1)
	require.Equal(t, knownHarbor.EntityID, knownCatalog.Groups[0].Candidates[0].EntityID)
	ownerCCatalog, err := semantic.ListSubmissionAssessmentEntityCatalog(ctx, SubmissionAssessmentEntityCatalogInput{
		TeamID: teamID, OwnerProfileID: ownerC,
		Entities: []SubmissionAssessmentEntityCatalogTarget{{Ref: "harbor", Surface: "Harbor", EntityKind: "product"}}, CandidateLimit: 20,
	})
	require.NoError(t, err)
	require.Len(t, ownerCCatalog.Groups[0].Candidates, 2, "a third same-team profile retains read visibility")
	otherCatalog, err := semantic.ListSubmissionAssessmentEntityCatalog(ctx, SubmissionAssessmentEntityCatalogInput{
		TeamID: otherTeam, OwnerProfileID: otherOwner,
		Entities: []SubmissionAssessmentEntityCatalogTarget{{Ref: "harbor", Surface: "Harbor", EntityKind: "product"}}, CandidateLimit: 20,
	})
	require.NoError(t, err)
	require.Empty(t, otherCatalog.Groups[0].Candidates, "a different team cannot resolve the submitted identities")
}

func TestRememberIdentityCharacterizationRejectsPrivateAndInaccessibleReferencesAtomically(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "identity-characterization-isolation", 3, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "identity-characterization-isolation-team")
	ownerA := createLedgerProfile(t, adminDB, rls, teamID, "identity-characterization-isolation-owner-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamID, "identity-characterization-isolation-owner-b")
	otherTeam := createLedgerTeam(t, adminDB, rls, "identity-characterization-isolation-other-team")
	otherOwner := createLedgerProfile(t, adminDB, rls, otherTeam, "identity-characterization-isolation-other-owner")
	semantic := NewStore(appDB, rls, ConflictRuntimeConfig{})
	knownPine := createSemanticEntity(t, ctx, semantic, teamID, ownerA, "project", "Pine")
	crossTeamEntity := createSemanticEntity(t, ctx, semantic, otherTeam, otherOwner, "product", "Harbor")

	privateSpace, err := privacypostgres.NewMemorySpaceRepository(appDB, rls).EnsureCredentialPrivate(ctx, uuid.MustParse(teamID), uuid.MustParse(ownerA))
	require.NoError(t, err)
	var privateGeneration int64
	privateEntityID, privateNameID := uuid.New(), uuid.New()
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Raw(`SELECT generation FROM memory_spaces WHERE id = ?::uuid`, privateSpace.ID).Row().Scan(&privateGeneration); err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO entity_records (team_id, entity_id, entity_kind, identity_context, metadata, space_id, space_generation)
			VALUES (?::uuid, ?::uuid, 'product', '{}'::jsonb, '{}'::jsonb, ?::uuid, ?)
		`, teamID, privateEntityID, privateSpace.ID, privateGeneration).Error; err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO entity_names (team_id, entity_name_id, entity_id, owner_profile_id, display_name, normalized_name, name_kind, metadata, space_id, space_generation)
			VALUES (?::uuid, ?::uuid, ?::uuid, ?::uuid, 'Private Harbor', 'private harbor', 'canonical', '{}'::jsonb, ?::uuid, ?)
		`, teamID, privateNameID, privateEntityID, ownerA, privateSpace.ID, privateGeneration).Error
	}))

	privateCatalog, err := semantic.ListSubmissionAssessmentEntityCatalog(ctx, SubmissionAssessmentEntityCatalogInput{
		TeamID: teamID, OwnerProfileID: ownerB,
		Entities: []SubmissionAssessmentEntityCatalogTarget{{Ref: "private", Surface: "Private Harbor", EntityKind: "product"}}, CandidateLimit: 20,
	})
	require.NoError(t, err)
	require.Empty(t, privateCatalog.Groups[0].Candidates, "private-space entities must not enter the team-shared catalog")

	input := identityCharacterizationRememberInput(teamID, ownerA, crossTeamEntity.EntityID, knownPine.EntityID)
	input.IdempotencyKey = "identity-characterization-inaccessible-reference"
	input.RequestHash = sha256Hex(input.IdempotencyKey)
	plan, err := semantic.PlanRememberEmbeddings(ctx, input)
	require.NoError(t, err)
	_, err = semantic.CommitRememberWithEmbeddings(ctx, input, rememberTestEmbeddings(plan, false))
	require.ErrorIs(t, err, ErrRememberExactReferenceStale)

	var ingestCount, eventCount, relationshipCount int64
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Raw(`SELECT count(*) FROM knowledge_ingests WHERE team_id = ?::uuid AND ingest_id = ?::uuid`, teamID, input.IngestID).Row().Scan(&ingestCount); err != nil {
			return err
		}
		if err := tx.Raw(`SELECT count(*) FROM entity_resolution_events WHERE team_id = ?::uuid AND ingest_id = ?::uuid`, teamID, input.IngestID).Row().Scan(&eventCount); err != nil {
			return err
		}
		return tx.Raw(`SELECT count(*) FROM relationship_observations WHERE team_id = ?::uuid AND ingest_id = ?::uuid`, teamID, input.IngestID).Row().Scan(&relationshipCount)
	}))
	require.Zero(t, ingestCount, "inaccessible exact references must roll back the intake")
	require.Zero(t, eventCount, "inaccessible exact references must not leave resolution events")
	require.Zero(t, relationshipCount, "inaccessible exact references must not leave relationship observations")
}

func TestRememberIdentityCharacterizationUsesValidatedAssessmentCommitPath(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "identity-characterization-assessment", 3, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "identity-characterization-assessment-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "identity-characterization-assessment-owner")
	semantic := NewStore(appDB, rls, ConflictRuntimeConfig{})
	knownHarbor := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "product", "Harbor")
	assessmentInput := identityAssessmentInput(teamID, ownerID, knownHarbor.EntityID)
	provider := &identityAssessmentProvider{}
	prepared, err := rememberservice.AssessSynchronousRemember(ctx, rememberservice.SynchronousAssessmentDependencies{
		Catalog: semantic, Provider: provider, Limits: assessor.DefaultSemanticAssessmentLimits(),
	}, assessmentInput)
	require.NoError(t, err)

	commitInput, err := rememberservice.BuildSynchronousRememberCommitInput(rememberservice.SynchronousRememberCommitRequest{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: assessmentInput.Scope.IngestID,
		IdempotencyKey: "identity-characterization-assessment", RequestHash: sha256Hex("identity-characterization-assessment"),
		SourceSummary: "identity-characterization-assessment", Evidence: []knowledgecontract.EvidenceInput{
			{FragmentID: assessmentInput.Snapshot.Evidence[0].FragmentID, Content: assessmentInput.Snapshot.Evidence[0].Content, ContentHash: sha256Hex(assessmentInput.Snapshot.Evidence[0].Content), SourceType: "conversation", Authority: "primary"},
			{FragmentID: assessmentInput.Snapshot.Evidence[1].FragmentID, Content: assessmentInput.Snapshot.Evidence[1].Content, ContentHash: sha256Hex(assessmentInput.Snapshot.Evidence[1].Content), SourceType: "conversation", Authority: "primary"},
		}, Assessment: prepared,
	})
	require.NoError(t, err)
	cedarEvidenceZeroResolutions := 0
	for _, entry := range commitInput.Commit.EntityResolutions {
		if entry.Resolution.CanonicalName == "Cedar" && entry.Resolution.FragmentID == assessmentInput.Snapshot.Evidence[0].FragmentID {
			cedarEvidenceZeroResolutions++
		}
	}
	require.Equal(t, 1, cedarEvidenceZeroResolutions, "matching assessor decisions on one grounding must coalesce before commit")
	plan, err := semantic.PlanRememberEmbeddings(ctx, commitInput)
	require.NoError(t, err)
	embeddings := rememberTestEmbeddings(plan, false)
	embeddedHashes := make(map[string]struct{}, len(embeddings))
	for _, embedding := range embeddings {
		embeddedHashes[embedding.DocumentHash] = struct{}{}
	}
	for _, evidence := range commitInput.Evidence {
		hash := searchDocumentHash(evidence.Content)
		if _, exists := embeddedHashes[hash]; exists {
			continue
		}
		vector := make([]float32, plan.EmbeddingDimensions)
		if len(vector) > 0 {
			vector[0] = 1
		}
		embeddings = append(embeddings, InlineEmbeddingResult{
			DocumentHash: hash, Embedding: vector, EmbeddingContractID: plan.EmbeddingContractID,
			EmbeddingDimensions: plan.EmbeddingDimensions, EmbeddingModel: plan.EmbeddingModel,
			SearchIndexGenerationID: plan.SearchIndexGenerationID, IndexGeneration: plan.IndexGeneration,
		})
		embeddedHashes[hash] = struct{}{}
	}
	result, err := semantic.CommitRememberWithEmbeddings(ctx, commitInput, embeddings)
	require.NoError(t, err)
	require.Equal(t, "completed", result.Outcome)
	require.Empty(t, result.RelationshipResults)

	alphaIDsByFragment := make(map[string][]string)
	var harborIDs []string
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		rows, err := tx.Raw(`
			SELECT event.mention_ref, event.entity_id::text, event.fragment_id::text, name.display_name
			FROM entity_resolution_events AS event
			JOIN entity_records AS record ON record.team_id = event.team_id AND record.entity_id = event.entity_id
			JOIN entity_names AS name ON name.team_id = record.team_id AND name.entity_id = record.entity_id
			 AND name.name_kind = 'canonical' AND name.valid_to IS NULL
			WHERE event.team_id = ?::uuid AND event.ingest_id = ?::uuid AND name.display_name IN ('Cedar', 'Harbor')
			ORDER BY event.mention_ref
		`, teamID, assessmentInput.Scope.IngestID).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var ref, entityID, fragmentID, name string
			if err := rows.Scan(&ref, &entityID, &fragmentID, &name); err != nil {
				return err
			}
			if name == "Cedar" {
				alphaIDsByFragment[fragmentID] = append(alphaIDsByFragment[fragmentID], entityID)
			}
			if name == "Harbor" {
				harborIDs = append(harborIDs, entityID)
			}
		}
		return rows.Err()
	}))
	require.Len(t, alphaIDsByFragment, 2)
	var alphaIDs []string
	for fragmentID, ids := range alphaIDsByFragment {
		require.Len(t, uniqueSortedStrings(ids), 1, "one Cedar identity is selected per evidence item")
		if fragmentID == assessmentInput.Snapshot.Evidence[0].FragmentID {
			alphaIDs = append(alphaIDs, ids[0])
		}
		if fragmentID == assessmentInput.Snapshot.Evidence[1].FragmentID {
			alphaIDs = append(alphaIDs, ids[0])
		}
	}
	require.Len(t, uniqueSortedStrings(alphaIDs), 2, "new Cedar mentions across evidence remain separately created without an explicit identity")
	require.Equal(t, []string{knownHarbor.EntityID}, uniqueSortedStrings(harborIDs))
}

type identityAssessmentSession struct{}

func (identityAssessmentSession) SessionID() string { return "identity-characterization-session" }

type identityAssessmentProvider struct{}

func (p *identityAssessmentProvider) Assess(_ context.Context, request assessor.SemanticAssessmentRequest) (assessor.SemanticAssessmentSession, assessor.SemanticAssessmentTurn, error) {
	return identityAssessmentSession{}, assessor.SemanticAssessmentTurn{Response: identityAssessmentResponse(request), Turn: 1}, nil
}

func (p *identityAssessmentProvider) Repair(_ context.Context, _ assessor.SemanticAssessmentSession, request assessor.SemanticAssessmentRepairRequest) (assessor.SemanticAssessmentTurn, error) {
	return assessor.SemanticAssessmentTurn{Response: identityAssessmentResponse(request.Request), Turn: 2}, nil
}

func (p *identityAssessmentProvider) ModelName() string { return "identity-characterization-provider" }

func identityAssessmentInput(teamID, ownerID, knownHarborID string) rememberservice.SynchronousAssessmentInput {
	first := knowledgecontract.EvidenceFragment{FragmentID: uuid.NewString(), EvidenceIndex: 0, Content: "Cedar uses Harbor. Cedar uses Pine.", Authority: "primary"}
	second := knowledgecontract.EvidenceFragment{FragmentID: uuid.NewString(), EvidenceIndex: 1, Content: "Cedar uses Pine. Harbor uses Pine.", Authority: "primary"}
	return rememberservice.SynchronousAssessmentInput{
		Scope: rememberservice.RememberAssessmentScope{TeamID: teamID, OwnerProfileID: ownerID, IngestID: uuid.NewString()},
		Snapshot: rememberservice.RememberAssessmentSnapshot{
			Evidence: []knowledgecontract.EvidenceFragment{first, second},
			Items: []rememberservice.RememberAssessmentItem{
				{ItemID: uuid.NewString(), Fragment: first}, {ItemID: uuid.NewString(), Fragment: second},
			},
			Proposal: map[string]any{"relationship_hints": []any{
				map[string]any{
					"ref": "r:cedar-harbor", "subject": map[string]any{"name": "Cedar", "entity_kind": "project"},
					"predicate": map[string]any{"proposed_key": "uses"}, "object": map[string]any{"entity": map[string]any{"name": "Harbor", "entity_kind": "product", "known_entity_id": knownHarborID}},
					"polarity": "+", "evidence_indices": []any{0},
				},
				map[string]any{
					"ref": "r:cedar-pine", "subject": map[string]any{"name": "Cedar", "entity_kind": "project"},
					"predicate": map[string]any{"proposed_key": "uses"}, "object": map[string]any{"entity": map[string]any{"name": "Pine", "entity_kind": "project"}},
					"polarity": "+", "evidence_indices": []any{1},
				},
				map[string]any{
					"ref": "r:cedar-pine-shared", "subject": map[string]any{"name": "Cedar", "entity_kind": "project"},
					"predicate": map[string]any{"proposed_key": "uses"}, "object": map[string]any{"entity": map[string]any{"name": "Pine", "entity_kind": "project"}},
					"polarity": "+", "evidence_indices": []any{0},
				},
				map[string]any{
					"ref": "r:harbor-pine", "subject": map[string]any{"name": "Harbor", "entity_kind": "product", "known_entity_id": knownHarborID},
					"predicate": map[string]any{"proposed_key": "uses"}, "object": map[string]any{"entity": map[string]any{"name": "Pine", "entity_kind": "project"}},
					"polarity": "+", "evidence_indices": []any{1},
				},
			}},
		},
	}
}

func identityAssessmentResponse(request assessor.SemanticAssessmentRequest) assessor.SemanticAssessmentResponse {
	response := assessor.SemanticAssessmentResponse{
		RequestID: request.RequestID, EvidenceEquivalenceResults: []assessor.SemanticAssessmentEvidenceEquivalenceResult{},
		EvidenceConflictResults: []assessor.SemanticAssessmentEvidenceConflictResult{}, EntityResults: []assessor.SemanticAssessmentEntityResult{},
		RelationshipResults: []assessor.SemanticAssessmentRelationshipResult{}, EvidenceSecurityResults: []assessor.SemanticAssessmentEvidenceSecurityResult{},
	}
	for _, evidence := range request.Evidence {
		response.EvidenceSecurityResults = append(response.EvidenceSecurityResults, assessor.SemanticAssessmentEvidenceSecurityResult{EvidenceID: evidence.EvidenceID, Decision: "pass", Signals: []assessor.SemanticAssessmentSecuritySignal{}})
	}
	for _, entity := range request.SubmittedEntities {
		if len(entity.Groundings) == 0 {
			continue
		}
		groundingRef := entity.Groundings[0].GroundingRef
		result := assessor.SemanticAssessmentEntityResult{Ref: entity.Ref, GroundingRef: &groundingRef, Action: string(domain.EntityResolutionCreate)}
		for _, group := range request.EntityCandidateGroups {
			if group.GroundingRef != groundingRef || len(group.Candidates) == 0 {
				continue
			}
			candidateID := group.Candidates[0].EntityID
			result.Action = string(domain.EntityResolutionReuse)
			result.CandidateEntityID = &candidateID
			break
		}
		response.EntityResults = append(response.EntityResults, result)
	}
	for _, relationship := range request.SubmittedRelationships {
		evidence := identityAssessmentEvidence(request.Evidence, relationship.EvidenceIDs[0])
		predicateStart := strings.Index(evidence.Content, relationship.PredicateHint)
		if predicateStart < 0 {
			predicateStart = 0
		}
		predicateEnd := predicateStart + len(relationship.PredicateHint)
		predicateStartRef, _ := assessor.SemanticAssessmentBoundaryRef(evidence, predicateStart)
		predicateEndRef, _ := assessor.SemanticAssessmentBoundaryRef(evidence, predicateEnd)
		supportStartRef, _ := assessor.SemanticAssessmentBoundaryRef(evidence, 0)
		supportEndRef, _ := assessor.SemanticAssessmentBoundaryRef(evidence, len([]rune(evidence.Content)))
		predicateKey, predicateVersion := relationship.PredicateHint, 1
		response.RelationshipResults = append(response.RelationshipResults, assessor.SemanticAssessmentRelationshipResult{
			Ref: relationship.Ref, Disposition: "stored", Splits: []assessor.SemanticAssessmentRelationshipSplit{{
				SplitIndex: 0, SubjectRef: relationship.SubjectRef, PredicateRange: assessor.SemanticAssessmentGroundedRange{EvidenceID: evidence.EvidenceID, StartRef: predicateStartRef, EndRef: predicateEndRef},
				PredicateStatus: "resolved", PredicateKey: &predicateKey, PredicateVersion: &predicateVersion, ObjectRef: relationship.ObjectRef, ObjectValue: relationship.ObjectValue, Polarity: relationship.Polarity,
				SupportRanges: []assessor.SemanticAssessmentGroundedRange{{EvidenceID: evidence.EvidenceID, StartRef: supportStartRef, EndRef: supportEndRef}}, Evidence: []assessor.SemanticAssessmentEvidenceSpan{{EvidenceID: evidence.EvidenceID, Start: 0, End: len([]rune(evidence.Content))}},
			}},
		})
	}
	for index := range response.RelationshipResults {
		reason := "not_supported_by_evidence"
		response.RelationshipResults[index].Disposition = "not_supported"
		response.RelationshipResults[index].Reason = &reason
		response.RelationshipResults[index].Splits = nil
	}
	return response
}

func identityAssessmentEvidence(evidence []assessor.SemanticReviewEvidence, id string) assessor.SemanticReviewEvidence {
	for _, item := range evidence {
		if item.EvidenceID == id {
			return item
		}
	}
	panic("identity characterization evidence is missing")
}

func identityCharacterizationRememberInput(teamID, ownerID, knownHarborID, knownPineID string) SynchronousRememberCommitInput {
	fragmentID, ingestID, assessmentID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	content := "Cedar uses Harbor. Cedar uses Pine. Harbor uses Pine."
	span := func(surface string, occurrence int) (*int, *int) {
		start := -1
		from := 0
		for index := 0; index <= occurrence; index++ {
			offset := strings.Index(content[from:], surface)
			if offset < 0 {
				panic(fmt.Sprintf("surface %q occurrence %d missing", surface, occurrence))
			}
			start = from + offset
			from = start + len(surface)
		}
		end := start + len(surface)
		return &start, &end
	}
	resolution := func(ref, action, kind, name string, occurrence int, exactID string) SubmissionAssessmentEntityResolutionInput {
		start, end := span(name, occurrence)
		return SubmissionAssessmentEntityResolutionInput{Resolution: SemanticEntityResolutionInput{
			MentionRef: ref, Action: action, EntityKind: kind, CanonicalName: name, ExactEntityID: exactID,
			EntityID:   exactID,
			FragmentID: fragmentID, SpanStart: start, SpanEnd: end, AssessmentID: assessmentID,
			IdentityContext: map[string]any{"surface": name, "source": "identity-characterization"},
			VerifierResult:  map[string]any{"decision": "server_accepted_grounding"}, Metadata: map[string]any{"case": "identity-characterization"},
		}}
	}
	observation := func(ref, subject, object string, start, end int) SubmissionAssessmentRelationshipObservationInput {
		return SubmissionAssessmentRelationshipObservationInput{RelationshipRef: ref, Observation: SemanticRelationshipDecisionInput{
			Ref: ref, SubjectRef: subject, ObjectRef: object, OriginalPredicate: "uses", PredicateKey: "uses", PredicateVersion: 1,
			Polarity: "+", AssessorAccepted: true, AssessmentID: assessmentID,
			Support: &EvidenceSupportInput{FragmentID: fragmentID, SourceGroupKey: ref, SpanStart: start, SpanEnd: end, Quote: content[start:end], Authority: "primary"},
		}}
	}
	return SynchronousRememberCommitInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: ingestID, IdempotencyKey: "identity-characterization", RequestHash: sha256Hex(content), SourceSummary: "identity-characterization",
		Evidence:     []EvidenceInput{{FragmentID: fragmentID, Content: content, ContentHash: sha256Hex(content), SourceType: "conversation", Authority: "primary"}},
		AssessmentID: assessmentID, AssessmentJSON: json.RawMessage(`{"request_id":"identity-characterization"}`), ProviderTurns: 1,
		EvidenceSecurityResults: []EvidenceSecurityResult{{FragmentID: fragmentID, EvidenceID: "evidence:0", EvidenceIndex: 0, Decision: "pass", Safe: true}},
		Commit: CommitSubmissionAssessmentInput{
			AssessmentID: assessmentID, Items: []SubmissionAssessmentItemInput{{FragmentID: fragmentID}},
			EntityResolutions: []SubmissionAssessmentEntityResolutionInput{
				resolution("cedar:one", string(domain.EntityResolutionCreate), "project", "Cedar", 0, ""),
				resolution("harbor:one", string(domain.EntityResolutionCreate), "product", "Harbor", 0, ""),
				resolution("cedar:two", string(domain.EntityResolutionCreate), "project", "Cedar", 1, ""),
				resolution("pine:one", string(domain.EntityResolutionReuse), "project", "Pine", 0, knownPineID),
				resolution("harbor:known", string(domain.EntityResolutionReuse), "product", "Harbor", 1, knownHarborID),
				resolution("pine:two", string(domain.EntityResolutionReuse), "project", "Pine", 1, knownPineID),
				resolution("cedar:kind", string(domain.EntityResolutionCreate), "product", "Cedar", 1, ""),
			},
			RelationshipObservations: []SubmissionAssessmentRelationshipObservationInput{
				observation("r:cedar-harbor", "cedar:one", "harbor:one", 0, len("Cedar uses Harbor.")),
				observation("r:cedar-pine", "cedar:two", "pine:one", len("Cedar uses Harbor. "), len("Cedar uses Harbor. Cedar uses Pine.")),
				observation("r:harbor-pine", "harbor:known", "pine:two", len("Cedar uses Harbor. Cedar uses Pine. "), len(content)),
			},
			RelationshipResults: []SubmissionRelationshipResultInput{
				{RelationshipRef: "r:cedar-harbor", Disposition: "stored"}, {RelationshipRef: "r:cedar-pine", Disposition: "stored"}, {RelationshipRef: "r:harbor-pine", Disposition: "stored"},
			},
			Payload: map[string]any{"response_hash": sha256Hex(content), "model": "identity-characterization", "tokenizer": "o200k_base", "candidate_context_tokens": 0, "candidate_context_truncated": false},
		},
	}
}

func uniqueSortedStrings(values []string) []string {
	sort.Strings(values)
	unique := values[:0]
	for _, value := range values {
		if len(unique) == 0 || unique[len(unique)-1] != value {
			unique = append(unique, value)
		}
	}
	return unique
}
