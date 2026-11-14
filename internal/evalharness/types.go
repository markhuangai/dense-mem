package evalharness

import "time"

const DefaultImportConcurrency = 4

type CorpusItem struct {
	SourceDocID   string         `json:"source_doc_id"`
	Title         string         `json:"title,omitempty"`
	Content       string         `json:"content"`
	SourceDataset string         `json:"source_dataset,omitempty"`
	Labels        []string       `json:"labels,omitempty"`
	Metadata      map[string]any `json:"metadata,omitempty"`
}

type Ref struct {
	Type        string `json:"type"`
	ID          string `json:"id"`
	SourceDocID string `json:"source_doc_id"`
}

type ImportedPartition struct {
	EvidenceID     string `json:"evidence_id"`
	OccurrenceID   string `json:"occurrence_id"`
	SourceItemID   string `json:"source_item_id"`
	PartitionIndex int    `json:"partition_index"`
	SpanStart      int    `json:"span_start"`
	SpanEnd        int    `json:"span_end"`
	ContentHash    string `json:"content_hash"`
}

type KnowledgeMapping struct {
	Partitions           map[string][]ImportedPartition `json:"partitions"`
	BySourceDocID        map[string]Ref                 `json:"by_source_doc_id"`
	BySourceDocIDAndType map[string]map[string][]Ref    `json:"by_source_doc_id_and_type"`
}

type Question struct {
	ID          string   `json:"id"`
	Query       string   `json:"query"`
	Answers     []string `json:"answers"`
	GoldSources []string `json:"gold_sources"`
}

// HostQuestion deliberately cannot carry gold labels into a conversation.
type HostQuestion struct {
	ID    string `json:"id"`
	Query string `json:"question"`
}

type Manifest struct {
	Version             int       `json:"version"`
	RunID               string    `json:"run_id"`
	WorkflowRunID       string    `json:"workflow_run_id"`
	SourceRevision      string    `json:"source_revision"`
	Image               string    `json:"image"`
	RegistryHash        string    `json:"registry_sha256"`
	Dataset             string    `json:"dataset"`
	Mode                string    `json:"mode"`
	Model               string    `json:"model"`
	EmbeddingModel      string    `json:"embedding_model"`
	EmbeddingDimensions int       `json:"embedding_dimensions"`
	ContextWindowTokens int       `json:"context_window_tokens"`
	ModelDeadline       time.Time `json:"model_deadline"`
	DataDir             string    `json:"data_dir"`
	OutputDir           string    `json:"output_dir"`
}

type Citation struct {
	EvidenceID string `json:"evidence_id"`
	Quote      string `json:"quote"`
}

type MemoryCall struct {
	Tool      string         `json:"tool"`
	Arguments map[string]any `json:"arguments"`
	Result    map[string]any `json:"result"`
	ElapsedMS float64        `json:"elapsed_ms"`
	Error     string         `json:"error,omitempty"`
}

type ProviderAttempt struct {
	Number     int     `json:"number"`
	ElapsedMS  float64 `json:"elapsed_ms"`
	UsageState string  `json:"usage_state"`
	Input      int     `json:"input_tokens"`
	Output     int     `json:"output_tokens"`
	Total      int     `json:"total_tokens"`
	Error      string  `json:"error,omitempty"`
}

type HostResult struct {
	Policy    string            `json:"policy"`
	Answer    string            `json:"answer"`
	Citations []Citation        `json:"citations"`
	Calls     []MemoryCall      `json:"calls"`
	Attempts  []ProviderAttempt `json:"attempts"`
	ElapsedMS float64           `json:"elapsed_ms"`
	Error     string            `json:"error,omitempty"`
}

type Observation struct {
	QuestionID string       `json:"question_id"`
	Condition  string       `json:"condition"`
	Initial    MemoryCall   `json:"initial"`
	Hosts      []HostResult `json:"hosts"`
}

type CanonicalEvidence struct {
	SourceID string `json:"source_id"`
	Content  string `json:"content"`
}

type UsageTotals struct {
	Attempts    int   `json:"attempts"`
	Available   int   `json:"available"`
	Unavailable int   `json:"unavailable"`
	Partial     int   `json:"partial"`
	Input       int64 `json:"reported_input_tokens"`
	Output      int64 `json:"reported_output_tokens"`
	Total       int64 `json:"reported_total_tokens"`
}
