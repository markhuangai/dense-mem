package contract

import (
	"context"
	"time"
)

type CommunityCandidate struct {
	ID          string `json:"id"`
	Version     int64  `json:"version"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type CommunityInput struct {
	Topic            CommunityCandidate   `json:"topic"`
	Current          string               `json:"current_parent,omitempty"`
	Candidates       []CommunityCandidate `json:"candidates"`
	WorkID           string               `json:"-"`
	EndpointVersions map[string]int64     `json:"-"`
}

type CommunityOutcome struct {
	InputHash    string
	Method       string
	Status       string
	ParentID     string
	AssessmentID string
}

type CommunityRepository interface {
	ClaimCommunity(context.Context, MaintenanceTurn, string, time.Time) (*CommunityInput, *MaintenanceClaim, error)
	CompleteCommunity(context.Context, MaintenanceClaim, CommunityInput, CommunityOutcome, string, time.Time) error
}

type CommunityBatchRepository interface {
	ClaimCommunities(context.Context, MaintenanceTurn, string, time.Time) ([]CommunityInput, *MaintenanceClaim, error)
	CompleteCommunityItem(context.Context, MaintenanceClaim, CommunityInput, CommunityOutcome, string, time.Time) error
}
