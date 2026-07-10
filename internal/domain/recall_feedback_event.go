package domain

import (
	"time"

	"github.com/google/uuid"
)

const (
	RecallFeedbackSnapshotCaptured     = "captured"
	RecallFeedbackSnapshotFeedbackOnly = "feedback_only"

	RecallFeedbackResultTypeFragment  = "fragment"
	RecallFeedbackResultTypeClaim     = "claim"
	RecallFeedbackResultTypeFact      = "fact"
	RecallFeedbackResultTypeDream     = "dream"
	RecallFeedbackResultTypeAssertion = "assertion"
)

// RecallFeedbackEvent is the durable investigation record that connects a
// recall_memory result set to deferred host-LLM session feedback.
type RecallFeedbackEvent struct {
	RecallID        string                          `json:"recall_id"`
	CreatedAt       time.Time                       `json:"created_at"`
	UpdatedAt       time.Time                       `json:"updated_at"`
	FeedbackAt      *time.Time                      `json:"feedback_at,omitempty"`
	TeamID          *uuid.UUID                      `json:"team_id,omitempty"`
	ProfileID       *uuid.UUID                      `json:"profile_id,omitempty"`
	KeyID           *uuid.UUID                      `json:"key_id,omitempty"`
	AuthMethod      string                          `json:"auth_method"`
	ToolName        string                          `json:"tool_name"`
	Query           string                          `json:"query"`
	ToolArgs        map[string]any                  `json:"tool_args"`
	ResultRefs      []RecallFeedbackResultRef       `json:"result_refs"`
	ResultCount     int                             `json:"result_count"`
	SnapshotState   string                          `json:"snapshot_state"`
	Used            *bool                           `json:"used,omitempty"`
	AnswerSupported *bool                           `json:"answer_supported,omitempty"`
	Quality         string                          `json:"quality,omitempty"`
	MissingContext  *bool                           `json:"missing_context,omitempty"`
	Irrelevant      *bool                           `json:"irrelevant,omitempty"`
	FeedbackComment string                          `json:"feedback_comment,omitempty"`
	IrrelevantRefs  []RecallFeedbackJudgedResultRef `json:"irrelevant_result_refs,omitempty"`
	DreamFeedback   []RecallFeedbackDreamFeedback   `json:"dream_feedback,omitempty"`
	ResolvedResults []RecallFeedbackResolvedResult  `json:"resolved_results,omitempty"`
	ReviewQueue     []RecallMemoryReview            `json:"review_queue,omitempty"`
}

// RecallFeedbackResultRef is a content-free reference to one result returned by
// recall_memory. The control portal dereferences these IDs from Neo4j on demand.
type RecallFeedbackResultRef struct {
	Type           string     `json:"type"`
	ID             string     `json:"id"`
	Rank           int        `json:"rank"`
	Tier           string     `json:"tier,omitempty"`
	Score          *float64   `json:"score,omitempty"`
	FinalScore     *float64   `json:"final_score,omitempty"`
	SemanticRank   int        `json:"semantic_rank,omitempty"`
	KeywordRank    int        `json:"keyword_rank,omitempty"`
	StatusAtRecall string     `json:"status_at_recall,omitempty"`
	RecordedAt     *time.Time `json:"recorded_at,omitempty"`
	CreatedAt      *time.Time `json:"created_at,omitempty"`
	UpdatedAt      *time.Time `json:"updated_at,omitempty"`
	ValidFrom      *time.Time `json:"valid_from,omitempty"`
	ValidTo        *time.Time `json:"valid_to,omitempty"`
	RetractedAt    *time.Time `json:"retracted_at,omitempty"`
}

// RecallFeedbackSubmission is one submitted quality judgment for a recall_id.
type RecallFeedbackSubmission struct {
	RecallID        string
	Used            bool
	AnswerSupported bool
	Quality         string
	MissingContext  bool
	Irrelevant      bool
	FeedbackComment string
	IrrelevantRefs  []RecallFeedbackJudgedResultRef
	DreamFeedback   []RecallFeedbackDreamFeedback
}

// RecallFeedbackJudgedResultRef is a host-LLM judgment about one returned
// result, stored separately from bounded Prometheus labels.
type RecallFeedbackJudgedResultRef struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Rank int    `json:"rank,omitempty"`
}

// RecallFeedbackDreamFeedback captures host-LLM quality judgments for
// hypothesis-lane dreams without mutating dream status.
type RecallFeedbackDreamFeedback struct {
	DreamID         string `json:"dream_id"`
	Used            bool   `json:"used"`
	Quality         string `json:"quality"`
	Contradicted    bool   `json:"contradicted"`
	FeedbackComment string `json:"feedback_comment,omitempty"`
}

// RecallFeedbackEventFilter controls control-portal event listing.
type RecallFeedbackEventFilter struct {
	Limit          int
	Offset         int
	TeamID         *uuid.UUID
	ProfileID      *uuid.UUID
	Quality        string
	IncludePending bool
	MissingContext *bool
	Irrelevant     *bool
	From           *time.Time
	To             *time.Time
}

// RecallFeedbackEventPage is a paginated recall-feedback investigation response.
type RecallFeedbackEventPage struct {
	Items []RecallFeedbackEvent `json:"items"`
	Total int64                 `json:"total"`
}

// RecallFeedbackResolvedResult is the current graph state for a stored result
// reference. Missing rows are explicit so investigations can distinguish hard
// deletion from a retracted-but-present result.
type RecallFeedbackResolvedResult struct {
	Type             string                  `json:"type"`
	ID               string                  `json:"id"`
	Rank             int                     `json:"rank"`
	ResolutionStatus string                  `json:"resolution_status"`
	CurrentStatus    string                  `json:"current_status,omitempty"`
	Current          map[string]any          `json:"current,omitempty"`
	Ref              RecallFeedbackResultRef `json:"ref"`
}

// RecallMemoryReview is a durable, non-mutating review task created from bad
// recall feedback. Truth changes still require the normal verified placement flow.
type RecallMemoryReview struct {
	ReviewID        string     `json:"review_id"`
	ProfileID       uuid.UUID  `json:"team_id"`
	RecallID        string     `json:"recall_id"`
	KnowledgeType   string     `json:"knowledge_type"`
	KnowledgeID     string     `json:"knowledge_id"`
	Reasons         []string   `json:"reasons"`
	FeedbackComment string     `json:"feedback_comment,omitempty"`
	Status          string     `json:"status"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	ResolvedAt      *time.Time `json:"resolved_at,omitempty"`
}
