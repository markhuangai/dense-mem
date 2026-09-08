package contract

// SynchronousRememberCommitResult is the terminal, replayable result produced by
// the one request-owned Remember transaction. PublicResult is already safe to
// persist and replay; callers must not reconstruct it from placement rows.
type SynchronousRememberCommitResult struct {
	IngestID            string
	AssessmentID        string
	Outcome             string
	PublicResult        map[string]any
	RelationshipResults []RelationshipDecisionResult
	SearchDocuments     []SearchDocumentResult
	EntityResolutionIDs []string
}
