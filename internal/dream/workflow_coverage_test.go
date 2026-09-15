package dream

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	dreamcontract "github.com/markhuangai/dense-mem/internal/dream/contract"
)

func TestWorkflowHelpersCoverNilAndOptionalBranches(t *testing.T) {
	applyDreamGenerationDiagnostics(nil, dreamGenerationResult{}, 0, 0, 0)
	result := &RunCycleResult{}
	applyDreamGenerationDiagnostics(result, dreamGenerationResult{
		model: "model", candidatePaths: 2, candidateTargets: 4, availableTargets: 1,
		targetLookupFailed: true, pathAssessmentLookupFailed: true, providerFailed: true,
		providerProposals: 3, rejected: 1, persistencePolicyRejected: 2,
		paths: []DreamPath{{PathRef: "path"}},
	}, 2, 1, 1)
	require.Equal(t, 0, result.OutcomeSummary["blocked_targets"])
	require.Equal(t, 1, result.OutcomeSummary["target_lookup_error"])

	if _, err := normalizeDreamTeamID("invalid"); err == nil {
		t.Fatal("invalid team id was accepted")
	}
	teamID := uuid.NewString()
	if normalized, err := normalizeDreamTeamID("  " + teamID + " "); err != nil || normalized != teamID {
		t.Fatalf("normalized team id = %q, %v", normalized, err)
	}
	if got := translateDreamRepositoryError(dreamcontract.ErrTeamInactive); got == nil || !strings.Contains(got.Error(), "team not found") {
		t.Fatalf("translated inactive error = %v", got)
	}
	plainErr := errors.New("plain")
	if translateDreamRepositoryError(plainErr) != plainErr {
		t.Fatal("plain repository error was replaced")
	}

	if _, _, err := dreamActor(context.Background()); !errors.Is(err, ErrDreamAuthContext) {
		t.Fatalf("anonymous dream actor error = %v", err)
	}
	actorCtx := dreamTestContext(uuid.New(), uuid.New())
	if team, owner, err := dreamActor(actorCtx); err != nil || team == "" || owner == "" {
		t.Fatalf("dream actor = %q/%q, %v", team, owner, err)
	}

	input := dreamcontract.DreamInput{RelationshipID: "relationship", Version: 3, Status: "active"}
	snapshot := dreamInputSnapshot([]dreamcontract.DreamInput{input})
	require.Equal(t, "relationship", snapshot[0]["relationship_id"])
	require.Nil(t, dreamRecord(nil))
	require.Empty(t, dreamRecords(nil))

	completed := time.Date(2026, 7, 17, 4, 0, 0, 0, time.UTC)
	run := cycleRunResult(&dreamcontract.DreamCycleRun{RunID: "run", CompletedAt: &completed, ScheduledFor: &completed, OutcomeSummary: map[string]int{"created": 1}})
	require.Equal(t, completed, run.CompletedAt)
	require.Equal(t, completed, run.ScheduledFor)
	require.Nil(t, cycleRunResult(nil))
	require.Nil(t, copyDreamOutcomeSummary(nil))
	require.Equal(t, map[string]int{"created": 1}, copyDreamOutcomeSummary(map[string]int{"created": 1}))
	require.Equal(t, "name", dreamDisplay(" name ", "entity"))
	require.Equal(t, "unnamed entity", dreamDisplay("", "entity"))
	require.Equal(t, "unnamed node", dreamDisplay("", ""))
	require.Equal(t, "second", firstNonEmpty(" ", "second"))
	require.Empty(t, firstNonEmpty(" ", "\t"))

	refs := []map[string]any{{"type": "relationship", "id": "active"}, {"type": "candidate_relationship", "id": "candidate"}, {"type": "relationship", "id": ""}}
	require.Equal(t, []string{"active"}, dreamSourceIDs(refs, false))
	require.Equal(t, []string{"candidate"}, dreamSourceIDs(refs, true))
	require.Len(t, dreamSourceRefs(append(refs, map[string]any{"type": "", "id": "ignored"})), 2)
	versions := copyDreamSourceVersions(map[string]int{"source": 2})
	versions["source"] = 3
	require.Equal(t, 2, map[string]int{"source": 2}["source"], "copy must not alias input")

	if got := optionalProbability(-1); got != nil {
		t.Fatal("negative probability was retained")
	}
	if got := optionalProbability(2); got == nil || *got != 1 {
		t.Fatalf("bounded probability = %v", got)
	}
	if got := floatPtrValue(nil); got != 0 {
		t.Fatalf("nil float pointer = %v", got)
	}
}

func TestWorkflowReadAndGenerationErrorBranches(t *testing.T) {
	teamID := uuid.New()
	ownerID := uuid.New()
	ctx := dreamTestContext(teamID, ownerID)
	repo := &dreamRepositoryStub{}
	svc := New(Dependencies{Store: repo, AppConfig: cycleAppConfigStub{cfg: dueSchedulerConfig().DreamingRuntimeConfig}}).(*service)

	if _, err := svc.runClaimedTeamCycle(ctx, teamID.String(), ownerID.String(), EffectiveConfig{}, RunCycleRequest{}, false, &RunCycleResult{}, nil); err == nil {
		t.Fatal("nil durable claim was accepted")
	}
	if created, rejected, _, err := svc.persistHypotheses(ctx, teamID.String(), ownerID.String(), "run", "lease", nil, []SeedDream{{Hypothesis: "ignored"}}, 0, false); err != nil || created != 0 || rejected != 0 {
		t.Fatalf("seed without a source result = %d/%d, %v", created, rejected, err)
	}

	repo.getErr = dreamcontract.ErrDreamHypothesisNotFound
	if _, err := svc.Get(ctx, teamID.String(), "missing"); !errors.Is(err, ErrDreamNotFound) {
		t.Fatalf("missing dream error = %v", err)
	}
	repo.getErr = errors.New("get failed")
	if _, err := svc.Get(ctx, teamID.String(), "dream"); err == nil {
		t.Fatal("get repository error was ignored")
	}
	repo.getErr = nil
	repo.listErr = errors.New("list failed")
	if _, _, err := svc.List(ctx, teamID.String(), ListOptions{}); err == nil {
		t.Fatal("list repository error was ignored")
	}
	repo.listErr = nil
	repo.latestErr = errors.New("runs failed")
	if _, err := svc.ListRuns(ctx, teamID.String(), 1); err == nil {
		t.Fatal("runs repository error was ignored")
	}
	repo.latestErr = nil
	repo.recallErr = errors.New("recall failed")
	if _, err := svc.Recall(ctx, teamID.String(), "query", 1); err == nil {
		t.Fatal("recall repository error was ignored")
	}

	if _, err := svc.ResolveFeedback(ctx, teamID.String(), ResolveFeedbackRequest{}); err == nil {
		t.Fatal("feedback without a dream id was accepted")
	}
	repo.getErr = dreamcontract.ErrDreamHypothesisNotFound
	if _, err := svc.ResolveFeedback(ctx, teamID.String(), ResolveFeedbackRequest{DreamID: "missing", Decision: "ignore"}); err == nil {
		t.Fatal("feedback missing dream was accepted")
	}
	repo.getErr = errors.New("feedback lookup failed")
	if _, err := svc.ResolveFeedback(ctx, teamID.String(), ResolveFeedbackRequest{DreamID: "dream", Decision: "ignore"}); err == nil {
		t.Fatal("feedback lookup error was ignored")
	}

	repo.getErr = nil
	repo.err = errors.New("status failed")
	if _, err := svc.Status(ctx, teamID.String()); err == nil {
		t.Fatal("status repository error was ignored")
	}
}

func TestWorkflowGenerationFailureBranches(t *testing.T) {
	inputs := testDreamPathInputs()
	teamID := uuid.NewString()
	serviceWith := func(repo *dreamRepositoryStub, generator Generator) *service {
		return New(Dependencies{Store: repo, Generator: generator}).(*service)
	}

	if _, err := serviceWith(&dreamRepositoryStub{err: errors.New("predicate failed")}, &dreamGeneratorStub{}).generateDreamProposals(context.Background(), teamID, inputs, 1); err == nil {
		t.Fatal("predicate lookup error was ignored")
	}
	if _, err := serviceWith(&dreamRepositoryStub{predicates: testDreamPathPredicates()}, emptyModelGenerator{}).generateDreamProposals(context.Background(), teamID, inputs, 1); !errors.Is(err, ErrDreamProviderUnavailable) {
		t.Fatalf("empty model error = %v", err)
	}
	providerErr := errors.New("provider failed")
	if _, err := serviceWith(&dreamRepositoryStub{predicates: testDreamPathPredicates()}, &dreamGeneratorStub{err: providerErr}).generateDreamProposals(context.Background(), teamID, inputs, 1); !errors.Is(err, providerErr) {
		t.Fatalf("provider error = %v", err)
	}

	repo := &dreamRepositoryStub{}
	svc := serviceWith(repo, &dreamGeneratorStub{})
	if created, rejected, _, err := svc.persistHypotheses(context.Background(), teamID, "owner", "run", "lease", inputs, []SeedDream{{Hypothesis: "seed", SourceRefs: []domain.DreamSourceRef{{Type: "relationship", ID: inputs[0].RelationshipID}}}}, 1, false); err != nil || created != 1 || rejected != 0 {
		t.Fatalf("seed persistence result = %d/%d, %v", created, rejected, err)
	}
	repo = &dreamRepositoryStub{upsertErr: errors.New("upsert failed")}
	if _, _, _, err := serviceWith(repo, &dreamGeneratorStub{}).persistHypotheses(context.Background(), teamID, "owner", "run", "lease", inputs, []SeedDream{{Hypothesis: "seed", SourceRefs: []domain.DreamSourceRef{{Type: "relationship", ID: inputs[0].RelationshipID}}}}, 1, false); err == nil {
		t.Fatal("upsert error was ignored")
	}
}

type emptyModelGenerator struct{}

func (emptyModelGenerator) Generate(context.Context, string, GenerateRequest) ([]GeneratedDream, error) {
	return nil, nil
}

func (emptyModelGenerator) Model() string { return "" }
