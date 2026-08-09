package observability

import (
	"strings"
	"sync"
)

// DiscoverabilityMetrics is the concurrency-safe metrics contract used by
// active memory services.
type DiscoverabilityMetrics interface {
	ObserveEmbeddingLatency(durationMs float64, outcome string)
	IncEmbeddingError(code string)
	ObserveRecallLatency(durationMs float64)
	ObserveRecall(durationMs float64, resultCount int, outcome string)
	ObserveRecallFeedback(feedback RecallFeedback)
	ObserveDreamFeedback(feedback DreamFeedback)
	ObserveConflictReviewDuration(seconds float64, outcome string)
	IncVerifyVerdict(outcome string)
}

// EmbeddingReconciliationMetrics records bounded-cardinality recovery
// progress. It is optional so existing metrics implementations remain valid.
type EmbeddingReconciliationMetrics interface {
	ObserveEmbeddingReconciliationRun(outcome string)
	ObserveEmbeddingReconciliationCanary(outcome string)
	ObserveEmbeddingReconciliationJobs(action, sourceKind, failureClass, failureCode string, count int)
	ObserveEmbeddingReconciliationDuration(seconds float64, outcome string)
}

// AssessorMetrics is an optional V2.4 write-path metric surface. Its methods
// deliberately accept no request identity, evidence, threshold, or rationale
// so assessor telemetry cannot create sensitive or high-cardinality labels.
type AssessorMetrics interface {
	ObserveAssessorCall(inputTokens, outputTokens int, durationSeconds float64, outcome string)
	IncAssessorValidationFailure(stage string)
	IncAssessorValidationFieldFailure(stage, family string)
	IncAssessorCandidateTruncation()
	IncAssessorAssessmentPersistence(outcome string)
	IncAssessorDuplicateRequestPrevention(stage string)
	IncAssessorConfidenceGate(band string)
	AddAssessorReviewExpiry(count int64)
	IncAssessorTerminalFailure(stage string)
}

// SubmissionQuarantineMetrics records required retention-worker failures.
type SubmissionQuarantineMetrics interface {
	IncSubmissionQuarantinePurgeFailure()
}

// NoopDiscoverabilityMetrics returns a recorder that discards all metrics.
func NoopDiscoverabilityMetrics() DiscoverabilityMetrics { return noopMetrics{} }

type noopMetrics struct{}

var _ DiscoverabilityMetrics = noopMetrics{}

func (noopMetrics) ObserveEmbeddingLatency(float64, string) {}
func (noopMetrics) IncEmbeddingError(string)                {}
func (noopMetrics) ObserveRecallLatency(float64)            {}
func (noopMetrics) ObserveRecall(float64, int, string)      {}
func (noopMetrics) ObserveRecallFeedback(RecallFeedback)    {}
func (noopMetrics) ObserveDreamFeedback(DreamFeedback)      {}
func (noopMetrics) ObserveConflictReviewDuration(float64, string) {
}
func (noopMetrics) IncVerifyVerdict(string)                       {}
func (noopMetrics) ObserveAssessorCall(int, int, float64, string) {}
func (noopMetrics) IncAssessorValidationFailure(string)           {}
func (noopMetrics) IncAssessorValidationFieldFailure(string, string) {
}
func (noopMetrics) IncAssessorCandidateTruncation()              {}
func (noopMetrics) IncAssessorAssessmentPersistence(string)      {}
func (noopMetrics) IncAssessorDuplicateRequestPrevention(string) {}
func (noopMetrics) IncAssessorConfidenceGate(string)             {}
func (noopMetrics) AddAssessorReviewExpiry(int64)                {}
func (noopMetrics) IncAssessorTerminalFailure(string)            {}
func (noopMetrics) IncSubmissionQuarantinePurgeFailure()         {}

// InMemoryDiscoverabilityMetrics is a test-friendly recorder.
type InMemoryDiscoverabilityMetrics struct {
	mu                          sync.Mutex
	embeddingSamples            []EmbeddingSample
	embeddingErrors             map[string]int
	recallLatencies             []float64
	recallSamples               []RecallSample
	recallFeedbackSamples       []RecallFeedbackSample
	dreamFeedbackSamples        []DreamFeedbackSample
	conflictReviewSamples       []ConflictReviewSample
	verifyVerdicts              map[string]int
	assessorCalls               []AssessorCallSample
	assessorValidation          map[string]int
	assessorValidationFields    map[assessorValidationFieldKey]int
	assessorTruncations         int
	assessorPersistence         map[string]int
	assessorDuplicatePrevention map[string]int
	assessorGateBands           map[string]int
	assessorReviewExpiry        int64
	assessorTerminalFailures    map[string]int
	quarantinePurgeFailures     int
	reconciliationRuns          map[string]int
	reconciliationCanaries      map[string]int
	reconciliationJobs          map[string]int
	reconciliationDurations     []float64
}

// AssessorCallSample records one bounded assessor conversation.
type AssessorCallSample struct {
	InputTokens     int
	OutputTokens    int
	DurationSeconds float64
	Outcome         string
}

type assessorValidationFieldKey struct {
	stage  string
	family string
}

// EmbeddingSample is one recorded embedding latency observation.
type EmbeddingSample struct {
	DurationMs float64
	Outcome    string
}

// RecallSample is one recorded recall event.
type RecallSample struct {
	DurationMs  float64
	ResultCount int
	Outcome     string
}

// RecallFeedback is one host-LLM online quality judgment for a recall result set.
type RecallFeedback struct {
	Used            bool
	AnswerSupported bool
	Quality         string
	MissingContext  bool
	Irrelevant      bool
}

// RecallFeedbackSample is one recorded online recall-feedback event.
type RecallFeedbackSample struct {
	Used            bool
	AnswerSupported bool
	Quality         string
	QualityScore    float64
	MissingContext  bool
	Irrelevant      bool
}

// DreamFeedback is one bounded dream-feedback decision.
type DreamFeedback struct {
	Decision   string
	Outcome    string
	FromStatus string
}

// DreamFeedbackSample is one recorded dream-feedback decision.
type DreamFeedbackSample struct {
	Decision   string
	Outcome    string
	FromStatus string
}

// ConflictReviewSample is one conflict-review duration observation.
type ConflictReviewSample struct {
	Seconds float64
	Outcome string
}

var _ DiscoverabilityMetrics = (*InMemoryDiscoverabilityMetrics)(nil)
var _ AssessorMetrics = (*InMemoryDiscoverabilityMetrics)(nil)
var _ SubmissionQuarantineMetrics = (*InMemoryDiscoverabilityMetrics)(nil)

// NewInMemoryDiscoverabilityMetrics constructs a fresh recorder.
func NewInMemoryDiscoverabilityMetrics() *InMemoryDiscoverabilityMetrics {
	return &InMemoryDiscoverabilityMetrics{
		embeddingErrors:             make(map[string]int),
		verifyVerdicts:              make(map[string]int),
		assessorValidation:          make(map[string]int),
		assessorValidationFields:    make(map[assessorValidationFieldKey]int),
		assessorPersistence:         make(map[string]int),
		assessorDuplicatePrevention: make(map[string]int),
		assessorGateBands:           make(map[string]int),
		assessorTerminalFailures:    make(map[string]int),
		reconciliationRuns:          make(map[string]int),
		reconciliationCanaries:      make(map[string]int),
		reconciliationJobs:          make(map[string]int),
	}
}

func (m *InMemoryDiscoverabilityMetrics) ObserveEmbeddingLatency(durationMs float64, outcome string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.embeddingSamples = append(m.embeddingSamples, EmbeddingSample{DurationMs: durationMs, Outcome: outcome})
}

func (m *InMemoryDiscoverabilityMetrics) IncEmbeddingError(code string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.embeddingErrors[code]++
}

func (m *InMemoryDiscoverabilityMetrics) ObserveRecallLatency(durationMs float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recallLatencies = append(m.recallLatencies, durationMs)
}

func (m *InMemoryDiscoverabilityMetrics) ObserveRecall(durationMs float64, resultCount int, outcome string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recallLatencies = append(m.recallLatencies, durationMs)
	m.recallSamples = append(m.recallSamples, RecallSample{DurationMs: durationMs, ResultCount: resultCount, Outcome: outcome})
}

func (m *InMemoryDiscoverabilityMetrics) ObserveRecallFeedback(feedback RecallFeedback) {
	m.mu.Lock()
	defer m.mu.Unlock()
	quality := normalizeRecallFeedbackQuality(feedback.Quality)
	m.recallFeedbackSamples = append(m.recallFeedbackSamples, RecallFeedbackSample{
		Used:            feedback.Used,
		AnswerSupported: feedback.AnswerSupported,
		Quality:         quality,
		QualityScore:    recallFeedbackQualityScore(quality),
		MissingContext:  feedback.MissingContext,
		Irrelevant:      feedback.Irrelevant,
	})
}

func (m *InMemoryDiscoverabilityMetrics) ObserveDreamFeedback(feedback DreamFeedback) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dreamFeedbackSamples = append(m.dreamFeedbackSamples, DreamFeedbackSample{
		Decision:   normalizeDreamFeedbackDecision(feedback.Decision),
		Outcome:    normalizeDreamFeedbackOutcome(feedback.Outcome),
		FromStatus: normalizeDreamStatusLabel(feedback.FromStatus),
	})
}

func (m *InMemoryDiscoverabilityMetrics) ObserveConflictReviewDuration(seconds float64, outcome string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.conflictReviewSamples = append(m.conflictReviewSamples, ConflictReviewSample{Seconds: seconds, Outcome: outcome})
}

func (m *InMemoryDiscoverabilityMetrics) IncVerifyVerdict(outcome string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.verifyVerdicts[outcome]++
}

func (m *InMemoryDiscoverabilityMetrics) EmbeddingSamples() []EmbeddingSample {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]EmbeddingSample, len(m.embeddingSamples))
	copy(out, m.embeddingSamples)
	return out
}

func (m *InMemoryDiscoverabilityMetrics) EmbeddingErrorCount(code string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.embeddingErrors[code]
}

func (m *InMemoryDiscoverabilityMetrics) RecallLatencies() []float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]float64, len(m.recallLatencies))
	copy(out, m.recallLatencies)
	return out
}

func (m *InMemoryDiscoverabilityMetrics) RecallSamples() []RecallSample {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]RecallSample, len(m.recallSamples))
	copy(out, m.recallSamples)
	return out
}

func (m *InMemoryDiscoverabilityMetrics) RecallFeedbackSamples() []RecallFeedbackSample {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]RecallFeedbackSample, len(m.recallFeedbackSamples))
	copy(out, m.recallFeedbackSamples)
	return out
}

func (m *InMemoryDiscoverabilityMetrics) DreamFeedbackSamples() []DreamFeedbackSample {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]DreamFeedbackSample, len(m.dreamFeedbackSamples))
	copy(out, m.dreamFeedbackSamples)
	return out
}

func (m *InMemoryDiscoverabilityMetrics) ConflictReviewSamples() []ConflictReviewSample {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]ConflictReviewSample, len(m.conflictReviewSamples))
	copy(out, m.conflictReviewSamples)
	return out
}

func (m *InMemoryDiscoverabilityMetrics) VerifyVerdictCount(outcome string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.verifyVerdicts[outcome]
}

func (m *InMemoryDiscoverabilityMetrics) ObserveAssessorCall(inputTokens, outputTokens int, durationSeconds float64, outcome string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.assessorCalls = append(m.assessorCalls, AssessorCallSample{
		InputTokens: inputTokens, OutputTokens: outputTokens, DurationSeconds: durationSeconds, Outcome: outcome,
	})
}

func (m *InMemoryDiscoverabilityMetrics) IncAssessorValidationFailure(stage string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.assessorValidation[stage]++
}

func (m *InMemoryDiscoverabilityMetrics) IncAssessorValidationFieldFailure(stage, family string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.assessorValidationFields[assessorValidationFieldKey{stage: stage, family: family}]++
}

func (m *InMemoryDiscoverabilityMetrics) IncAssessorCandidateTruncation() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.assessorTruncations++
}

func (m *InMemoryDiscoverabilityMetrics) IncAssessorAssessmentPersistence(outcome string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.assessorPersistence[outcome]++
}

func (m *InMemoryDiscoverabilityMetrics) IncAssessorDuplicateRequestPrevention(stage string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.assessorDuplicatePrevention[stage]++
}

func (m *InMemoryDiscoverabilityMetrics) IncAssessorConfidenceGate(band string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.assessorGateBands[band]++
}

func (m *InMemoryDiscoverabilityMetrics) AddAssessorReviewExpiry(count int64) {
	if count <= 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.assessorReviewExpiry += count
}

func (m *InMemoryDiscoverabilityMetrics) IncAssessorTerminalFailure(stage string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.assessorTerminalFailures[NormalizeAssessorTerminalFailureStage(stage)]++
}

func (m *InMemoryDiscoverabilityMetrics) IncSubmissionQuarantinePurgeFailure() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.quarantinePurgeFailures++
}

func (m *InMemoryDiscoverabilityMetrics) AssessorCalls() []AssessorCallSample {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]AssessorCallSample, len(m.assessorCalls))
	copy(out, m.assessorCalls)
	return out
}

func (m *InMemoryDiscoverabilityMetrics) AssessorValidationFailureCount(stage string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.assessorValidation[stage]
}

func (m *InMemoryDiscoverabilityMetrics) AssessorValidationFieldFailureCount(stage, family string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.assessorValidationFields[assessorValidationFieldKey{stage: stage, family: family}]
}

func (m *InMemoryDiscoverabilityMetrics) AssessorCandidateTruncationCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.assessorTruncations
}

func (m *InMemoryDiscoverabilityMetrics) AssessorPersistenceCount(outcome string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.assessorPersistence[outcome]
}

func (m *InMemoryDiscoverabilityMetrics) AssessorDuplicatePreventionCount(stage string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.assessorDuplicatePrevention[stage]
}

func (m *InMemoryDiscoverabilityMetrics) AssessorConfidenceGateCount(band string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.assessorGateBands[band]
}

func (m *InMemoryDiscoverabilityMetrics) AssessorReviewExpiryCount() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.assessorReviewExpiry
}

func (m *InMemoryDiscoverabilityMetrics) AssessorTerminalFailureCount(stage string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.assessorTerminalFailures[NormalizeAssessorTerminalFailureStage(stage)]
}

// NormalizeAssessorTerminalFailureStage keeps terminal failure telemetry and
// status projections on a bounded, system-only vocabulary.
func NormalizeAssessorTerminalFailureStage(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "placement_load", "placement_item", "trusted_context_validation",
		"deterministic_security_scan", "catalog_context_overflow",
		"catalog_context_validation", "predicate_options_overflow",
		"assessment_input_overflow", "candidate_context_validation", "candidate_context_limit",
		"candidate_prefetch", "assessment_attempt_consumed", "assessment", "provider",
		"review_override", "security_signal", "confidence_policy", "policy_review",
		"deterministic_policy", "commit_review", "replacement_conflict", "assessment_scope",
		"stale_source", "semantic_commit", "verification", "stored_response",
		"predicate_catalog", "extraction", "preflight":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "unknown"
	}
}

func (m *InMemoryDiscoverabilityMetrics) SubmissionQuarantinePurgeFailureCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.quarantinePurgeFailures
}

func (m *InMemoryDiscoverabilityMetrics) ObserveEmbeddingReconciliationRun(outcome string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reconciliationRuns[strings.TrimSpace(outcome)]++
}

func (m *InMemoryDiscoverabilityMetrics) ObserveEmbeddingReconciliationCanary(outcome string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reconciliationCanaries[strings.TrimSpace(outcome)]++
}

func (m *InMemoryDiscoverabilityMetrics) ObserveEmbeddingReconciliationJobs(action, sourceKind, failureClass, failureCode string, count int) {
	if count <= 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	key := strings.Join([]string{strings.TrimSpace(action), strings.TrimSpace(sourceKind), strings.TrimSpace(failureClass), strings.TrimSpace(failureCode)}, ":")
	m.reconciliationJobs[key] += count
}

func (m *InMemoryDiscoverabilityMetrics) ObserveEmbeddingReconciliationDuration(seconds float64, _ string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reconciliationDurations = append(m.reconciliationDurations, seconds)
}

func (m *InMemoryDiscoverabilityMetrics) EmbeddingReconciliationRunCount(outcome string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reconciliationRuns[strings.TrimSpace(outcome)]
}

func (m *InMemoryDiscoverabilityMetrics) EmbeddingReconciliationCanaryCount(outcome string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reconciliationCanaries[strings.TrimSpace(outcome)]
}
