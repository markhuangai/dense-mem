package repository

type relationshipRecallCandidate struct {
	RelationshipID string
	Score          float64
	BestBranchRank int
	SearchState    string
}
