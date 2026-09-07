// Package contract contains the trace capability's dependency-safe contracts.
package contract

// Input is the caller-owned trace request. The adapter derives memory-space
// scope from the authenticated relationship and never accepts it here.
type Input struct {
	TeamID                  string
	RelationshipID          string
	IncludeEvidenceContent  *bool
	IncludeVerification     *bool
	IncludeTransitions      *bool
	MaxDepth                int
	MaxEdges                int
	MaxEvents               int
	MaxFragmentContentRunes int
	PredicateKeys           []string
	Topic                   string
	MinRelevance            *float64
}
