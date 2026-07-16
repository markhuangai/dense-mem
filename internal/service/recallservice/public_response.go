package recallservice

import (
	"errors"
	"sort"
	"strings"

	"github.com/markhuangai/dense-mem/internal/domain"
)

const (
	DiscoveryGuidance = "If you are uncertain or have concerns about any specific part, call recall_memory again with a focused query. Pass known_evidence_ids and known_relationship_ids from this response to avoid repeated context, and pass expand_from_entity_ids when you want more context around specific entities. Use trace_memory for supporting evidence or history."

	MaxRecallEvidenceBytes        = 64 * 1024
	MaxDiscoveryPaths             = 5
	MaxDiscoveryPathRelationships = 2
)

type PublicRecallResponse struct {
	RecallID          string                  `json:"recall_id,omitempty"`
	Results           []PublicEvidenceContext `json:"results"`
	DiscoveryPaths    []PublicDiscoveryPath   `json:"discovery_paths"`
	DiscoveryGuidance string                  `json:"discovery_guidance"`
	RelatedHypotheses []any                   `json:"related_hypotheses"`
}

type PublicRecallEntity struct {
	EntityID string                    `json:"entity_id,omitempty"`
	Name     string                    `json:"name"`
	Kind     domain.SemanticEntityKind `json:"kind,omitempty"`
}

type PublicRecallObject struct {
	EntityID string                    `json:"entity_id,omitempty"`
	Name     string                    `json:"name,omitempty"`
	Kind     domain.SemanticEntityKind `json:"kind,omitempty"`
	Value    string                    `json:"value,omitempty"`
	Type     string                    `json:"type,omitempty"`
}

type PublicPathRelationship struct {
	RelationshipID string             `json:"relationship_id"`
	Subject        PublicRecallEntity `json:"subject"`
	Predicate      string             `json:"predicate"`
	Object         PublicRecallObject `json:"object"`
	Polarity       string             `json:"polarity,omitempty"`
}

type PublicDiscoveryPath struct {
	Relationships []PublicPathRelationship `json:"relationships"`
	EvidenceIDs   []string                 `json:"evidence_ids"`
}

type PublicEvidenceContext struct {
	EvidenceID string `json:"evidence_id"`
	Context    string `json:"context"`
}

func RenderPublicRecall(req RecallRequest, hits []RecallHit) (PublicRecallResponse, error) {
	response := PublicRecallResponse{
		Results:           []PublicEvidenceContext{},
		DiscoveryPaths:    []PublicDiscoveryPath{},
		DiscoveryGuidance: DiscoveryGuidance,
		RelatedHypotheses: []any{},
	}
	evidenceSeen := map[string]struct{}{}
	evidenceBytes := 0
	for _, hit := range hits {
		switch {
		case hit.Evidence != nil:
			appendEvidenceResult(&response, evidenceSeen, &evidenceBytes, *hit.Evidence)
		case len(hit.Evidences) > 0:
			for _, evidence := range hit.Evidences {
				appendEvidenceResult(&response, evidenceSeen, &evidenceBytes, evidence)
			}
		case hit.Fragment != nil:
			appendEvidenceResult(&response, evidenceSeen, &evidenceBytes, legacyEvidenceFromFragment(hit.Fragment))
		case hit.Fact != nil:
			appendEvidenceContextResult(&response, evidenceSeen, &evidenceBytes, PublicEvidenceContext{
				EvidenceID: hit.Fact.FactID,
				Context:    legacyFactContext(hit.Fact),
			})
		case hit.Claim != nil:
			appendEvidenceContextResult(&response, evidenceSeen, &evidenceBytes, PublicEvidenceContext{
				EvidenceID: hit.Claim.ClaimID,
				Context:    legacyClaimContext(hit.Claim),
			})
		case len(hit.Relationships) > 0 || len(hit.Supports) > 0:
			continue
		case hit.Relationship != nil:
			continue
		default:
			return PublicRecallResponse{}, errors.New("recall response: hit missing payload")
		}
	}
	response.DiscoveryPaths = publicDiscoveryPaths(hits, response.Results)
	return response, nil
}

func appendEvidenceResult(response *PublicRecallResponse, seen map[string]struct{}, usedBytes *int, evidence domain.SemanticEvidenceFragment) {
	appendEvidenceContextResult(response, seen, usedBytes, PublicEvidenceContext{
		EvidenceID: strings.TrimSpace(evidence.FragmentID),
		Context:    evidence.Content,
	})
}

func appendEvidenceContextResult(response *PublicRecallResponse, seen map[string]struct{}, usedBytes *int, evidence PublicEvidenceContext) {
	id := strings.TrimSpace(evidence.EvidenceID)
	if id == "" {
		return
	}
	if _, ok := seen[id]; ok {
		return
	}
	bytes := len(evidence.Context)
	if len(response.Results) > 0 && *usedBytes+bytes > MaxRecallEvidenceBytes {
		return
	}
	seen[id] = struct{}{}
	response.Results = append(response.Results, PublicEvidenceContext{EvidenceID: id, Context: evidence.Context})
	*usedBytes += bytes
}

func publicDiscoveryPaths(hits []RecallHit, results []PublicEvidenceContext) []PublicDiscoveryPath {
	if len(hits) == 0 {
		return []PublicDiscoveryPath{}
	}
	resultIDs := make(map[string]struct{}, len(results))
	for _, result := range results {
		resultIDs[result.EvidenceID] = struct{}{}
	}
	out := make([]PublicDiscoveryPath, 0, MaxDiscoveryPaths)
	seen := map[string]struct{}{}
	for _, hit := range hits {
		if len(out) == MaxDiscoveryPaths {
			break
		}
		evidenceIDs := pathEvidenceIDs(hit, resultIDs)
		if len(evidenceIDs) == 0 {
			continue
		}
		relationships := pathRelationships(hit)
		if len(relationships) == 0 {
			continue
		}
		key := strings.Join(evidenceIDs, ",") + "|" + pathRelationshipKey(relationships)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, PublicDiscoveryPath{
			Relationships: relationships,
			EvidenceIDs:   evidenceIDs,
		})
	}
	return out
}

func pathEvidenceIDs(hit RecallHit, allowed map[string]struct{}) []string {
	seen := map[string]struct{}{}
	ids := []string{}
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if hit.Evidence != nil {
		add(hit.Evidence.FragmentID)
	}
	for _, evidence := range hit.Evidences {
		add(evidence.FragmentID)
	}
	for _, support := range hit.Supports {
		add(support.FragmentID)
	}
	sortEvidenceIDs(ids, allowed)
	return ids
}

func sortEvidenceIDs(ids []string, resultIDs map[string]struct{}) {
	sort.SliceStable(ids, func(i, j int) bool {
		_, iResult := resultIDs[ids[i]]
		_, jResult := resultIDs[ids[j]]
		if iResult != jResult {
			return iResult
		}
		return ids[i] < ids[j]
	})
}

func pathRelationships(hit RecallHit) []PublicPathRelationship {
	relationships := make([]PublicPathRelationship, 0, MaxDiscoveryPathRelationships)
	seen := map[string]struct{}{}
	add := func(rel domain.SemanticRelationship) {
		id := strings.TrimSpace(rel.RelationshipID)
		if id == "" {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		relationships = append(relationships, publicPathRelationship(rel))
	}
	for _, rel := range hit.Relationships {
		if len(relationships) == MaxDiscoveryPathRelationships {
			break
		}
		add(rel)
	}
	if hit.Relationship != nil && len(relationships) < MaxDiscoveryPathRelationships {
		add(*hit.Relationship)
	}
	return relationships
}

func pathRelationshipKey(relationships []PublicPathRelationship) string {
	ids := make([]string, 0, len(relationships))
	for _, rel := range relationships {
		ids = append(ids, rel.RelationshipID)
	}
	return strings.Join(ids, ",")
}

func publicPathRelationship(rel domain.SemanticRelationship) PublicPathRelationship {
	return PublicPathRelationship{
		RelationshipID: rel.RelationshipID,
		Subject: PublicRecallEntity{
			EntityID: rel.SubjectEntityID,
			Name:     rel.SubjectEntityName,
			Kind:     rel.SubjectEntityKind,
		},
		Predicate: rel.Predicate,
		Object: PublicRecallObject{
			EntityID: rel.ObjectEntityID,
			Name:     rel.ObjectEntityName,
			Kind:     rel.ObjectEntityKind,
			Value:    rel.ObjectValue,
			Type:     rel.ObjectKind,
		},
		Polarity: string(rel.Polarity),
	}
}

func legacyEvidenceFromFragment(fragment *domain.Fragment) domain.SemanticEvidenceFragment {
	return domain.SemanticEvidenceFragment{
		FragmentID:       fragment.FragmentID,
		OwnerProfileID:   firstNonEmpty(fragment.OwnerProfileID, fragment.ProfileID),
		OwnerProfileName: fragment.OwnerProfileName,
		Content:          fragment.Content,
		Source:           fragment.Source,
		SourceType:       fragment.SourceType,
		Authority:        fragment.Authority,
		Labels:           append([]string(nil), fragment.Labels...),
		CreatedAt:        fragment.CreatedAt,
	}
}

func legacyFactContext(fact *domain.Fact) string {
	return strings.TrimSpace(strings.Join([]string{fact.Subject, fact.Predicate, fact.Object}, " "))
}

func legacyClaimContext(claim *domain.Claim) string {
	return strings.TrimSpace(strings.Join([]string{claim.Subject, claim.Predicate, claim.Object}, " "))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
