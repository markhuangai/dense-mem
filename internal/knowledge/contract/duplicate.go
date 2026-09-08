package contract

const (
	RememberDuplicateCandidateLimit = 10
	RememberDuplicateMaxEvidence    = 20
)

// RememberDuplicateCandidate is a canonical evidence item that the assessor
// may compare with one submitted occurrence. The repository has already
// applied authenticated team, profile, space, lifecycle, and search fences.
type RememberDuplicateCandidate struct {
	FragmentID          string
	OwnerProfileID      string
	Content             string
	ContentHash         string
	Distance            float64
	EmbeddingContractID string
}

// RememberDuplicateCandidateGroup binds the bounded candidate allowlist to a
// submitted evidence index. Empty candidates are meaningful: the assessor must
// return new for that item.
type RememberDuplicateCandidateGroup struct {
	EvidenceIndex int
	EvidenceID    string
	Candidates    []RememberDuplicateCandidate
}

// RememberDuplicateResolution is the server-owned exact result and the
// assessor-owned semantic result carried into the final transaction.
type RememberDuplicateResolution struct {
	EvidenceIndex       int
	EvidenceID          string
	InputFragmentID     string
	Disposition         string
	Exact               bool
	CandidateFragmentID string
	CandidateOwnerID    string
}

// RememberDuplicateEmbeddingPlan is the provider-independent pre-assessment
// render for unique non-exact evidence content.
type RememberDuplicateEmbeddingPlan struct {
	Documents               []SearchDocumentForEmbedding
	EmbeddingContractID     string
	EmbeddingDimensions     int
	EmbeddingModel          string
	SearchIndexGenerationID string
	IndexGeneration         int
}

// RememberDuplicateCandidateInput identifies one authenticated Remember
// request. Evidence content is read-only provider input; no durable row exists
// until the terminal commit.
type RememberDuplicateCandidateInput struct {
	TeamID          string
	OwnerProfileID  string
	SpaceID         string
	SpaceGeneration int64
	Evidence        []EvidenceInput
}

type RememberDuplicateResolutionResult struct {
	Exact      []RememberDuplicateResolution
	Candidates []RememberDuplicateCandidateGroup
}

// PlanRememberDuplicateEmbeddings resolves deterministic exact matches and
// returns one embedding document for every unique non-exact content value.
