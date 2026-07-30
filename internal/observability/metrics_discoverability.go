package observability

import "sync"

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
func (noopMetrics) IncVerifyVerdict(string) {}

// InMemoryDiscoverabilityMetrics is a test-friendly recorder.
type InMemoryDiscoverabilityMetrics struct {
	mu                    sync.Mutex
	embeddingSamples      []EmbeddingSample
	embeddingErrors       map[string]int
	recallLatencies       []float64
	recallSamples         []RecallSample
	recallFeedbackSamples []RecallFeedbackSample
	dreamFeedbackSamples  []DreamFeedbackSample
	conflictReviewSamples []ConflictReviewSample
	verifyVerdicts        map[string]int
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

// NewInMemoryDiscoverabilityMetrics constructs a fresh recorder.
func NewInMemoryDiscoverabilityMetrics() *InMemoryDiscoverabilityMetrics {
	return &InMemoryDiscoverabilityMetrics{
		embeddingErrors: make(map[string]int),
		verifyVerdicts:  make(map[string]int),
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
