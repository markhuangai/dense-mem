package observability

import "sync"

// DiscoverabilityMetrics is the minimal contract Unit 25 services emit to.
// Implementations must be safe for concurrent use. A no-op implementation is
// returned by NoopDiscoverabilityMetrics and is suitable as a default when a
// production metrics backend (e.g. Prometheus) is not wired.
type DiscoverabilityMetrics interface {
	// ObserveEmbeddingLatency records one embedding-provider call.
	// outcome must be one of: "ok" | "timeout" | "error" | "rate_limited".
	ObserveEmbeddingLatency(durationMs float64, outcome string)
	// IncEmbeddingError bumps the error counter for the given failure code
	// so dashboards can surface which failure mode dominates.
	IncEmbeddingError(code string)
	// ObserveRecallLatency records one recall-service call end-to-end.
	ObserveRecallLatency(durationMs float64)
	// ObserveRecall records one recall-service call with its result count.
	ObserveRecall(durationMs float64, resultCount int, outcome string)
	// ObserveRecallFeedback records one host-LLM online recall feedback event.
	ObserveRecallFeedback(feedback RecallFeedback)
	// ObserveDreamFeedback records one bounded dream-feedback decision event.
	ObserveDreamFeedback(feedback DreamFeedback)
	// ObserveMemoryFunnelLatency records latency between memory pipeline stages.
	ObserveMemoryFunnelLatency(stage string, seconds float64, outcome string)
	// IncFragmentCreate bumps the fragment-create outcome counter.
	// outcome must be one of: "created" | "duplicate" | "error".
	IncFragmentCreate(outcome string)

	// IncClaimCreate bumps the claim-create outcome counter.
	// outcome must be one of: "created" | "duplicate" | "error".
	// dedupeReason is the reason for deduplication (empty string if not a duplicate).
	IncClaimCreate(outcome string, dedupeReason string)
	// IncVerifyVerdict bumps the verify-verdict counter.
	// outcome must be one of: "verified" | "refuted" | "inconclusive" | "error".
	IncVerifyVerdict(outcome string)
	// IncPromotionOutcome bumps the claim-to-fact promotion outcome counter.
	// outcome must be one of: "promoted" | "skipped" | "error".
	IncPromotionOutcome(outcome string)
	// ObservePromoteLockWait records how long a promotion waited for the row lock.
	ObservePromoteLockWait(seconds float64)
	// IncFragmentRetract bumps the fragment-retract counter.
	IncFragmentRetract()
	// IncFactNeedsRevalidation bumps the counter for facts that are queued
	// for revalidation after a contradicting fragment is ingested.
	IncFactNeedsRevalidation()
	// IncCommunityDetect bumps the community-detection run counter.
	// outcome must be one of: "ok" | "error" | "too_large" | "unavailable".
	IncCommunityDetect(outcome string)
	// ObserveCommunityDetect records the duration and projected node count of
	// one community-detection run.
	ObserveCommunityDetect(durationSeconds float64, projectedNodes int)
}

// AssessorMetrics is an optional V2.4 write-path metric surface. Its methods
// deliberately accept no request identity, evidence, threshold, or rationale
// so assessor telemetry cannot create sensitive or high-cardinality labels.
type AssessorMetrics interface {
	ObserveAssessorCall(inputTokens, outputTokens int, durationSeconds float64, outcome string)
	IncAssessorValidationFailure(stage string)
	IncAssessorCandidateTruncation()
	IncAssessorAssessmentPersistence(outcome string)
	IncAssessorDuplicateRequestPrevention(stage string)
	IncAssessorConfidenceGate(band string)
	AddAssessorReviewExpiry(count int64)
}

// NoopDiscoverabilityMetrics returns a DiscoverabilityMetrics that discards
// every call. Use this as the default when no metrics backend is configured —
// call sites never need nil checks.
func NoopDiscoverabilityMetrics() DiscoverabilityMetrics { return noopMetrics{} }

type noopMetrics struct{}

var _ DiscoverabilityMetrics = noopMetrics{}

func (noopMetrics) ObserveEmbeddingLatency(float64, string) {}
func (noopMetrics) IncEmbeddingError(string)                {}
func (noopMetrics) ObserveRecallLatency(float64)            {}
func (noopMetrics) ObserveRecall(float64, int, string)      {}
func (noopMetrics) ObserveRecallFeedback(RecallFeedback)    {}
func (noopMetrics) ObserveDreamFeedback(DreamFeedback)      {}
func (noopMetrics) ObserveMemoryFunnelLatency(string, float64, string) {
}
func (noopMetrics) IncFragmentCreate(string)                      {}
func (noopMetrics) IncClaimCreate(string, string)                 {}
func (noopMetrics) IncVerifyVerdict(string)                       {}
func (noopMetrics) IncPromotionOutcome(string)                    {}
func (noopMetrics) ObservePromoteLockWait(float64)                {}
func (noopMetrics) IncFragmentRetract()                           {}
func (noopMetrics) IncFactNeedsRevalidation()                     {}
func (noopMetrics) IncCommunityDetect(string)                     {}
func (noopMetrics) ObserveCommunityDetect(float64, int)           {}
func (noopMetrics) ObserveAssessorCall(int, int, float64, string) {}
func (noopMetrics) IncAssessorValidationFailure(string)           {}
func (noopMetrics) IncAssessorCandidateTruncation()               {}
func (noopMetrics) IncAssessorAssessmentPersistence(string)       {}
func (noopMetrics) IncAssessorDuplicateRequestPrevention(string)  {}
func (noopMetrics) IncAssessorConfidenceGate(string)              {}
func (noopMetrics) AddAssessorReviewExpiry(int64)                 {}

// InMemoryDiscoverabilityMetrics is a test-friendly recorder. Tests can
// inspect the captured samples to assert that a code path actually emitted
// the expected metric without standing up Prometheus.
type InMemoryDiscoverabilityMetrics struct {
	mu                          sync.Mutex
	embeddingSamples            []EmbeddingSample
	embeddingErrors             map[string]int
	recallLatencies             []float64
	recallSamples               []RecallSample
	recallFeedbackSamples       []RecallFeedbackSample
	dreamFeedbackSamples        []DreamFeedbackSample
	memoryFunnelSamples         []MemoryFunnelSample
	fragmentOutcomes            map[string]int
	claimCreateSamples          []ClaimCreateSample
	verifyVerdicts              map[string]int
	promotionOutcomes           map[string]int
	promoteLockWaits            []float64
	fragmentRetracts            int
	factNeedsRevalidation       int
	communityDetectOuts         map[string]int
	communityDetectSamples      []CommunityDetectSample
	assessorCalls               []AssessorCallSample
	assessorValidation          map[string]int
	assessorTruncations         int
	assessorPersistence         map[string]int
	assessorDuplicatePrevention map[string]int
	assessorGateBands           map[string]int
	assessorReviewExpiry        int64
}

// ClaimCreateSample is one recorded claim-create event.
type ClaimCreateSample struct {
	Outcome      string
	DedupeReason string
}

// CommunityDetectSample is one recorded community-detection run.
type CommunityDetectSample struct {
	DurationSeconds float64
	ProjectedNodes  int
}

// AssessorCallSample records one bounded assessor conversation.
type AssessorCallSample struct {
	InputTokens     int
	OutputTokens    int
	DurationSeconds float64
	Outcome         string
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

// MemoryFunnelSample is one observed pipeline-stage latency.
type MemoryFunnelSample struct {
	Stage   string
	Seconds float64
	Outcome string
}

var _ DiscoverabilityMetrics = (*InMemoryDiscoverabilityMetrics)(nil)

// NewInMemoryDiscoverabilityMetrics constructs a fresh recorder.
func NewInMemoryDiscoverabilityMetrics() *InMemoryDiscoverabilityMetrics {
	return &InMemoryDiscoverabilityMetrics{
		embeddingErrors:             make(map[string]int),
		fragmentOutcomes:            make(map[string]int),
		verifyVerdicts:              make(map[string]int),
		promotionOutcomes:           make(map[string]int),
		communityDetectOuts:         make(map[string]int),
		assessorValidation:          make(map[string]int),
		assessorPersistence:         make(map[string]int),
		assessorDuplicatePrevention: make(map[string]int),
		assessorGateBands:           make(map[string]int),
	}
}

// ObserveEmbeddingLatency records one sample.
func (m *InMemoryDiscoverabilityMetrics) ObserveEmbeddingLatency(durationMs float64, outcome string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.embeddingSamples = append(m.embeddingSamples, EmbeddingSample{DurationMs: durationMs, Outcome: outcome})
}

// IncEmbeddingError bumps the counter for `code`.
func (m *InMemoryDiscoverabilityMetrics) IncEmbeddingError(code string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.embeddingErrors[code]++
}

// ObserveRecallLatency records one end-to-end recall duration.
func (m *InMemoryDiscoverabilityMetrics) ObserveRecallLatency(durationMs float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recallLatencies = append(m.recallLatencies, durationMs)
}

// ObserveRecall records one recall event with the number of returned hits.
func (m *InMemoryDiscoverabilityMetrics) ObserveRecall(durationMs float64, resultCount int, outcome string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recallLatencies = append(m.recallLatencies, durationMs)
	m.recallSamples = append(m.recallSamples, RecallSample{DurationMs: durationMs, ResultCount: resultCount, Outcome: outcome})
}

// ObserveRecallFeedback records one host-LLM online recall-feedback event.
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

// ObserveDreamFeedback records one bounded dream-feedback decision event.
func (m *InMemoryDiscoverabilityMetrics) ObserveDreamFeedback(feedback DreamFeedback) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dreamFeedbackSamples = append(m.dreamFeedbackSamples, DreamFeedbackSample{
		Decision:   normalizeDreamFeedbackDecision(feedback.Decision),
		Outcome:    normalizeDreamFeedbackOutcome(feedback.Outcome),
		FromStatus: normalizeDreamStatusLabel(feedback.FromStatus),
	})
}

// ObserveMemoryFunnelLatency records one memory-pipeline stage latency.
func (m *InMemoryDiscoverabilityMetrics) ObserveMemoryFunnelLatency(stage string, seconds float64, outcome string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.memoryFunnelSamples = append(m.memoryFunnelSamples, MemoryFunnelSample{Stage: stage, Seconds: seconds, Outcome: outcome})
}

// IncFragmentCreate bumps the fragment-create outcome counter.
func (m *InMemoryDiscoverabilityMetrics) IncFragmentCreate(outcome string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fragmentOutcomes[outcome]++
}

// EmbeddingSamples returns a copy of the recorded embedding samples.
func (m *InMemoryDiscoverabilityMetrics) EmbeddingSamples() []EmbeddingSample {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]EmbeddingSample, len(m.embeddingSamples))
	copy(out, m.embeddingSamples)
	return out
}

// EmbeddingErrorCount returns the recorded count for `code`.
func (m *InMemoryDiscoverabilityMetrics) EmbeddingErrorCount(code string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.embeddingErrors[code]
}

// RecallLatencies returns a copy of the recorded recall-latency samples.
func (m *InMemoryDiscoverabilityMetrics) RecallLatencies() []float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]float64, len(m.recallLatencies))
	copy(out, m.recallLatencies)
	return out
}

// RecallSamples returns a copy of the recorded recall samples.
func (m *InMemoryDiscoverabilityMetrics) RecallSamples() []RecallSample {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]RecallSample, len(m.recallSamples))
	copy(out, m.recallSamples)
	return out
}

// RecallFeedbackSamples returns a copy of the recorded online recall feedback.
func (m *InMemoryDiscoverabilityMetrics) RecallFeedbackSamples() []RecallFeedbackSample {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]RecallFeedbackSample, len(m.recallFeedbackSamples))
	copy(out, m.recallFeedbackSamples)
	return out
}

// DreamFeedbackSamples returns a copy of the recorded dream feedback.
func (m *InMemoryDiscoverabilityMetrics) DreamFeedbackSamples() []DreamFeedbackSample {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]DreamFeedbackSample, len(m.dreamFeedbackSamples))
	copy(out, m.dreamFeedbackSamples)
	return out
}

// MemoryFunnelSamples returns a copy of the recorded funnel latency samples.
func (m *InMemoryDiscoverabilityMetrics) MemoryFunnelSamples() []MemoryFunnelSample {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]MemoryFunnelSample, len(m.memoryFunnelSamples))
	copy(out, m.memoryFunnelSamples)
	return out
}

// FragmentCreateCount returns the recorded count for `outcome`.
func (m *InMemoryDiscoverabilityMetrics) FragmentCreateCount(outcome string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.fragmentOutcomes[outcome]
}

// IncClaimCreate records one claim-create event.
func (m *InMemoryDiscoverabilityMetrics) IncClaimCreate(outcome string, dedupeReason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.claimCreateSamples = append(m.claimCreateSamples, ClaimCreateSample{Outcome: outcome, DedupeReason: dedupeReason})
}

// IncVerifyVerdict bumps the verify-verdict counter.
func (m *InMemoryDiscoverabilityMetrics) IncVerifyVerdict(outcome string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.verifyVerdicts[outcome]++
}

// IncPromotionOutcome bumps the promotion-outcome counter.
func (m *InMemoryDiscoverabilityMetrics) IncPromotionOutcome(outcome string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.promotionOutcomes[outcome]++
}

// ObservePromoteLockWait records one promotion lock-wait duration.
func (m *InMemoryDiscoverabilityMetrics) ObservePromoteLockWait(seconds float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.promoteLockWaits = append(m.promoteLockWaits, seconds)
}

// IncFragmentRetract bumps the fragment-retract counter.
func (m *InMemoryDiscoverabilityMetrics) IncFragmentRetract() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fragmentRetracts++
}

// IncFactNeedsRevalidation bumps the fact-needs-revalidation counter.
func (m *InMemoryDiscoverabilityMetrics) IncFactNeedsRevalidation() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.factNeedsRevalidation++
}

// IncCommunityDetect bumps the community-detection outcome counter.
func (m *InMemoryDiscoverabilityMetrics) IncCommunityDetect(outcome string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.communityDetectOuts[outcome]++
}

// ObserveCommunityDetect records one community-detection run duration and node count.
func (m *InMemoryDiscoverabilityMetrics) ObserveCommunityDetect(durationSeconds float64, projectedNodes int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.communityDetectSamples = append(m.communityDetectSamples, CommunityDetectSample{DurationSeconds: durationSeconds, ProjectedNodes: projectedNodes})
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

// ClaimCreateSamples returns a copy of the recorded claim-create samples.
func (m *InMemoryDiscoverabilityMetrics) ClaimCreateSamples() []ClaimCreateSample {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]ClaimCreateSample, len(m.claimCreateSamples))
	copy(out, m.claimCreateSamples)
	return out
}

// VerifyVerdictCount returns the recorded count for `outcome`.
func (m *InMemoryDiscoverabilityMetrics) VerifyVerdictCount(outcome string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.verifyVerdicts[outcome]
}

// PromotionOutcomeCount returns the recorded count for `outcome`.
func (m *InMemoryDiscoverabilityMetrics) PromotionOutcomeCount(outcome string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.promotionOutcomes[outcome]
}

// PromoteLockWaits returns a copy of the recorded promotion lock-wait durations.
func (m *InMemoryDiscoverabilityMetrics) PromoteLockWaits() []float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]float64, len(m.promoteLockWaits))
	copy(out, m.promoteLockWaits)
	return out
}

// FragmentRetractCount returns the total number of fragment-retract events.
func (m *InMemoryDiscoverabilityMetrics) FragmentRetractCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.fragmentRetracts
}

// FactNeedsRevalidationCount returns the total number of fact-needs-revalidation events.
func (m *InMemoryDiscoverabilityMetrics) FactNeedsRevalidationCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.factNeedsRevalidation
}

// CommunityDetectCount returns the recorded count for `outcome`.
func (m *InMemoryDiscoverabilityMetrics) CommunityDetectCount(outcome string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.communityDetectOuts[outcome]
}

// CommunityDetectSamples returns a copy of the recorded community-detection samples.
func (m *InMemoryDiscoverabilityMetrics) CommunityDetectSamples() []CommunityDetectSample {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]CommunityDetectSample, len(m.communityDetectSamples))
	copy(out, m.communityDetectSamples)
	return out
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
