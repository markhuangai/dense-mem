package verifier

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
)

type ProviderProposalRequest struct {
	RequestID            string                   `json:"request_id"`
	Attempt              int                      `json:"attempt,omitempty"`
	ValidationFeedback   []string                 `json:"validation_feedback,omitempty"`
	PreviousResponseHash string                   `json:"previous_response_hash,omitempty"`
	Evidence             []SemanticReviewEvidence `json:"evidence"`
	EntityHints          []map[string]any         `json:"entity_hints,omitempty"`
	RelationshipHints    []map[string]any         `json:"relationship_hints,omitempty"`
	PredicateOptions     []string                 `json:"predicate_options"`
}

type ProviderProposal struct {
	PredicateOptions      []string                       `json:"predicate_options"`
	EntityProposals       []ProviderEntityProposal       `json:"entity_proposals"`
	RelationshipProposals []ProviderRelationshipProposal `json:"relationship_proposals"`
}

type ProviderEvidenceSpan struct {
	EvidenceIndex int `json:"evidence_index"`
	Start         int `json:"start"`
	End           int `json:"end"`
}

type ProviderEntityProposal struct {
	Ref             string                 `json:"ref"`
	Name            string                 `json:"name"`
	EntityKind      string                 `json:"entity_kind,omitempty"`
	Aliases         []string               `json:"aliases,omitempty"`
	KnownEntityID   *string                `json:"known_entity_id,omitempty"`
	IdentityContext map[string]any         `json:"identity_context,omitempty"`
	Evidence        []ProviderEvidenceSpan `json:"evidence"`
}

type ProviderRelationshipProposal struct {
	ProposalID          string                    `json:"proposal_id"`
	SubjectRef          string                    `json:"subject_ref"`
	OriginalPredicate   string                    `json:"original_predicate"`
	PredicateCandidates []string                  `json:"predicate_candidates,omitempty"`
	RelationshipKind    string                    `json:"relationship_kind"`
	ObjectRef           string                    `json:"object_ref,omitempty"`
	ObjectValue         *SemanticValueObservation `json:"object_value,omitempty"`
	Polarity            string                    `json:"polarity,omitempty"`
	Modality            string                    `json:"modality,omitempty"`
	Evidence            []ProviderEvidenceSpan    `json:"evidence"`
	ValidFrom           *string                   `json:"valid_from,omitempty"`
	ValidTo             *string                   `json:"valid_to,omitempty"`
	ClientComment       *string                   `json:"client_comment,omitempty"`
}

func DecodeProviderProposalJSON(raw []byte) (ProviderProposal, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var proposal ProviderProposal
	if err := decoder.Decode(&proposal); err != nil {
		return ProviderProposal{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ProviderProposal{}, errors.New("provider proposal response contains trailing JSON")
	}
	return proposal, nil
}

func PrepareProviderProposalRequest(req ProviderProposalRequest) (ProviderProposalRequest, []SemanticValidationError) {
	req.RequestID = strings.TrimSpace(req.RequestID)
	req.PreviousResponseHash = strings.TrimSpace(req.PreviousResponseHash)
	req.PredicateOptions = providerTrimStringSlice(req.PredicateOptions)
	for i := range req.Evidence {
		req.Evidence[i].EvidenceID = strings.TrimSpace(req.Evidence[i].EvidenceID)
		req.Evidence[i].FragmentID = strings.TrimSpace(req.Evidence[i].FragmentID)
	}
	var errs []SemanticValidationError
	if req.RequestID == "" {
		errs = append(errs, semanticErr("request_id", "is required"))
	}
	if len(req.Evidence) == 0 {
		errs = append(errs, semanticErr("evidence", "is required"))
	}
	if len(req.PredicateOptions) == 0 {
		errs = append(errs, semanticErr("predicate_options", "is required"))
	}
	evidenceIDs := map[string]struct{}{}
	evidenceIndexes := map[int]struct{}{}
	for i, evidence := range req.Evidence {
		if evidence.EvidenceID == "" {
			errs = append(errs, semanticErr(fmt.Sprintf("evidence[%d].evidence_id", i), "is required"))
		}
		if _, exists := evidenceIDs[evidence.EvidenceID]; exists {
			errs = append(errs, semanticErr(fmt.Sprintf("evidence[%d].evidence_id", i), "is duplicated"))
		}
		evidenceIDs[evidence.EvidenceID] = struct{}{}
		if _, exists := evidenceIndexes[evidence.EvidenceIndex]; exists {
			errs = append(errs, semanticErr(fmt.Sprintf("evidence[%d].evidence_index", i), "is duplicated"))
		}
		evidenceIndexes[evidence.EvidenceIndex] = struct{}{}
		if strings.TrimSpace(evidence.Content) == "" {
			errs = append(errs, semanticErr(fmt.Sprintf("evidence[%d].content", i), "is required"))
		}
	}
	return req, errs
}

func ValidateProviderProposal(req ProviderProposalRequest, proposal ProviderProposal) []SemanticValidationError {
	req, errs := PrepareProviderProposalRequest(req)
	if len(errs) > 0 {
		return errs
	}
	evidenceByIndex := map[int]SemanticReviewEvidence{}
	for _, evidence := range req.Evidence {
		evidenceByIndex[evidence.EvidenceIndex] = evidence
	}
	errs = append(errs, validateProviderEntityProposals(evidenceByIndex, proposal.EntityProposals)...)
	errs = append(errs, validateProviderRelationshipProposals(evidenceByIndex, proposal.EntityProposals, proposal.RelationshipProposals)...)
	return errs
}

func validateProviderEntityProposals(
	evidenceByIndex map[int]SemanticReviewEvidence,
	entities []ProviderEntityProposal,
) []SemanticValidationError {
	seen := map[string]struct{}{}
	var errs []SemanticValidationError
	for i, entity := range entities {
		ref := strings.TrimSpace(entity.Ref)
		if ref == "" {
			errs = append(errs, semanticErr(fmt.Sprintf("entity_proposals[%d].ref", i), "is required"))
		}
		if _, exists := seen[ref]; ref != "" && exists {
			errs = append(errs, semanticErr(fmt.Sprintf("entity_proposals[%d].ref", i), "is duplicated"))
		}
		seen[ref] = struct{}{}
		if strings.TrimSpace(entity.Name) == "" {
			errs = append(errs, semanticErr(fmt.Sprintf("entity_proposals[%d].name", i), "is required"))
		}
		if entity.EntityKind != "" && !semanticOneOf(strings.TrimSpace(entity.EntityKind), domain.EntityKinds()...) {
			errs = append(errs, semanticErr(fmt.Sprintf("entity_proposals[%d].entity_kind", i), "is unsupported"))
		}
		if len(entity.Evidence) == 0 {
			errs = append(errs, semanticErr(fmt.Sprintf("entity_proposals[%d].evidence", i), "is required"))
		}
		for j, span := range entity.Evidence {
			if err := validateProviderSpan(evidenceByIndex, span); err != nil {
				errs = append(errs, semanticErr(fmt.Sprintf("entity_proposals[%d].evidence[%d]", i, j), err.Error()))
			}
		}
	}
	return errs
}

func validateProviderRelationshipProposals(
	evidenceByIndex map[int]SemanticReviewEvidence,
	entities []ProviderEntityProposal,
	relationships []ProviderRelationshipProposal,
) []SemanticValidationError {
	entityRefs := map[string]struct{}{}
	for _, entity := range entities {
		if ref := strings.TrimSpace(entity.Ref); ref != "" {
			entityRefs[ref] = struct{}{}
		}
	}
	seen := map[string]struct{}{}
	var errs []SemanticValidationError
	for i, relationship := range relationships {
		ref := strings.TrimSpace(relationship.ProposalID)
		if ref == "" {
			errs = append(errs, semanticErr(fmt.Sprintf("relationship_proposals[%d].proposal_id", i), "is required"))
		}
		if _, exists := seen[ref]; ref != "" && exists {
			errs = append(errs, semanticErr(fmt.Sprintf("relationship_proposals[%d].proposal_id", i), "is duplicated"))
		}
		seen[ref] = struct{}{}
		if _, ok := entityRefs[strings.TrimSpace(relationship.SubjectRef)]; !ok {
			errs = append(errs, semanticErr(fmt.Sprintf("relationship_proposals[%d].subject_ref", i), "is unknown"))
		}
		if strings.TrimSpace(relationship.OriginalPredicate) == "" {
			errs = append(errs, semanticErr(fmt.Sprintf("relationship_proposals[%d].original_predicate", i), "is required"))
		}
		if len(providerTrimStringSlice(relationship.PredicateCandidates)) == 0 {
			errs = append(errs, semanticErr(fmt.Sprintf("relationship_proposals[%d].predicate_candidates", i), "is required"))
		}
		if !semanticOneOf(strings.TrimSpace(relationship.RelationshipKind), domain.RelationshipKinds()...) {
			errs = append(errs, semanticErr(fmt.Sprintf("relationship_proposals[%d].relationship_kind", i), "is unsupported"))
		}
		if (strings.TrimSpace(relationship.ObjectRef) == "") == (relationship.ObjectValue == nil) {
			errs = append(errs, semanticErr(fmt.Sprintf("relationship_proposals[%d].object", i), "requires exactly one object_ref or object_value"))
		}
		if relationship.ObjectRef != "" {
			if _, ok := entityRefs[strings.TrimSpace(relationship.ObjectRef)]; !ok {
				errs = append(errs, semanticErr(fmt.Sprintf("relationship_proposals[%d].object_ref", i), "is unknown"))
			}
		}
		if relationship.ObjectValue != nil && !semanticOneOf(strings.TrimSpace(relationship.ObjectValue.Type), domain.ValueTypes()...) {
			errs = append(errs, semanticErr(fmt.Sprintf("relationship_proposals[%d].object_value.type", i), "is unsupported"))
		}
		if relationship.Polarity != "" && !semanticOneOf(strings.TrimSpace(relationship.Polarity), "+", "-") {
			errs = append(errs, semanticErr(fmt.Sprintf("relationship_proposals[%d].polarity", i), "is unsupported"))
		}
		if relationship.Modality != "" && !semanticOneOf(strings.TrimSpace(relationship.Modality), "statement", "question", "proposal", "speculation", "quoted") {
			errs = append(errs, semanticErr(fmt.Sprintf("relationship_proposals[%d].modality", i), "is unsupported"))
		}
		validFrom, validFromErr := providerOptionalTime(relationship.ValidFrom)
		if validFromErr != nil {
			errs = append(errs, semanticErr(fmt.Sprintf("relationship_proposals[%d].valid_from", i), validFromErr.Error()))
		}
		validTo, validToErr := providerOptionalTime(relationship.ValidTo)
		if validToErr != nil {
			errs = append(errs, semanticErr(fmt.Sprintf("relationship_proposals[%d].valid_to", i), validToErr.Error()))
		}
		if validFromErr == nil && validToErr == nil && validFrom != nil && validTo != nil && validTo.Before(*validFrom) {
			errs = append(errs, semanticErr(fmt.Sprintf("relationship_proposals[%d].valid_to", i), "must not be before valid_from"))
		}
		if len(relationship.Evidence) == 0 {
			errs = append(errs, semanticErr(fmt.Sprintf("relationship_proposals[%d].evidence", i), "is required"))
		}
		for j, span := range relationship.Evidence {
			if err := validateProviderSpan(evidenceByIndex, span); err != nil {
				errs = append(errs, semanticErr(fmt.Sprintf("relationship_proposals[%d].evidence[%d]", i, j), err.Error()))
			}
		}
	}
	return errs
}

func validateProviderSpan(evidenceByIndex map[int]SemanticReviewEvidence, span ProviderEvidenceSpan) error {
	evidence, ok := evidenceByIndex[span.EvidenceIndex]
	if !ok {
		return fmt.Errorf("evidence_index %d is unknown", span.EvidenceIndex)
	}
	if _, err := semanticExactSpanQuote(evidence.Content, span.Start, span.End, ""); err != nil {
		return err
	}
	return nil
}

func providerOptionalTime(value *string) (*time.Time, error) {
	if value == nil {
		return nil, nil
	}
	text := strings.TrimSpace(*value)
	if text == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return nil, errors.New("must be RFC3339 timestamp")
	}
	parsed = parsed.UTC()
	return &parsed, nil
}

func providerTrimStringSlice(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
