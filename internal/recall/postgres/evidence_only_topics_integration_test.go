//go:build integration

package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	"github.com/markhuangai/dense-mem/internal/observability"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	recall "github.com/markhuangai/dense-mem/internal/recall"
	recallcontract "github.com/markhuangai/dense-mem/internal/recall/contract"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	"github.com/stretchr/testify/require"
)

func TestEvidenceOnlyPostgresTopicsUseDirectMembershipAndDeterministicHints(t *testing.T) {
	f := newRecallOntologyFixture(t)
	first, second := f.evidence(t, 0, "Shared topic first matching Evidence.", nil), f.evidence(t, 1, "Shared topic second matching Evidence.", nil)
	unseen := []ontology.SourceHandle{f.evidence(t, 0, "Additional child Evidence.", nil), f.evidence(t, 1, "Additional sibling Evidence one.", nil), f.evidence(t, 1, "Additional sibling Evidence two.", nil)}
	parentEvidence := f.evidence(t, 0, "Direct parent Evidence.", nil)
	parent := ontology.Record{ID: "73100000-0000-4000-8000-000000000001", Kind: ontology.Topic, Definition: &ontology.Definition{Key: "operations", Label: "Operations", Description: "Operational Evidence"}}
	child := ontology.Record{ID: "73100000-0000-4000-8000-000000000002", Kind: ontology.Topic, Definition: &ontology.Definition{Key: "database operations", Label: "Database operations", Description: "Database operating procedures", ParentID: parent.ID}}
	sibling := ontology.Record{ID: "73100000-0000-4000-8000-000000000003", Kind: ontology.Topic, Definition: &ontology.Definition{Key: "service operations", Label: "Service operations", Description: "Service operating procedures"}}
	f.publication(t, parent, child, sibling,
		f.assignment(t, parent.ID, parentEvidence),
		f.assignment(t, child.ID, first), f.assignment(t, child.ID, second), f.assignment(t, child.ID, unseen[0]),
		f.assignment(t, sibling.ID, first), f.assignment(t, sibling.ID, unseen[1]), f.assignment(t, sibling.ID, unseen[2]))
	ctx := f.actor(2, "member")
	input := RecallEvidenceInput{TeamID: f.team, SpaceID: f.space, SpaceKind: "team_shared", OrganizationEnabled: true, Limit: 10}
	hints, err := f.search.recall.RelatedEvidenceTopics(ctx, input, []string{first.ID, second.ID, parentEvidence.ID}, 2)
	require.NoError(t, err)
	require.Equal(t, []recallcontract.RelatedTopic{
		{TopicID: child.ID, Name: child.Definition.Label, Description: child.Definition.Description, Rank: 1, EligibleEvidenceCount: 3, MatchingResultCount: 2, AdditionalEvidenceCount: 1},
		{TopicID: sibling.ID, Name: sibling.Definition.Label, Description: sibling.Definition.Description, Rank: 2, EligibleEvidenceCount: 3, MatchingResultCount: 1, AdditionalEvidenceCount: 2},
	}, hints.Topics)
	require.True(t, hints.Truncated)
	input.TopicIDs = []string{parent.ID}
	result, err := f.search.RecallEvidence(ctx, input)
	require.NoError(t, err)
	require.Len(t, result.Results, 1)
	require.Equal(t, parentEvidence.ID, result.Results[0].EvidenceID, "a parent filter cannot expand its descendants")
	input.TopicIDs = []string{parent.ID, child.ID}
	result, err = f.search.RecallEvidence(ctx, input)
	require.NoError(t, err)
	ids := []string{}
	for _, hit := range result.Results {
		ids = append(ids, hit.EvidenceID)
	}
	require.ElementsMatch(t, []string{parentEvidence.ID, first.ID, second.ID, unseen[0].ID}, ids)
	input.KnownEvidenceIDs = []string{first.ID, unseen[0].ID}
	result, err = f.search.RecallEvidence(ctx, input)
	require.NoError(t, err)
	ids = nil
	for _, hit := range result.Results {
		ids = append(ids, hit.EvidenceID)
	}
	require.ElementsMatch(t, []string{parentEvidence.ID, second.ID}, ids)
	input.TopicIDs = nil
	hints, err = f.search.recall.RelatedEvidenceTopics(ctx, input, []string{first.ID, second.ID, parentEvidence.ID}, 10)
	require.NoError(t, err)
	for _, hint := range hints.Topics {
		if hint.TopicID == child.ID {
			require.Equal(t, 1, hint.EligibleEvidenceCount)
		}
		require.NotEqual(t, sibling.ID, hint.TopicID, "known results cannot make a topic hint eligible")
	}
}

func TestEvidenceOnlyPostgresTopicsRejectUnavailableAndReauthorizeCurrentMembership(t *testing.T) {
	f := newRecallOntologyFixture(t)
	first, withdrawn := f.evidence(t, 0, "Current shared topic Evidence.", nil), f.evidence(t, 1, "Withdrawn shared topic Evidence.", nil)
	parent := ontology.Record{ID: uuid.NewString(), Kind: ontology.Topic, Definition: &ontology.Definition{Key: "parent", Label: "Parent"}}
	child := ontology.Record{ID: uuid.NewString(), Kind: ontology.Topic, Definition: &ontology.Definition{Key: "child", Label: "Child", ParentID: parent.ID}}
	f.publication(t, parent, child, f.assignment(t, child.ID, first), f.assignment(t, child.ID, withdrawn))
	ctx := f.actor(2, "member")
	input := RecallEvidenceInput{TeamID: f.team, SpaceID: f.space, SpaceKind: "team_shared", OrganizationEnabled: true, TopicIDs: []string{child.ID}, Limit: 10}
	_, err := f.knowledge.RetractEvidence(f.actor(1, "member"), knowledge.RetractEvidenceInput{TeamID: f.team, OwnerProfileID: f.owners[1], EvidenceIDs: []string{withdrawn.ID}, Reason: "Withdraw source", IdempotencyKey: "withdraw-topic-source", RequestHash: sha256Hex("withdraw-topic-source")})
	require.NoError(t, err)
	result, err := f.search.RecallEvidence(ctx, input)
	require.NoError(t, err)
	require.Len(t, result.Results, 1)
	require.Equal(t, first.ID, result.Results[0].EvidenceID)
	hints, err := f.search.recall.RelatedEvidenceTopics(ctx, input, []string{first.ID}, 3)
	require.NoError(t, err)
	require.Len(t, hints.Topics, 1)
	require.Equal(t, 1, hints.Topics[0].EligibleEvidenceCount)
	beforeCreation := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	input.KnownAt = &beforeCreation
	result, err = f.search.RecallEvidence(ctx, input)
	require.NoError(t, err)
	require.Empty(t, result.Results, "current topic metadata cannot bypass temporal Evidence eligibility")
	input.KnownAt = nil
	privateCtx := requestctx.WithActor(context.Background(), requestctx.Actor{TeamID: uuid.MustParse(f.team), OwnerID: uuid.MustParse(f.owners[2]), AllowedSpaces: []domain.MemorySpaceAccess{{ID: f.credentials[2].MemorySpaceID, Kind: domain.MemorySpacePrivate, Generation: 1}}})
	privateInput := input
	privateInput.SpaceID, privateInput.SpaceKind = f.credentials[2].MemorySpaceID.String(), "private"
	_, err = f.search.RecallEvidence(privateCtx, privateInput)
	require.ErrorIs(t, err, recallcontract.ErrTopicUnavailable)
	for _, unavailable := range []string{uuid.NewString(), first.ID} {
		input.TopicIDs = []string{unavailable}
		_, err = f.search.RecallEvidence(ctx, input)
		require.ErrorIs(t, err, recallcontract.ErrTopicUnavailable)
	}
	foreignTeam := createLedgerTeam(t, f.admin, f.rls, "foreign topic team")
	foreignOwner := createLedgerProfile(t, f.admin, f.rls, foreignTeam, "foreign topic manager")
	var foreignSpace string
	var foreignGeneration int64
	require.NoError(t, f.admin.Raw(`SELECT id::text,generation FROM memory_spaces WHERE team_id=?::uuid AND kind='team_shared'`, foreignTeam).Row().Scan(&foreignSpace, &foreignGeneration))
	foreignCtx := requestctx.WithActor(context.Background(), requestctx.Actor{TeamID: uuid.MustParse(foreignTeam), OwnerID: uuid.MustParse(foreignOwner), Role: "manager", Grants: []string{"read", "write"}, AllowedSpaces: []domain.MemorySpaceAccess{{ID: uuid.MustParse(foreignSpace), Kind: domain.MemorySpaceTeamShared, Generation: foreignGeneration}}})
	foreignTopic := ontology.Record{ID: uuid.NewString(), Kind: ontology.Topic, Definition: &ontology.Definition{Key: "foreign", Label: "Foreign"}}
	_, err = f.ontology.PublishManager(foreignCtx, foreignTeam, ontology.Publication{OperationKey: "foreign-topic", Reason: "Isolation fixture", Changes: []ontology.Change{{Record: foreignTopic}}})
	require.NoError(t, err)
	input.TopicIDs = []string{foreignTopic.ID}
	_, err = f.search.RecallEvidence(ctx, input)
	require.ErrorIs(t, err, recallcontract.ErrTopicUnavailable)
	parent.Version = 1
	parent.Definition.Description = "Changed current parent"
	f.publication(t, parent)
	input.TopicIDs = []string{child.ID}
	_, err = f.search.RecallEvidence(ctx, input)
	require.ErrorIs(t, err, recallcontract.ErrTopicUnavailable, "stale and missing topics share bounded public rejection")
	var graphObjects int64
	require.NoError(t, f.admin.Raw(`SELECT count(*) FROM pg_class WHERE relnamespace='public'::regnamespace AND relname IN ('entity_records','relationship_records','value_records','team_predicate_definitions','community_records')`).Scan(&graphObjects).Error)
	require.Zero(t, graphObjects)
}

type evidenceOnlyTopicConfig bool

func (c evidenceOnlyTopicConfig) OntologyMaintenanceRuntimeConfig(context.Context) (domain.OntologyMaintenanceConfig, error) {
	return domain.OntologyMaintenanceConfig{Enabled: bool(c)}, nil
}

func TestEvidenceOnlyPostgresTopicLogDiagnosis(t *testing.T) {
	f := newRecallOntologyFixture(t)
	const credential, presentedCredential = "topic-credential-canary", "topic-request-credential-canary"
	topic := ontology.Record{ID: uuid.NewString(), Kind: ontology.Topic, Definition: &ontology.Definition{Key: "diagnosis", Label: "Diagnosis", Description: "Topic diagnosis admitted detail. " + credential + " " + presentedCredential}}
	records := []ontology.Record{topic}
	evidenceIDs, assignmentIDs := []string{}, []string{}
	for index := range 60 {
		source := f.evidence(t, index%2, fmt.Sprintf("Topic diagnosis admitted Evidence %d.", index), nil)
		assignment := f.assignment(t, topic.ID, source)
		records = append(records, assignment)
		evidenceIDs, assignmentIDs = append(evidenceIDs, source.ID), append(assignmentIDs, assignment.ID)
	}
	f.publication(t, records...)
	path := filepath.Join(t.TempDir(), "topics.jsonl")
	output, err := os.Create(path)
	require.NoError(t, err)
	defer output.Close()
	previous := os.Stdout
	os.Stdout = output
	protector := observability.NewCredentialProtector(credential)
	logger := observability.NewWithProtector(slog.LevelDebug, protector)
	os.Stdout = previous
	f.search.recall.WithTopicDiagnostics(logger, protector)
	api := recall.NewRecallService(recall.RecallDependencies{Search: f.search.recall, OntologyConfig: evidenceOnlyTopicConfig(true), Logger: logger, DiagnosticProtector: protector})
	ctx := observability.WithAuthenticationSecrets(f.actor(2, "member"), presentedCredential)
	selected, err := api.Recall(ctx, recall.RecallRequest{TopicIDs: []string{topic.ID}})
	require.NoError(t, err)
	require.Len(t, selected.RelatedTopics, 1)
	require.Equal(t, 60, selected.RelatedTopics[0].EligibleEvidenceCount)
	missing := uuid.NewString()
	_, err = api.Recall(ctx, recall.RecallRequest{TopicIDs: []string{missing}})
	require.ErrorIs(t, err, recallcontract.ErrTopicUnavailable)
	f.search.recall.WithTopics(nil)
	degraded, err := api.Recall(ctx, recall.RecallRequest{Query: "diagnosis"})
	require.NoError(t, err)
	require.Contains(t, degraded.Degradations, recall.RecallDegradationResult{Frontier: "topics", Code: "topic_unavailable", Message: "Related topics are unavailable.", Optional: true})
	require.NoError(t, output.Close())
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NotContains(t, string(body), credential)
	require.NotContains(t, string(body), presentedCredential)
	summaries := []map[string]any{}
	captures := map[string][]observability.DiagnosticBodyChunk{}
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		var record struct {
			Message   string                             `json:"msg"`
			Level     string                             `json:"level"`
			TeamID    string                             `json:"team_id"`
			CaptureID string                             `json:"diagnostic_capture_id"`
			Chunk     *observability.DiagnosticBodyChunk `json:"payload_chunk"`
		}
		require.NoError(t, json.Unmarshal([]byte(line), &record))
		require.NotEqual(t, "TRACE", record.Level)
		require.NotEqual(t, "diagnostic_body_unavailable", record.Message)
		if record.Chunk != nil {
			require.Contains(t, []string{"recall_topic_filter_payload", "recall_topic_selection_payload"}, record.Message)
			require.Equal(t, "DEBUG", record.Level)
			require.Equal(t, f.team, record.TeamID)
			require.NotEmpty(t, record.CaptureID)
			captures[record.CaptureID] = append(captures[record.CaptureID], *record.Chunk)
		} else if record.Message == "recall_topic_selection" || record.Message == "recall_topic_degraded" {
			var summary map[string]any
			require.NoError(t, json.Unmarshal([]byte(line), &summary))
			summaries = append(summaries, summary)
		}
	}
	payloads := map[string][]byte{}
	for id, chunks := range captures {
		var reconstructed strings.Builder
		for index, chunk := range chunks {
			require.Equal(t, index, chunk.Index)
			require.Equal(t, len(chunks), chunk.Count)
			require.Equal(t, "captured", chunk.State)
			require.LessOrEqual(t, len(chunk.Body), 2048)
			require.Equal(t, chunks[0].SHA256, chunk.SHA256)
			reconstructed.WriteString(chunk.Body)
		}
		digest := sha256.Sum256([]byte(reconstructed.String()))
		require.Equal(t, hex.EncodeToString(digest[:]), chunks[0].SHA256)
		payloads[id] = []byte(reconstructed.String())
	}
	filter, hints, rejected, optional := false, false, false, false
	for _, record := range summaries {
		require.Equal(t, f.team, record["team_id"])
		captureID, ok := record["diagnostic_capture_id"].(string)
		require.True(t, ok)
		require.Contains(t, payloads, captureID)
		switch record["outcome"] {
		case "filter_selected":
			filter = true
			require.Equal(t, "DEBUG", record["level"])
			require.Equal(t, []any{topic.ID}, record["requested_ids"])
			require.Equal(t, float64(f.generation), record["generation"])
			var selection ontology.TopicReadResult
			require.NoError(t, json.Unmarshal(payloads[captureID], &selection))
			require.Len(t, selection.Topics, 1)
			require.ElementsMatch(t, evidenceIDs, selection.Topics[0].EvidenceIDs)
			require.ElementsMatch(t, assignmentIDs, selection.Topics[0].AssignmentIDs)
			require.Contains(t, selection.Topics[0].Description, "Topic diagnosis admitted detail.")
			require.Contains(t, selection.Topics[0].Description, observability.CredentialProtectionRedacted)
			require.Greater(t, len(payloads[captureID]), 4096)
		case "selected":
			hints = true
			require.Equal(t, "DEBUG", record["level"])
			require.Equal(t, selected.RecallID, record["recall_id"])
			var selection recallcontract.RelatedTopicResult
			require.NoError(t, json.Unmarshal(payloads[captureID], &selection))
			require.Equal(t, float64(f.generation), selection.Diagnostics["generation"])
			require.Len(t, selection.Topics, 1)
			require.Equal(t, 60, selection.Topics[0].EligibleEvidenceCount)
			counts := selection.Diagnostics["counts"].(map[string]any)[topic.ID].(map[string]any)
			require.Equal(t, float64(len(selected.Results)), counts["matching_result_count"])
			require.Equal(t, float64(60-len(selected.Results)), counts["additional_evidence_count"])
			require.Len(t, selection.Diagnostics["eligible_ids"], 60)
			memberships := selection.Diagnostics["memberships"].([]any)
			require.Len(t, memberships, 1)
			require.Len(t, memberships[0].(map[string]any)["EvidenceIDs"], 60)
			require.Greater(t, len(payloads[captureID]), 4096)
		case "unavailable":
			require.Equal(t, "WARN", record["level"])
			require.NotEmpty(t, record["reason"])
			if record["recall_id"] == degraded.RecallID {
				optional = true
			} else if record["requested_ids"] != nil {
				rejected = true
				var selection ontology.TopicReadResult
				require.NoError(t, json.Unmarshal(payloads[captureID], &selection))
				require.Equal(t, "unavailable", selection.Rejected[missing])
			}
		}
	}
	require.True(t, filter)
	require.True(t, hints)
	require.True(t, rejected)
	require.True(t, optional)
}

func TestEvidenceOnlyPostgresTopicFiltersRespectRuntimeDisable(t *testing.T) {
	f := newRecallOntologyFixture(t)
	svc := recall.NewRecallService(recall.RecallDependencies{Search: f.search.recall, OntologyConfig: evidenceOnlyTopicConfig(false)})
	_, err := svc.Recall(f.actor(2, "member"), recall.RecallRequest{TopicIDs: []string{uuid.NewString()}})
	require.ErrorIs(t, err, recallcontract.ErrTopicUnavailable)
	result, err := svc.Recall(f.actor(2, "member"), recall.RecallRequest{Query: "ordinary Evidence"})
	require.NoError(t, err)
	require.Empty(t, result.RelatedTopics)
	for _, degradation := range result.Degradations {
		require.NotEqual(t, "topic_unavailable", degradation.Code)
	}
}
