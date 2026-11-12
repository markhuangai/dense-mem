package contract

import "strings"

const (
	defaultTraceEvents        = 100
	maxTraceEvents            = 500
	defaultTraceFragmentRunes = 2000
	maxTraceFragmentRunes     = 8000
)

type Input struct {
	TeamID                  string
	EvidenceID              string
	IncludeEvidenceContent  *bool
	IncludeTransitions      *bool
	MaxEvents               int
	MaxFragmentContentRunes int
}

func NormalizeInput(input Input) Input {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.EvidenceID = strings.TrimSpace(input.EvidenceID)
	input.MaxEvents = clampInt(input.MaxEvents, defaultTraceEvents, maxTraceEvents)
	input.MaxFragmentContentRunes = clampInt(input.MaxFragmentContentRunes, defaultTraceFragmentRunes, maxTraceFragmentRunes)
	return input
}

func clampInt(value, defaultValue, maxValue int) int {
	if value <= 0 {
		return defaultValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}
