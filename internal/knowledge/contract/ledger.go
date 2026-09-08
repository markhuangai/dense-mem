package contract

// Ledger write shapes. Semantic claiming and status mutation are intentionally
// not part of the runtime API.

// CreateIngestInput is the low-level evidence input used by conflict-derived
// evidence and synchronous commit helpers. It never creates placement state.
type CreateIngestInput struct {
	TeamID            string
	OwnerProfileID    string
	IngestID          string
	SpaceID           string
	SpaceGeneration   int64
	IdempotencyKey    string
	RequestHash       string
	SourceSummary     string
	Status            string
	TelemetryRemember bool
	Proposal          map[string]any
	Metadata          map[string]any
	Evidence          []EvidenceInput
}

type EvidenceInput struct {
	FragmentID                    string
	Content                       string
	ForceInsert                   bool
	ContentHash                   string
	SourceType                    string
	Authority                     string
	SourceRef                     string
	SourceKey                     string
	SourceRevisionToken           string
	ExpectedPreviousRevisionToken string
	SourceRevisionContentHash     string
	SourceRevisionEnvelope        map[string]any
	SupersedesEvidenceIDs         []string
	IdempotencyKey                string
	Labels                        []string
	Metadata                      map[string]any
	InitialEvent                  *SecurityEventDraft
}

type SecurityEventDraft struct {
	EventKind string
	Decision  string
	Reason    string
	Signals   []SecuritySignalInput
	Metadata  map[string]any
}

type SecurityEventInput struct {
	TeamID                 string
	OwnerProfileID         string
	IngestID               string
	FragmentID             string
	OccurrenceID           string
	EvidenceOwnerProfileID string
	SecurityEventDraft
}

type SecuritySignalInput struct {
	Kind      string
	Severity  string
	SpanStart int
	SpanEnd   int
	Quote     string
	Metadata  map[string]any
}

type EvidenceIngestResult struct {
	TeamID              string
	OwnerProfileID      string
	IngestID            string
	Status              string
	CorrelationID       string
	Existing            bool
	Proposal            map[string]any
	Evidence            []EvidenceFragment
	RelationshipResults []SubmissionRelationshipResult
}

type EvidenceFragment struct {
	FragmentID            string
	SubmittedFragmentID   string
	OccurrenceID          string
	CanonicalOwnerID      string
	OccurrenceOwnerID     string
	EvidenceIndex         int
	Content               string
	ContentHash           string
	Authority             string
	SourceID              string
	SourceRevisionID      string
	SupersededEvidenceIDs []string
}
