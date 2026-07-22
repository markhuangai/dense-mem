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

type V2ProviderProposalRequest struct {
	RequestID            string                     `json:"request_id"`
	Attempt              int                        `json:"attempt,omitempty"`
	ValidationFeedback   []string                   `json:"validation_feedback,omitempty"`
	PreviousResponseHash string                     `json:"previous_response_hash,omitempty"`
	Evidence             []V2SemanticReviewEvidence `json:"evidence"`
	EntityHints          []map[string]any           `json:"entity_hints,omitempty"`
	RelationshipHints    []map[string]any           `json:"relationship_hints,omitempty"`
	PredicateOptions     []string                   `json:"predicate_options"`
}

type V2ProviderProposal struct {
	PredicateOptions      []string                         `json:"predicate_options"`
	EntityProposals       []V2ProviderEntityProposal       `json:"entity_proposals"`
	RelationshipProposals []V2ProviderRelationshipProposal `json:"relationship_proposals"`
}

type V2ProviderEvidenceSpan struct {
	EvidenceIndex int `json:"evidence_index"`
	Start         int `json:"start"`
	End           int `json:"end"`
}

type V2ProviderEntityProposal struct {
	Ref             string                   `json:"ref"`
	Name            string                   `json:"name"`
	EntityKind      string                   `json:"entity_kind,omitempty"`
	Aliases         []string                 `json:"aliases,omitempty"`
	KnownEntityID   *string                  `json:"known_entity_id,omitempty"`
	IdentityContext map[string]any           `json:"identity_context,omitempty"`
	Evidence        []V2ProviderEvidenceSpan `json:"evidence"`
}

type V2ProviderRelationshipProposal struct {
	ProposalID          string                      `json:"proposal_id"`
	SubjectRef          string                      `json:"subject_ref"`
	OriginalPredicate   string                      `json:"original_predicate"`
	PredicateCandidates []string                    `json:"predicate_candidates,omitempty"`
	ObjectRef           string                      `json:"object_ref,omitempty"`
	ObjectValue         *V2SemanticValueObservation `json:"object_value,omitempty"`
	Polarity            string                      `json:"polarity,omitempty"`
	Modality            string                      `json:"modality,omitempty"`
	Evidence            []V2ProviderEvidenceSpan    `json:"evidence"`
	ValidFrom           *string                     `json:"valid_from,omitempty"`
	ValidTo             *string                     `json:"valid_to,omitempty"`
	ClientComment       *string                     `json:"client_comment,omitempty"`
}

func DecodeV2ProviderProposalJSON(raw []byte) (V2ProviderProposal, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var proposal V2ProviderProposal
	if err := decoder.Decode(&proposal); err != nil {
		return V2ProviderProposal{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return V2ProviderProposal{}, errors.New("provider proposal response contains trailing JSON")
	}
	return proposal, nil
}

func PrepareV2ProviderProposalRequest(req V2ProviderProposalRequest) (V2ProviderProposalRequest, []V2SemanticValidationError) {
	req.RequestID = strings.TrimSpace(req.RequestID)
	req.PreviousResponseHash = strings.TrimSpace(req.PreviousResponseHash)
	req.PredicateOptions = v2ProviderTrimStringSlice(req.PredicateOptions)
	for i := range req.Evidence {
		req.Evidence[i].EvidenceID = strings.TrimSpace(req.Evidence[i].EvidenceID)
		req.Evidence[i].FragmentID = strings.TrimSpace(req.Evidence[i].FragmentID)
	}
	var errs []V2SemanticValidationError
	if req.RequestID == "" {
		errs = append(errs, v2SemanticErr("request_id", "is required"))
	}
	if len(req.Evidence) == 0 {
		errs = append(errs, v2SemanticErr("evidence", "is required"))
	}
	if len(req.PredicateOptions) == 0 {
		errs = append(errs, v2SemanticErr("predicate_options", "is required"))
	}
	evidenceIDs := map[string]struct{}{}
	evidenceIndexes := map[int]struct{}{}
	for i, evidence := range req.Evidence {
		if evidence.EvidenceID == "" {
			errs = append(errs, v2SemanticErr(fmt.Sprintf("evidence[%d].evidence_id", i), "is required"))
		}
		if _, exists := evidenceIDs[evidence.EvidenceID]; exists {
			errs = append(errs, v2SemanticErr(fmt.Sprintf("evidence[%d].evidence_id", i), "is duplicated"))
		}
		evidenceIDs[evidence.EvidenceID] = struct{}{}
		if _, exists := evidenceIndexes[evidence.EvidenceIndex]; exists {
			errs = append(errs, v2SemanticErr(fmt.Sprintf("evidence[%d].evidence_index", i), "is duplicated"))
		}
		evidenceIndexes[evidence.EvidenceIndex] = struct{}{}
		if strings.TrimSpace(evidence.Content) == "" {
			errs = append(errs, v2SemanticErr(fmt.Sprintf("evidence[%d].content", i), "is required"))
		}
	}
	return req, errs
}

func ValidateV2ProviderProposal(req V2ProviderProposalRequest, proposal V2ProviderProposal) []V2SemanticValidationError {
	req, errs := PrepareV2ProviderProposalRequest(req)
	if len(errs) > 0 {
		return errs
	}
	evidenceByIndex := map[int]V2SemanticReviewEvidence{}
	for _, evidence := range req.Evidence {
		evidenceByIndex[evidence.EvidenceIndex] = evidence
	}
	errs = append(errs, validateV2ProviderEntityProposals(evidenceByIndex, proposal.EntityProposals)...)
	errs = append(errs, validateV2ProviderRelationshipProposals(evidenceByIndex, proposal.EntityProposals, proposal.RelationshipProposals)...)
	return errs
}

func validateV2ProviderEntityProposals(
	evidenceByIndex map[int]V2SemanticReviewEvidence,
	entities []V2ProviderEntityProposal,
) []V2SemanticValidationError {
	seen := map[string]struct{}{}
	var errs []V2SemanticValidationError
	for i, entity := range entities {
		ref := strings.TrimSpace(entity.Ref)
		if ref == "" {
			errs = append(errs, v2SemanticErr(fmt.Sprintf("entity_proposals[%d].ref", i), "is required"))
		}
		if _, exists := seen[ref]; ref != "" && exists {
			errs = append(errs, v2SemanticErr(fmt.Sprintf("entity_proposals[%d].ref", i), "is duplicated"))
		}
		seen[ref] = struct{}{}
		if strings.TrimSpace(entity.Name) == "" {
			errs = append(errs, v2SemanticErr(fmt.Sprintf("entity_proposals[%d].name", i), "is required"))
		}
		if entity.EntityKind != "" && !v2SemanticOneOf(strings.TrimSpace(entity.EntityKind), domain.V2EntityKinds()...) {
			errs = append(errs, v2SemanticErr(fmt.Sprintf("entity_proposals[%d].entity_kind", i), "is unsupported"))
		}
		if len(entity.Evidence) == 0 {
			errs = append(errs, v2SemanticErr(fmt.Sprintf("entity_proposals[%d].evidence", i), "is required"))
		}
		for j, span := range entity.Evidence {
			if err := validateV2ProviderSpan(evidenceByIndex, span); err != nil {
				errs = append(errs, v2SemanticErr(fmt.Sprintf("entity_proposals[%d].evidence[%d]", i, j), err.Error()))
			}
		}
	}
	return errs
}

func validateV2ProviderRelationshipProposals(
	evidenceByIndex map[int]V2SemanticReviewEvidence,
	entities []V2ProviderEntityProposal,
	relationships []V2ProviderRelationshipProposal,
) []V2SemanticValidationError {
	entityRefs := map[string]struct{}{}
	for _, entity := range entities {
		if ref := strings.TrimSpace(entity.Ref); ref != "" {
			entityRefs[ref] = struct{}{}
		}
	}
	seen := map[string]struct{}{}
	var errs []V2SemanticValidationError
	for i, relationship := range relationships {
		ref := strings.TrimSpace(relationship.ProposalID)
		if ref == "" {
			errs = append(errs, v2SemanticErr(fmt.Sprintf("relationship_proposals[%d].proposal_id", i), "is required"))
		}
		if _, exists := seen[ref]; ref != "" && exists {
			errs = append(errs, v2SemanticErr(fmt.Sprintf("relationship_proposals[%d].proposal_id", i), "is duplicated"))
		}
		seen[ref] = struct{}{}
		if _, ok := entityRefs[strings.TrimSpace(relationship.SubjectRef)]; !ok {
			errs = append(errs, v2SemanticErr(fmt.Sprintf("relationship_proposals[%d].subject_ref", i), "is unknown"))
		}
		if strings.TrimSpace(relationship.OriginalPredicate) == "" {
			errs = append(errs, v2SemanticErr(fmt.Sprintf("relationship_proposals[%d].original_predicate", i), "is required"))
		}
		if (strings.TrimSpace(relationship.ObjectRef) == "") == (relationship.ObjectValue == nil) {
			errs = append(errs, v2SemanticErr(fmt.Sprintf("relationship_proposals[%d].object", i), "requires exactly one object_ref or object_value"))
		}
		if relationship.ObjectRef != "" {
			if _, ok := entityRefs[strings.TrimSpace(relationship.ObjectRef)]; !ok {
				errs = append(errs, v2SemanticErr(fmt.Sprintf("relationship_proposals[%d].object_ref", i), "is unknown"))
			}
		}
		if relationship.ObjectValue != nil && !v2SemanticOneOf(strings.TrimSpace(relationship.ObjectValue.Type), domain.V2ValueTypes()...) {
			errs = append(errs, v2SemanticErr(fmt.Sprintf("relationship_proposals[%d].object_value.type", i), "is unsupported"))
		}
		if relationship.Polarity != "" && !v2SemanticOneOf(strings.TrimSpace(relationship.Polarity), "+", "-") {
			errs = append(errs, v2SemanticErr(fmt.Sprintf("relationship_proposals[%d].polarity", i), "is unsupported"))
		}
		if relationship.Modality != "" && !v2SemanticOneOf(strings.TrimSpace(relationship.Modality), "statement", "question", "proposal", "speculation", "quoted") {
			errs = append(errs, v2SemanticErr(fmt.Sprintf("relationship_proposals[%d].modality", i), "is unsupported"))
		}
		validFrom, validFromErr := v2ProviderOptionalTime(relationship.ValidFrom)
		if validFromErr != nil {
			errs = append(errs, v2SemanticErr(fmt.Sprintf("relationship_proposals[%d].valid_from", i), validFromErr.Error()))
		}
		validTo, validToErr := v2ProviderOptionalTime(relationship.ValidTo)
		if validToErr != nil {
			errs = append(errs, v2SemanticErr(fmt.Sprintf("relationship_proposals[%d].valid_to", i), validToErr.Error()))
		}
		if validFromErr == nil && validToErr == nil && validFrom != nil && validTo != nil && validTo.Before(*validFrom) {
			errs = append(errs, v2SemanticErr(fmt.Sprintf("relationship_proposals[%d].valid_to", i), "must not be before valid_from"))
		}
		if len(relationship.Evidence) == 0 {
			errs = append(errs, v2SemanticErr(fmt.Sprintf("relationship_proposals[%d].evidence", i), "is required"))
		}
		for j, span := range relationship.Evidence {
			if err := validateV2ProviderSpan(evidenceByIndex, span); err != nil {
				errs = append(errs, v2SemanticErr(fmt.Sprintf("relationship_proposals[%d].evidence[%d]", i, j), err.Error()))
			}
		}
	}
	return errs
}

func validateV2ProviderSpan(evidenceByIndex map[int]V2SemanticReviewEvidence, span V2ProviderEvidenceSpan) error {
	evidence, ok := evidenceByIndex[span.EvidenceIndex]
	if !ok {
		return fmt.Errorf("evidence_index %d is unknown", span.EvidenceIndex)
	}
	if _, err := v2SemanticExactSpanQuote(evidence.Content, span.Start, span.End, ""); err != nil {
		return err
	}
	return nil
}

func v2ProviderOptionalTime(value *string) (*time.Time, error) {
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

func v2ProviderTrimStringSlice(values []string) []string {
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
