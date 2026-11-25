package postgres

import (
	"context"
	"errors"

	assignment "github.com/markhuangai/dense-mem/internal/topics/assignment"
	"gorm.io/gorm"
)

type topicAssignmentWriter interface {
	PublishAssignmentsTx(context.Context, *gorm.DB, assignment.Scope, []assignment.Publication) error
}

func (r *Store) WithTopicAssignments(writer topicAssignmentWriter) *Store {
	r.topicAssignments = writer
	return r
}

func (r *Store) commitTopicAssignments(ctx context.Context, tx *gorm.DB, input SynchronousRememberCommitInput, evidence []EvidenceFragment) error {
	if len(input.TopicAssignments) == 0 {
		return nil
	}
	if r.topicAssignments == nil {
		return errors.New("required topic assignment writer missing")
	}
	seen := map[string]bool{}
	items := make([]assignment.Publication, 0, len(input.TopicAssignments))
	for _, outcome := range input.TopicAssignments {
		if outcome.Index < 0 || outcome.Index >= len(evidence) {
			return errors.New("invalid topic assignment slice index")
		}
		id := evidence[outcome.Index].FragmentID
		if !seen[id] {
			items = append(items, assignment.Publication{EvidenceID: id, Outcome: outcome})
			seen[id] = true
		}
	}
	return r.topicAssignments.PublishAssignmentsTx(ctx, tx, assignment.Scope{TeamID: input.TeamID, SpaceID: input.SpaceID, Generation: input.SpaceGeneration, OperationID: input.IngestID}, items)
}
