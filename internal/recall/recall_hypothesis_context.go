package recall

import (
	"strings"

	dreamcontract "github.com/markhuangai/dense-mem/internal/dream/contract"
)

type recallHypothesisContextHandles struct {
	evidenceIDs      []string
	relationshipIDs  []string
	entityIDs        []string
	valueIDs         []string
	evidenceSeen     map[string]struct{}
	relationshipSeen map[string]struct{}
	entitySeen       map[string]struct{}
	valueSeen        map[string]struct{}
}

func recallHypothesisContextFrom(result *RecallResult) recallHypothesisContextHandles {
	context := recallHypothesisContextHandles{
		evidenceIDs:      []string{},
		relationshipIDs:  []string{},
		entityIDs:        []string{},
		valueIDs:         []string{},
		evidenceSeen:     map[string]struct{}{},
		relationshipSeen: map[string]struct{}{},
		entitySeen:       map[string]struct{}{},
		valueSeen:        map[string]struct{}{},
	}
	if result == nil {
		return context
	}

	for _, evidence := range result.Results {
		context.addEvidence(evidence.EvidenceID)
		context.addRelationships(evidence.RelationshipIDs...)
	}
	for _, relationship := range result.RelatedRelationships {
		context.addRelationshipSummary(relationship)
	}
	for _, path := range append(append([]RecallDiscoveryPath(nil), result.RelatedCommunities...), result.DiscoveryPaths...) {
		for _, evidenceID := range path.EvidenceIDs {
			context.addEvidence(evidenceID)
		}
		for _, entity := range path.TopEntities {
			context.addEntity(entity.EntityID)
		}
		for _, relationship := range path.CommunityRelationships {
			context.addRelationshipSummary(relationship)
		}
		for _, relationship := range path.Relationships {
			context.addRelationship(relationship)
		}
	}
	return context
}

func (c recallHypothesisContextHandles) empty() bool {
	return len(c.evidenceIDs) == 0 && len(c.relationshipIDs) == 0 && len(c.entityIDs) == 0 && len(c.valueIDs) == 0
}

func (c *recallHypothesisContextHandles) addRelationshipSummary(relationship RelatedRelationshipSummary) {
	c.addRelationships(relationship.RelationshipID)
	c.addRelationships(relationship.EquivalentRelationshipIDs...)
	c.addEvidence(relationship.EvidenceIDs...)
	c.addEntity(relationship.Subject.EntityID)
	c.addEntity(relationship.Object.EntityID)
	c.addValues(relationship.Object.ValueID)
}

func (c *recallHypothesisContextHandles) addRelationship(relationship RecallRelationshipHandle) {
	c.addRelationships(relationship.RelationshipID)
	c.addEntity(relationship.Subject.EntityID)
	c.addEntity(relationship.Object.EntityID)
	c.addValues(relationship.Object.ValueID)
}

func (c *recallHypothesisContextHandles) addEvidence(values ...string) {
	for _, value := range values {
		appendRecallHypothesisHandle(&c.evidenceIDs, c.evidenceSeen, value)
	}
}

func (c *recallHypothesisContextHandles) addRelationships(values ...string) {
	for _, value := range values {
		appendRecallHypothesisHandle(&c.relationshipIDs, c.relationshipSeen, value)
	}
}

func (c *recallHypothesisContextHandles) addEntity(values ...string) {
	for _, value := range values {
		appendRecallHypothesisHandle(&c.entityIDs, c.entitySeen, value)
	}
}

func (c *recallHypothesisContextHandles) addValues(values ...string) {
	for _, value := range values {
		appendRecallHypothesisHandle(&c.valueIDs, c.valueSeen, value)
	}
}

func appendRecallHypothesisHandle(values *[]string, seen map[string]struct{}, value string) {
	value = strings.TrimSpace(value)
	if value == "" || len(*values) >= dreamcontract.MaxRecallHypothesisContextIDs {
		return
	}
	if _, exists := seen[value]; exists {
		return
	}
	seen[value] = struct{}{}
	*values = append(*values, value)
}
