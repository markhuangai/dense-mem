package contract

type AdvanceSourceRevisionInput struct {
	TeamID                        string
	OwnerProfileID                string
	IngestID                      string
	SpaceID                       string
	SpaceGeneration               int64
	SourceKey                     string
	SourceKind                    string
	Authority                     string
	RevisionToken                 string
	ExpectedPreviousRevisionToken string
	ContentHash                   string
	Envelope                      map[string]any
}

type SourceRevisionResult struct {
	TeamID                       string
	SourceID                     string
	SourceRevisionID             string
	RevisionToken                string
	SupersededSourceRevisionID   string
	SupersededSourceRevisionSeen bool
}
