package contract

// SubmissionAssessmentKnownEvidence is an immutable, request-scoped snapshot
// of explicitly requested evidence that passed the authenticated visibility
// and lifecycle eligibility fence. It is read-only assessment context; it is
// never treated as submitted evidence or security input.
type SubmissionAssessmentKnownEvidence struct {
	TeamID                  string
	EvidenceID              string
	FragmentID              string
	IngestID                string
	OwnerProfileID          string
	Content                 string
	ContentHash             string
	Authority               string
	SourceID                string
	SourceRevisionID        string
	CurrentSourceRevisionID string
	SpaceID                 string
	SpaceGeneration         int64
}

type SubmissionAssessmentKnownEvidenceInput struct {
	TeamID         string
	OwnerProfileID string
	// SpaceID scopes known evidence to the memory space receiving the submission.
	// Empty retains the team-shared default for direct repository callers.
	SpaceID     string
	EvidenceIDs []string
}

type SubmissionAssessmentKnownEvidenceResult struct {
	Evidence []SubmissionAssessmentKnownEvidence
}
