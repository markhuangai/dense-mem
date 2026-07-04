package evalharness

import "time"

const SeedSchemaVersion = "dense-mem.eval.seed.v1"

// SeedManifest describes a local-only evaluation seed pack.
type SeedManifest struct {
	SchemaVersion       string         `json:"schema_version"`
	SeedID              string         `json:"seed_id"`
	Description         string         `json:"description,omitempty"`
	GeneratedAt         string         `json:"generated_at,omitempty"`
	CorpusFile          string         `json:"corpus_file"`
	CasesFile           string         `json:"cases_file"`
	QrelsFile           string         `json:"qrels_file"`
	AnswersFile         string         `json:"answers_file,omitempty"`
	HardNegativesFile   string         `json:"hard_negatives_file,omitempty"`
	TransformsFile      string         `json:"transforms_file,omitempty"`
	DreamsFile          string         `json:"dreams_file,omitempty"`
	LicensesFile        string         `json:"licenses_file,omitempty"`
	EmbeddingProvider   string         `json:"embedding_provider,omitempty"`
	EmbeddingModel      string         `json:"embedding_model,omitempty"`
	EmbeddingDimensions int            `json:"embedding_dimensions,omitempty"`
	Counts              map[string]int `json:"counts,omitempty"`
	Sources             []SeedSource   `json:"sources,omitempty"`
}

type SeedSource struct {
	Name    string `json:"name"`
	URL     string `json:"url,omitempty"`
	License string `json:"license,omitempty"`
	Notes   string `json:"notes,omitempty"`
}

type CorpusItem struct {
	SourceDocID   string         `json:"source_doc_id"`
	Title         string         `json:"title,omitempty"`
	Content       string         `json:"content"`
	SourceDataset string         `json:"source_dataset,omitempty"`
	SourceType    string         `json:"source_type,omitempty"`
	Authority     string         `json:"authority,omitempty"`
	SourceQuality float64        `json:"source_quality,omitempty"`
	Labels        []string       `json:"labels,omitempty"`
	Metadata      map[string]any `json:"metadata,omitempty"`
	Claims        []TypedClaim   `json:"claims,omitempty"`
	AutoPromote   bool           `json:"auto_promote,omitempty"`
}

type TypedClaim struct {
	Subject           string         `json:"subject"`
	Predicate         string         `json:"predicate"`
	Object            string         `json:"object"`
	Modality          string         `json:"modality,omitempty"`
	Polarity          string         `json:"polarity,omitempty"`
	Speaker           string         `json:"speaker,omitempty"`
	ExtractConf       float64        `json:"extract_conf"`
	ResolutionConf    float64        `json:"resolution_conf"`
	IdempotencyKey    string         `json:"idempotency_key,omitempty"`
	ValidFrom         *time.Time     `json:"valid_from,omitempty"`
	ValidTo           *time.Time     `json:"valid_to,omitempty"`
	SupportedBy       []string       `json:"supported_by,omitempty"`
	ExtractionModel   string         `json:"extraction_model,omitempty"`
	ExtractionVersion string         `json:"extraction_version,omitempty"`
	PipelineRunID     string         `json:"pipeline_run_id,omitempty"`
	Classification    map[string]any `json:"classification,omitempty"`
}

type Case struct {
	CaseID           string   `json:"case_id"`
	Query            string   `json:"query"`
	TaskType         string   `json:"task_type,omitempty"`
	Difficulty       string   `json:"difficulty,omitempty"`
	Slices           []string `json:"slices,omitempty"`
	ExpectedBehavior string   `json:"expected_behavior,omitempty"`
	ValidAt          string   `json:"valid_at,omitempty"`
	KnownAt          string   `json:"known_at,omitempty"`
	Limit            int      `json:"limit,omitempty"`
	IncludeDreams    bool     `json:"include_dreams,omitempty"`
	UseCommunities   bool     `json:"use_communities,omitempty"`
}

type Ref struct {
	Type        string  `json:"type"`
	ID          string  `json:"id,omitempty"`
	SourceDocID string  `json:"source_doc_id,omitempty"`
	Rank        int     `json:"rank,omitempty"`
	Grade       float64 `json:"grade,omitempty"`
	Reason      string  `json:"reason,omitempty"`
}

type QRel struct {
	CaseID               string `json:"case_id"`
	RequiredRefs         []Ref  `json:"required_refs"`
	AcceptableRefs       []Ref  `json:"acceptable_refs,omitempty"`
	BadRefs              []Ref  `json:"bad_refs,omitempty"`
	RequiredEvidenceRefs []Ref  `json:"required_evidence_refs,omitempty"`
	BadEvidenceRefs      []Ref  `json:"bad_evidence_refs,omitempty"`
	RequiredDreamRefs    []Ref  `json:"required_dream_refs,omitempty"`
	AcceptableDreamRefs  []Ref  `json:"acceptable_dream_refs,omitempty"`
	BadDreamRefs         []Ref  `json:"bad_dream_refs,omitempty"`
}

type ExpectedDream struct {
	SourceDocID string         `json:"source_doc_id"`
	CaseID      string         `json:"case_id,omitempty"`
	Hypothesis  string         `json:"hypothesis,omitempty"`
	SourceRefs  []Ref          `json:"source_refs"`
	Acceptable  bool           `json:"acceptable,omitempty"`
	Bad         bool           `json:"bad,omitempty"`
	Labels      []string       `json:"labels,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

type AnswerLabel struct {
	CaseID             string   `json:"case_id"`
	ReferenceAnswer    string   `json:"reference_answer,omitempty"`
	MustInclude        []string `json:"must_include,omitempty"`
	MustNotInclude     []string `json:"must_not_include,omitempty"`
	ExpectedBehavior   string   `json:"expected_behavior,omitempty"`
	GroundednessPolicy string   `json:"groundedness_policy,omitempty"`
}

type SuiteCase struct {
	CaseID string   `json:"case_id"`
	Weight float64  `json:"weight,omitempty"`
	Slices []string `json:"slices,omitempty"`
}

type KnowledgeMapping struct {
	BySourceDocID        map[string]Ref              `json:"by_source_doc_id"`
	BySourceDocIDAndType map[string]map[string][]Ref `json:"by_source_doc_id_and_type,omitempty"`
	DreamSourceRefsByID  map[string][]Ref            `json:"dream_source_refs_by_id,omitempty"`
}

type RecallTrace struct {
	CaseID              string         `json:"case_id"`
	Query               string         `json:"query"`
	RankedRefs          []Ref          `json:"ranked_refs"`
	ContextRefs         []Ref          `json:"context_refs,omitempty"`
	ContextEvidenceRefs []Ref          `json:"context_evidence_refs,omitempty"`
	DreamRefs           []Ref          `json:"dream_refs,omitempty"`
	LatencyMS           int64          `json:"latency_ms,omitempty"`
	ContextBlockChars   int            `json:"context_block_chars,omitempty"`
	Raw                 map[string]any `json:"raw,omitempty"`
}

type RetrievalScore struct {
	CaseID                    string  `json:"case_id"`
	K                         int     `json:"k"`
	RelevantAtK               int     `json:"relevant_at_k"`
	RelevantTotal             int     `json:"relevant_total"`
	BadAtK                    int     `json:"bad_at_k"`
	RecallAtK                 float64 `json:"recall_at_k"`
	MRR                       float64 `json:"mrr"`
	NDCGAtK                   float64 `json:"ndcg_at_k"`
	FirstRequiredRank         int     `json:"first_required_rank,omitempty"`
	FirstBadRank              int     `json:"first_bad_rank,omitempty"`
	MissingRequired           []Ref   `json:"missing_required_refs,omitempty"`
	BadRefsAtK                []Ref   `json:"bad_refs_at_k,omitempty"`
	ContextScored             bool    `json:"context_scored"`
	ContextRelevantAtK        int     `json:"context_relevant_at_k"`
	ContextRelevantTotal      int     `json:"context_relevant_total"`
	ContextBadAtK             int     `json:"context_bad_at_k"`
	ContextRecallAtK          float64 `json:"context_recall_at_k"`
	ContextMRR                float64 `json:"context_mrr"`
	ContextNDCGAtK            float64 `json:"context_ndcg_at_k"`
	ContextFirstRequiredRank  int     `json:"context_first_required_rank,omitempty"`
	ContextFirstBadRank       int     `json:"context_first_bad_rank,omitempty"`
	ContextMissingRequired    []Ref   `json:"context_missing_required_refs,omitempty"`
	ContextBadRefsAtK         []Ref   `json:"context_bad_refs_at_k,omitempty"`
	EvidenceScored            bool    `json:"evidence_scored"`
	EvidenceRelevantAtK       int     `json:"evidence_relevant_at_k"`
	EvidenceRelevantTotal     int     `json:"evidence_relevant_total"`
	EvidenceBadAtK            int     `json:"evidence_bad_at_k"`
	EvidenceRecallAtK         float64 `json:"evidence_recall_at_k"`
	EvidenceMRR               float64 `json:"evidence_mrr"`
	EvidenceNDCGAtK           float64 `json:"evidence_ndcg_at_k"`
	EvidenceFirstRequiredRank int     `json:"evidence_first_required_rank,omitempty"`
	EvidenceFirstBadRank      int     `json:"evidence_first_bad_rank,omitempty"`
	EvidenceMissingRequired   []Ref   `json:"evidence_missing_required_refs,omitempty"`
	EvidenceBadRefsAtK        []Ref   `json:"evidence_bad_refs_at_k,omitempty"`
	DreamScored               bool    `json:"dream_scored"`
	DreamRelevantAtK          int     `json:"dream_relevant_at_k"`
	DreamRelevantTotal        int     `json:"dream_relevant_total"`
	DreamBadAtK               int     `json:"dream_bad_at_k"`
	DreamRecallAtK            float64 `json:"dream_recall_at_k"`
	DreamMRR                  float64 `json:"dream_mrr"`
	DreamNDCGAtK              float64 `json:"dream_ndcg_at_k"`
	DreamFirstRequiredRank    int     `json:"dream_first_required_rank,omitempty"`
	DreamFirstBadRank         int     `json:"dream_first_bad_rank,omitempty"`
	DreamMissingRequired      []Ref   `json:"dream_missing_required_refs,omitempty"`
	DreamBadRefsAtK           []Ref   `json:"dream_bad_refs_at_k,omitempty"`
	UnmappedSourceRefs        []Ref   `json:"unmapped_source_refs,omitempty"`
	LatencyMS                 int64   `json:"latency_ms,omitempty"`
}

type Summary struct {
	RunID                     string              `json:"run_id"`
	Mode                      string              `json:"mode"`
	SeedID                    string              `json:"seed_id"`
	SeedHash                  string              `json:"seed_hash"`
	SuitePath                 string              `json:"suite_path"`
	CaseCount                 int                 `json:"case_count"`
	ScoredCaseCount           int                 `json:"scored_case_count"`
	ContextScoredCaseCount    int                 `json:"context_scored_case_count"`
	EvidenceScoredCaseCount   int                 `json:"evidence_scored_case_count"`
	DreamScoredCaseCount      int                 `json:"dream_scored_case_count"`
	UnmappedSourceRefs        int                 `json:"unmapped_source_refs"`
	AverageRecallAtK          float64             `json:"average_recall_at_k"`
	AverageMRR                float64             `json:"average_mrr"`
	AverageNDCGAtK            float64             `json:"average_ndcg_at_k"`
	AverageBadAtK             float64             `json:"average_bad_at_k"`
	RequiredRank1Rate         float64             `json:"required_rank1_rate"`
	BadRank1Rate              float64             `json:"bad_rank1_rate"`
	AverageContextRecallAtK   float64             `json:"average_context_recall_at_k"`
	AverageContextMRR         float64             `json:"average_context_mrr"`
	AverageContextNDCGAtK     float64             `json:"average_context_ndcg_at_k"`
	AverageContextBadAtK      float64             `json:"average_context_bad_at_k"`
	ContextRequiredRank1Rate  float64             `json:"context_required_rank1_rate"`
	ContextBadRank1Rate       float64             `json:"context_bad_rank1_rate"`
	AverageEvidenceRecallAtK  float64             `json:"average_evidence_recall_at_k"`
	AverageEvidenceMRR        float64             `json:"average_evidence_mrr"`
	AverageEvidenceNDCGAtK    float64             `json:"average_evidence_ndcg_at_k"`
	AverageEvidenceBadAtK     float64             `json:"average_evidence_bad_at_k"`
	EvidenceRequiredRank1Rate float64             `json:"evidence_required_rank1_rate"`
	EvidenceBadRank1Rate      float64             `json:"evidence_bad_rank1_rate"`
	AverageDreamRecallAtK     float64             `json:"average_dream_recall_at_k"`
	AverageDreamMRR           float64             `json:"average_dream_mrr"`
	AverageDreamNDCGAtK       float64             `json:"average_dream_ndcg_at_k"`
	AverageDreamBadAtK        float64             `json:"average_dream_bad_at_k"`
	DreamRequiredRank1Rate    float64             `json:"dream_required_rank1_rate"`
	DreamBadRank1Rate         float64             `json:"dream_bad_rank1_rate"`
	Slices                    map[string]SliceAvg `json:"slices,omitempty"`
	CreatedAt                 time.Time           `json:"created_at"`
}

type SliceAvg struct {
	CaseCount                int     `json:"case_count"`
	ContextScoredCaseCount   int     `json:"context_scored_case_count"`
	EvidenceScoredCaseCount  int     `json:"evidence_scored_case_count"`
	DreamScoredCaseCount     int     `json:"dream_scored_case_count"`
	AverageRecallAtK         float64 `json:"average_recall_at_k"`
	AverageMRR               float64 `json:"average_mrr"`
	AverageNDCGAtK           float64 `json:"average_ndcg_at_k"`
	AverageBadAtK            float64 `json:"average_bad_at_k"`
	AverageContextRecallAtK  float64 `json:"average_context_recall_at_k"`
	AverageContextMRR        float64 `json:"average_context_mrr"`
	AverageContextNDCGAtK    float64 `json:"average_context_ndcg_at_k"`
	AverageContextBadAtK     float64 `json:"average_context_bad_at_k"`
	AverageEvidenceRecallAtK float64 `json:"average_evidence_recall_at_k"`
	AverageEvidenceMRR       float64 `json:"average_evidence_mrr"`
	AverageEvidenceNDCGAtK   float64 `json:"average_evidence_ndcg_at_k"`
	AverageEvidenceBadAtK    float64 `json:"average_evidence_bad_at_k"`
	AverageDreamRecallAtK    float64 `json:"average_dream_recall_at_k"`
	AverageDreamMRR          float64 `json:"average_dream_mrr"`
	AverageDreamNDCGAtK      float64 `json:"average_dream_ndcg_at_k"`
	AverageDreamBadAtK       float64 `json:"average_dream_bad_at_k"`
}

type RunConfig struct {
	RunID             string `json:"run_id"`
	Mode              string `json:"mode"`
	SeedManifest      string `json:"seed_manifest"`
	SeedHash          string `json:"seed_hash"`
	SuitePath         string `json:"suite_path"`
	BaseURL           string `json:"base_url,omitempty"`
	ControlURL        string `json:"control_url,omitempty"`
	ImportSeed        bool   `json:"import_seed"`
	ImportConcurrency int    `json:"import_concurrency,omitempty"`
	DirectImport      bool   `json:"direct_import,omitempty"`
	DirectImportBatch int    `json:"direct_import_batch,omitempty"`
	DirectImportTeam  string `json:"direct_import_team,omitempty"`
	TracesPath        string `json:"traces_path,omitempty"`
	MappingPath       string `json:"mapping_path,omitempty"`
	BaselineRunPath   string `json:"baseline_run_path,omitempty"`
}

type Comparison struct {
	BaselineRunID       string  `json:"baseline_run_id"`
	CandidateRunID      string  `json:"candidate_run_id"`
	SeedHash            string  `json:"seed_hash"`
	RecallDelta         float64 `json:"recall_delta"`
	MRRDelta            float64 `json:"mrr_delta"`
	NDCGDelta           float64 `json:"ndcg_delta"`
	BadAtKDelta         float64 `json:"bad_at_k_delta"`
	ContextRecallDelta  float64 `json:"context_recall_delta"`
	ContextMRRDelta     float64 `json:"context_mrr_delta"`
	ContextNDCGDelta    float64 `json:"context_ndcg_delta"`
	ContextBadAtKDelta  float64 `json:"context_bad_at_k_delta"`
	EvidenceRecallDelta float64 `json:"evidence_recall_delta"`
	EvidenceMRRDelta    float64 `json:"evidence_mrr_delta"`
	EvidenceNDCGDelta   float64 `json:"evidence_ndcg_delta"`
	EvidenceBadAtKDelta float64 `json:"evidence_bad_at_k_delta"`
	DreamRecallDelta    float64 `json:"dream_recall_delta"`
	DreamMRRDelta       float64 `json:"dream_mrr_delta"`
	DreamNDCGDelta      float64 `json:"dream_ndcg_delta"`
	DreamBadAtKDelta    float64 `json:"dream_bad_at_k_delta"`
}

type GateOptions struct {
	MinRecallAtK                 *float64 `json:"min_recall_at_k,omitempty"`
	MinRequiredRank1Rate         *float64 `json:"min_required_rank1_rate,omitempty"`
	MaxAverageBadAtK             *float64 `json:"max_average_bad_at_k,omitempty"`
	MaxBadRank1Rate              *float64 `json:"max_bad_rank1_rate,omitempty"`
	MinContextRecallAtK          *float64 `json:"min_context_recall_at_k,omitempty"`
	MinContextRequiredRank1Rate  *float64 `json:"min_context_required_rank1_rate,omitempty"`
	MaxAverageContextBadAtK      *float64 `json:"max_average_context_bad_at_k,omitempty"`
	MaxContextBadRank1Rate       *float64 `json:"max_context_bad_rank1_rate,omitempty"`
	MinEvidenceRecallAtK         *float64 `json:"min_evidence_recall_at_k,omitempty"`
	MinEvidenceRequiredRank1Rate *float64 `json:"min_evidence_required_rank1_rate,omitempty"`
	MaxAverageEvidenceBadAtK     *float64 `json:"max_average_evidence_bad_at_k,omitempty"`
	MaxEvidenceBadRank1Rate      *float64 `json:"max_evidence_bad_rank1_rate,omitempty"`
	MinDreamRecallAtK            *float64 `json:"min_dream_recall_at_k,omitempty"`
	MinDreamRequiredRank1Rate    *float64 `json:"min_dream_required_rank1_rate,omitempty"`
	MaxAverageDreamBadAtK        *float64 `json:"max_average_dream_bad_at_k,omitempty"`
	MaxDreamBadRank1Rate         *float64 `json:"max_dream_bad_rank1_rate,omitempty"`
}

type GateResult struct {
	Passed     bool               `json:"passed"`
	Thresholds map[string]float64 `json:"thresholds"`
	Metrics    map[string]float64 `json:"metrics"`
	Failures   []string           `json:"failures,omitempty"`
}
