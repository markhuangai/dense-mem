// Package assignment carries prepared topic decisions across the provider-free publication boundary.
package assignment

import "errors"

const MaxTopics = 20
const PolicyVersion = "topic-assignment-v1"

var ErrStale = errors.New("topic assignment input changed")
var ErrStorage = errors.New("topic assignment storage unavailable")

type Scope struct {
	TeamID      string
	SpaceID     string
	Generation  int64
	OperationID string
}

type Slice struct {
	EvidenceID        string
	SourceFingerprint string
	PriorInputHash    string
	Index             int
	Text              string
}

type Candidates struct {
	Topics      []Topic
	NeedsRepair bool
}

type Topic struct {
	ID          string   `json:"id"`
	Version     int64    `json:"version"`
	Label       string   `json:"label"`
	Description string   `json:"description"`
	Aliases     []string `json:"aliases,omitempty"`
}

type Proposal struct {
	Label       string `json:"label"`
	Description string `json:"description"`
	Quote       string `json:"quote"`
}

type Match struct {
	ID      string `json:"id"`
	Version int64  `json:"version"`
	Quote   string `json:"quote"`
}

type Outcome struct {
	Unchanged bool
	Reviewed  []Topic
	Index     int
	Text      string `json:"-"`
	InputHash string
	Method    string
	Matches   []Match
	Proposals []Proposal
}

type Publication struct {
	EvidenceID          string
	ExpectedFingerprint string
	Outcome             Outcome
}
