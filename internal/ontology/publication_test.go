package ontology

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/ontology/assessment"
	contract "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	"github.com/stretchr/testify/require"
)

func unitSource(key, text string) contract.SourceSnapshot {
	return contract.SourceSnapshot{SourceHandle: contract.SourceHandle{Kind: contract.EvidenceSource, ID: uuid.NewSHA1(uuid.NameSpaceURL, []byte(key)).String(), Version: 1}, TeamID: uuid.NewString(), SpaceID: uuid.NewString(), Generation: 1, OwnerID: "owner", Eligible: true, State: map[string]string{"content": text, "metadata": "{}"}, MeaningKey: key}
}

func unitReceipt(t *testing.T, sources []contract.SourceSnapshot) contract.OrganizationReceipt {
	t.Helper()
	receipt := contract.OrganizationReceipt{ID: uuid.NewString(), OperationKey: "unit"}
	for _, source := range sources {
		fingerprint, err := contract.SourceFingerprint(source)
		require.NoError(t, err)
		receipt.Sources = append(receipt.Sources, contract.SourceDependency{SourceHandle: source.SourceHandle, Fingerprint: fingerprint})
		receipt.Result.Outcomes = append(receipt.Result.Outcomes, contract.OrganizationOutcome{Source: source.SourceHandle, Status: "unchanged"})
	}
	return receipt
}

func TestOrganizationRequestUsesBoundedContextAndManagerRules(t *testing.T) {
	a, b := unitSource("a", "Atlas uses PostgreSQL."), unitSource("b", "Atlas uses PostgreSQL.")
	topic := contract.Record{ID: uuid.NewString(), Version: 1, Kind: contract.Topic, Definition: &contract.Definition{Key: "postgresql", Label: "PostgreSQL"}}
	separate := contract.Record{ID: uuid.NewString(), Kind: contract.OverrideKind, Override: &contract.Override{Action: contract.KeepSeparate, Members: []contract.SourceHandle{a.SourceHandle, b.SourceHandle}}}
	pin := contract.Record{ID: uuid.NewString(), Kind: contract.OverrideKind, Override: &contract.Override{Action: contract.SetClassification, DefinitionID: topic.ID, Members: []contract.SourceHandle{a.SourceHandle}}}
	ctx := contract.OrganizationContext{Sources: []contract.SourceSnapshot{a, b}, Records: []contract.RecordView{{Record: separate, Current: true}, {Record: pin, Current: true}}, Candidates: []contract.RecordView{{Record: topic, Current: true}}}
	service := NewService(nil, assessment.NewProvider(nil, "model", assessor.DefaultSemanticAssessmentLimits()))
	receipt := unitReceipt(t, ctx.Sources)
	request, binding, err := service.request(ctx, &receipt)
	require.NoError(t, err)
	require.Len(t, request.Items, 2)
	require.Len(t, request.Definitions, 1)
	require.Len(t, binding.sources, 2)
	require.Equal(t, "d0", request.Items[0].LockedDefinitionRef)
	require.Equal(t, "distinct", request.Pairs[0].RequiredRelation)
	ctx.Candidates = nil
	receipt = unitReceipt(t, ctx.Sources)
	request, _, err = service.request(ctx, &receipt)
	require.NoError(t, err)
	require.Len(t, request.Items, 1)
	require.Equal(t, "required_classification_unavailable", receipt.Result.Outcomes[0].Reason)
	ctx.Records = []contract.RecordView{{Record: contract.Record{ID: uuid.NewString(), Kind: contract.EvidenceGroup, Group: &contract.Group{Members: []contract.SourceHandle{a.SourceHandle, unitSource("outside", "outside").SourceHandle}}}}}
	receipt = unitReceipt(t, ctx.Sources)
	request, _, err = service.request(ctx, &receipt)
	require.NoError(t, err)
	require.Len(t, request.Items, 1)
	require.Equal(t, "resubmit_complete_group", receipt.Result.Outcomes[0].Reason)
	ctx.Records = nil
	ctx.Sources[0].Eligible = false
	receipt = unitReceipt(t, ctx.Sources)
	_, _, err = service.request(ctx, &receipt)
	require.NoError(t, err)
	require.Equal(t, "unavailable", receipt.Result.Outcomes[0].Status)
	ctx.Sources[0].Eligible = true
	ctx.Sources[0].State["content"] = strings.Repeat("oversized evidence ", 20000)
	limits := assessor.DefaultSemanticAssessmentLimits()
	limits.MaxInputTokens = 10000
	service = NewService(nil, assessment.NewProvider(nil, "model", limits))
	receipt = unitReceipt(t, ctx.Sources)
	request, _, err = service.request(ctx, &receipt)
	require.NoError(t, err)
	require.Len(t, request.Items, 1)
	require.Equal(t, "oversized", receipt.Result.Outcomes[0].Status)
}

func TestOrganizationPublicationPreservesOriginalSourcesAndStableIDs(t *testing.T) {
	a, b := unitSource("a", "Atlas stores data in PostgreSQL."), unitSource("b", "PostgreSQL is Atlas's data store.")
	ctx := contract.OrganizationContext{Sources: []contract.SourceSnapshot{a, b}}
	service := NewService(nil, assessment.NewProvider(nil, "model", assessor.DefaultSemanticAssessmentLimits()))
	receipt := unitReceipt(t, ctx.Sources)
	request, binding, err := service.request(ctx, &receipt)
	require.NoError(t, err)
	response := assessment.Response{RequestID: request.RequestID, Definitions: []assessment.Definition{{Ref: "new", Kind: contract.Topic, Key: "storage", Label: "Storage", Aliases: []string{}}}, Items: []assessment.Decision{{Ref: "s0", Status: "classified", DefinitionRef: "new"}, {Ref: "s1", Status: "classified", DefinitionRef: "new"}}, Equivalence: []assessment.Equivalence{{Ref: "p0", Relation: "equivalent"}}}
	require.NoError(t, assessment.Validate(request, response))
	publication, err := buildPublication(a.TeamID, ctx, request, response, binding, &receipt)
	require.NoError(t, err)
	require.Len(t, publication.Changes, 4)
	snapshots := map[string]contract.SourceSnapshot{contract.SourceKey(a.SourceHandle): a, contract.SourceKey(b.SourceHandle): b}
	prepared, err := contract.PreparePublication(nil, snapshots, publication, true)
	require.NoError(t, err)
	var group contract.Record
	for _, record := range prepared {
		ctx.Records = append(ctx.Records, contract.RecordView{Record: record, Current: true})
		if record.Group != nil {
			group = record
		}
	}
	require.ElementsMatch(t, []contract.SourceHandle{a.SourceHandle, b.SourceHandle}, group.Group.Members)
	require.Equal(t, receipt.ID, group.Group.AssessmentID)
	for _, outcome := range receipt.Result.Outcomes {
		require.Equal(t, "organized", outcome.Status)
		require.Len(t, outcome.RecordIDs, 2)
	}
	ctx.Revision = 1
	ctx.Candidates = []contract.RecordView{}
	for _, view := range ctx.Records {
		if view.Definition != nil {
			ctx.Candidates = append(ctx.Candidates, view)
		}
	}
	receipt = unitReceipt(t, ctx.Sources)
	request, binding, err = service.request(ctx, &receipt)
	require.NoError(t, err)
	response = assessment.Response{RequestID: request.RequestID, Definitions: []assessment.Definition{}, Items: []assessment.Decision{{Ref: "s0", Status: "classified", DefinitionRef: "d0"}, {Ref: "s1", Status: "classified", DefinitionRef: "d0"}}, Equivalence: []assessment.Equivalence{{Ref: "p0", Relation: "equivalent"}}}
	publication, err = buildPublication(a.TeamID, ctx, request, response, binding, &receipt)
	require.NoError(t, err)
	require.Empty(t, publication.Changes)
}

func TestOrganizationRejectsRequestAuthorityAndMissingConfiguration(t *testing.T) {
	service := NewService(nil, nil)
	_, err := service.Organize(requestctx.WithActor(context.Background(), requestctx.Actor{}), uuid.NewString(), contract.OrganizationInput{})
	require.ErrorIs(t, err, contract.ErrUnauthorized)
	_, err = service.Organize(context.Background(), uuid.NewString(), contract.OrganizationInput{})
	var organizationError *OrganizationError
	require.ErrorAs(t, err, &organizationError)
	require.Equal(t, "configuration_invalid", organizationError.Code)
	receipt := unitReceipt(t, []contract.SourceSnapshot{unitSource("a", "a")})
	markFailure(&receipt, contract.ErrConflict)
	require.Equal(t, "commit_conflict", receipt.Result.FailureCode)
	require.Equal(t, "failed", receipt.Result.Outcomes[0].Status)
}
