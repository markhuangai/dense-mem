package ontology

import (
	"context"
	"encoding/json"
	"fmt"
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
	ctx.Records = []contract.RecordView{{Record: contract.Record{ID: uuid.NewString(), Kind: contract.EvidenceGroup, Group: &contract.Group{Members: []contract.SourceHandle{a.SourceHandle, unitSource("outside", "outside").SourceHandle}}}, Current: true}}
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
	limits.MaxInputTokens = 20000
	limits.MaxOutputTokens = 1024
	service = NewService(nil, assessment.NewProvider(nil, "model", limits))
	receipt = unitReceipt(t, ctx.Sources)
	request, _, err = service.request(ctx, &receipt)
	require.NoError(t, err)
	require.Len(t, request.Items, 1)
	require.Equal(t, "oversized", receipt.Result.Outcomes[0].Status)
}

func TestIncompleteGroupPreservesPersistentOverrides(t *testing.T) {
	a, b := unitSource("a", "Atlas uses PostgreSQL."), unitSource("b", "Atlas uses PostgreSQL.")
	group := contract.Record{ID: uuid.NewString(), Kind: contract.EvidenceGroup, Group: &contract.Group{Members: []contract.SourceHandle{a.SourceHandle, b.SourceHandle}}}
	keys := map[string]bool{contract.SourceKey(a.SourceHandle): true}
	require.False(t, incompleteGroup(a, []contract.RecordView{{Record: group}}, keys))
	require.True(t, incompleteGroup(a, []contract.RecordView{{Record: group, Current: true}}, keys))
	override := contract.Record{ID: uuid.NewString(), Kind: contract.OverrideKind, Override: &contract.Override{Action: contract.GroupTogether, Members: group.Group.Members}}
	require.True(t, incompleteGroup(a, []contract.RecordView{{Record: override}}, keys))
}

func TestOrganizationRejectsConflictingDefinitionIdentities(t *testing.T) {
	source := unitSource("source", "Atlas uses PostgreSQL.")
	a := contract.Record{ID: contract.OrganizationRecordID(source.TeamID, contract.Topic, "alpha"), Kind: contract.Topic, Definition: &contract.Definition{Key: "alpha", Label: "Alpha"}}
	b := contract.Record{ID: uuid.NewString(), Kind: contract.Topic, Definition: &contract.Definition{Key: "beta", Label: "Beta"}}
	for _, views := range [][]contract.RecordView{
		{{Record: a, Current: true}, {Record: b, Current: true}},
		{{Record: contract.Record{ID: a.ID, Kind: contract.Topic, Definition: &contract.Definition{Key: "unrelated", Label: "Unrelated"}}, Current: true}},
	} {
		ctx := contract.OrganizationContext{Sources: []contract.SourceSnapshot{source}, Records: views}
		receipt := unitReceipt(t, ctx.Sources)
		request, binding, err := NewService(nil, assessment.NewProvider(nil, "model", assessor.DefaultSemanticAssessmentLimits())).request(ctx, &receipt)
		require.NoError(t, err)
		response := assessment.Response{RequestID: request.RequestID, Definitions: []assessment.Definition{{Ref: "new", Kind: contract.Topic, Key: "alpha", Label: "Beta", Aliases: []string{}}}, Items: []assessment.Decision{{Ref: "s0", Status: "classified", DefinitionRef: "new"}}, Equivalence: []assessment.Equivalence{}}
		require.NoError(t, assessment.Validate(request, response))
		publication, err := buildPublication(source.TeamID, ctx, request, response, binding, &receipt)
		require.ErrorIs(t, err, contract.ErrConflict)
		require.Empty(t, publication.Changes)
	}
}

func TestOrganizationRequestTrimsParentRefsWithoutChangingStoredHierarchy(t *testing.T) {
	source := unitSource("child", "Atlas uses PostgreSQL.")
	parent := contract.Record{ID: uuid.NewString(), Version: 1, Kind: contract.Topic, Definition: &contract.Definition{Key: "storage", Label: "Storage"}}
	child := contract.Record{ID: uuid.NewString(), Version: 1, Kind: contract.Topic, Definition: &contract.Definition{Key: "postgresql", Label: "PostgreSQL", ParentID: parent.ID}}
	assignment := contract.Record{ID: uuid.NewString(), Kind: contract.AssignmentKind, Assignment: &contract.Assignment{Source: source.SourceHandle, DefinitionID: child.ID}, Sources: unitReceipt(t, []contract.SourceSnapshot{source}).Sources}
	ctx := contract.OrganizationContext{Sources: []contract.SourceSnapshot{source}, Records: []contract.RecordView{{Record: assignment, Current: true}}, Candidates: []contract.RecordView{{Record: child, Current: true}, {Record: parent, Current: true}}}
	for _, trim := range []bool{false, true} {
		t.Run(fmt.Sprintf("trim=%t", trim), func(t *testing.T) {
			limits := assessor.DefaultSemanticAssessmentLimits()
			if trim {
				encoded, err := json.Marshal([]assessment.Definition{{Ref: "d0", Kind: child.Kind, Key: child.Definition.Key, Label: child.Definition.Label, Aliases: []string{}, ParentRef: "d1"}})
				require.NoError(t, err)
				limits.MaxCandidateContextTokens, err = assessor.CountTokens(string(encoded), limits.Tokenizer)
				require.NoError(t, err)
			}
			service := NewService(nil, assessment.NewProvider(nil, "model", limits))
			receipt := unitReceipt(t, ctx.Sources)
			request, binding, err := service.request(ctx, &receipt)
			require.NoError(t, err)
			require.Len(t, request.Items, 1)
			require.Equal(t, "d0", request.Items[0].LockedDefinitionRef)
			if trim {
				require.Len(t, request.Definitions, 1)
				require.Empty(t, request.Definitions[0].ParentRef)
			} else {
				require.Len(t, request.Definitions, 2)
				require.Equal(t, "d1", request.Definitions[0].ParentRef)
			}
			require.Equal(t, parent.ID, binding.definitions["d0"].Definition.ParentID)
			response := assessment.Response{RequestID: request.RequestID, Definitions: []assessment.Definition{}, Items: []assessment.Decision{{Ref: "s0", Status: "classified", DefinitionRef: "d0"}}, Equivalence: []assessment.Equivalence{}}
			require.NoError(t, assessment.Validate(request, response))
			publication, err := buildPublication(source.TeamID, ctx, request, response, binding, &receipt)
			require.NoError(t, err)
			require.Empty(t, publication.Changes)
		})
	}
	require.Equal(t, parent.ID, child.Definition.ParentID)
}

func TestOrganizationRequestReservesRepairBudgetForAdmittedSources(t *testing.T) {
	ctx := contract.OrganizationContext{Sources: []contract.SourceSnapshot{unitSource("large", strings.Repeat("large evidence ", 6000)), unitSource("small", "Atlas uses PostgreSQL.")}}
	limits := assessor.DefaultSemanticAssessmentLimits()
	initialProvider := assessment.NewProvider(nil, "model", limits)
	receipt := unitReceipt(t, ctx.Sources)
	full, _, err := NewService(nil, initialProvider).request(ctx, &receipt)
	require.NoError(t, err)
	measurement, err := initialProvider.Measure(full)
	require.NoError(t, err)
	limits.MaxInputTokens = measurement + 50
	limits.MaxOutputTokens = 1024
	provider := assessment.NewProvider(nil, "model", limits)
	receipt = unitReceipt(t, ctx.Sources)
	request, _, err := NewService(nil, provider).request(ctx, &receipt)
	require.NoError(t, err)
	require.Len(t, request.Items, 1)
	require.Equal(t, "s1", request.Items[0].Ref)
	require.Equal(t, "oversized", receipt.Result.Outcomes[0].Status)
	require.Equal(t, "organization_input_budget", receipt.Result.Outcomes[0].Reason)
	require.Equal(t, "unchanged", receipt.Result.Outcomes[1].Status)
	admitted, err := provider.Measure(request)
	require.NoError(t, err)
	require.LessOrEqual(t, admitted, provider.MaxInitialInputTokens())
}

func TestOrganizationFailurePreservesOnlyPreflightAmbiguity(t *testing.T) {
	receipt := unitReceipt(t, []contract.SourceSnapshot{unitSource("excluded-group", "a"), unitSource("excluded-class", "b"), unitSource("assessed", "c")})
	receipt.Result.Outcomes[0].Status, receipt.Result.Outcomes[0].Reason = "ambiguous", "resubmit_complete_group"
	receipt.Result.Outcomes[1].Status, receipt.Result.Outcomes[1].Reason = "ambiguous", "required_classification_unavailable"
	receipt.Result.Outcomes[2].Status, receipt.Result.Outcomes[2].Reason = "ambiguous", "classification_ambiguous"
	receipt.Result.Outcomes[2].RecordIDs = []string{uuid.NewString()}
	markFailure(&receipt, contract.ErrSourceStale)
	require.Equal(t, "stale_input", receipt.Result.FailureCode)
	for _, index := range []int{0, 1} {
		require.Equal(t, "ambiguous", receipt.Result.Outcomes[index].Status)
	}
	require.Equal(t, "resubmit_complete_group", receipt.Result.Outcomes[0].Reason)
	require.Equal(t, "required_classification_unavailable", receipt.Result.Outcomes[1].Reason)
	require.Equal(t, "failed", receipt.Result.Outcomes[2].Status)
	require.Equal(t, "stale_input", receipt.Result.Outcomes[2].Reason)
	require.Empty(t, receipt.Result.Outcomes[2].RecordIDs)
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
