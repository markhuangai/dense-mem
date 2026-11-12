package dto

import "time"

type RecallRequest struct {
	Query            string     `query:"query" validate:"max=512"`
	Limit            int        `query:"limit" validate:"min=0,max=50"`
	TopicIDs         []string   `query:"topic_ids" validate:"omitempty,min=1,max=10,unique,dive,uuid"`
	TopicLimit       *int       `query:"topic_limit" validate:"omitempty,min=0,max=10"`
	ValidAt          *time.Time `query:"valid_at"`
	KnownAt          *time.Time `query:"known_at"`
	KnownEvidenceIDs []string   `query:"known_evidence_ids" validate:"max=200,unique,dive,uuid"`
}
