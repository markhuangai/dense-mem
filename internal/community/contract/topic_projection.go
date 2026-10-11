package contract

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"time"

	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
)

const TopicProjectionSummaryVersion = "ontology-topic-v1"

func TopicProjectionSummary(definition ontology.Definition, entities, predicates []string) string {
	parts := []string{definition.Label, definition.Description}
	if len(entities) > 0 {
		parts = append(parts, "Entities: "+strings.Join(entities, ", "))
	}
	if len(predicates) > 0 {
		parts = append(parts, "Predicates: "+strings.Join(predicates, ", "))
	}
	text := []rune(strings.Join(parts, ". "))
	if len(text) > 4000 {
		text = text[:4000]
	}
	return string(text)
}

func TopicProjectionFingerprint(previous string, batch TopicProjectionBatch) (string, error) {
	fingerprints := append([]string(nil), batch.Work.Fingerprints...)
	sort.Strings(fingerprints)
	encoded, err := json.Marshal(struct {
		Version, Previous string
		Fingerprints      []string
		Sources           []CommunitySourceInput
	}{TopicProjectionSummaryVersion, previous, fingerprints, batch.Sources})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

type TopicProjectionCoverage struct {
	TotalTopics      int  `json:"total_topics"`
	CurrentTopics    int  `json:"current_topics"`
	PendingTopics    int  `json:"pending_topics"`
	StaleTopics      int  `json:"stale_topics"`
	FailedTopics     int  `json:"failed_topics"`
	CoverageComplete bool `json:"coverage_complete"`
}

type TopicProjectionWork struct {
	TeamID                 string
	SpaceID                string
	Generation             int64
	TopicID                string
	CommunityID            string
	RunID                  string
	LeaseToken             string
	Topic                  ontology.RecordView
	Cursor                 ontology.TopicMembershipCursor
	More                   bool
	Inputs                 []CommunityInput
	Dependencies           map[string]int64
	Fingerprints           []string
	DependencyFingerprints map[string]string
}

type TopicProjectionBatch struct {
	Work        TopicProjectionWork
	Sources     []CommunitySourceInput
	Memberships []CommunityMembershipInput
}

type TopicProjectionRepository interface {
	ClaimTopicProjection(context.Context, time.Time) (*TopicProjectionWork, error)
	AppendTopicProjection(context.Context, TopicProjectionBatch, time.Time) error
	FailTopicProjection(context.Context, TopicProjectionWork, string, time.Time) error
	TopicProjectionCoverage(context.Context, string) (TopicProjectionCoverage, error)
}
