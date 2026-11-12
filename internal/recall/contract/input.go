package contract

import "time"

type Request struct {
	Query            string     `json:"query"`
	Limit            int        `json:"limit,omitempty"`
	TopicIDs         []string   `json:"topic_ids,omitempty"`
	TopicLimit       *int       `json:"topic_limit,omitempty"`
	ValidAt          *time.Time `json:"valid_at,omitempty"`
	KnownAt          *time.Time `json:"known_at,omitempty"`
	KnownEvidenceIDs []string   `json:"known_evidence_ids,omitempty"`
}

type RecallRequest = Request
