package contract

import "errors"

var ErrTopicUnavailable = errors.New("topic_unavailable")

type TopicReadInput struct {
	SpaceID     string
	TopicIDs    []string
	EvidenceIDs []string
}
type EvidenceTopic struct {
	ID            string
	Version       int64
	Name          string
	Description   string
	EvidenceIDs   []string
	AssignmentIDs []string
}
type TopicReadResult struct {
	Topics     []EvidenceTopic
	SpaceID    string
	Generation int64
	Rejected   map[string]string
}
