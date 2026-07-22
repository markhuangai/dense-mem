package verifier

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/markhuangai/dense-mem/internal/domain"
)

const V2SemanticReviewResponseSchemaName = "dense_mem_v2_semantic_review_response"

type V2SemanticReviewRequest struct {
	RequestID                string                              `json:"request_id"`
	TeamID                   string                              `json:"-"`
	OwnerProfileID           string                              `json:"-"`
	Attempt                  int                                 `json:"-"`
	ValidationFeedback       []string                            `json:"-"`
	PreviousResponseHash     string                              `json:"-"`
	Evidence                 []V2SemanticReviewEvidence          `json:"evidence"`
	EntityMentions           []V2SemanticEntityMention           `json:"entity_mentions"`
	RelationshipObservations []V2SemanticRelationshipObservation `json:"relationship_observations"`
}

type V2SemanticReviewEvidence struct {
	EvidenceID              string `json:"evidence_id"`
	FragmentID              string `json:"-"`
	EvidenceIndex           int    `json:"-"`
	Content                 string `json:"content"`
	SourceID                string `json:"-"`
	SourceRevisionID        string `json:"-"`
	CurrentSourceRevisionID string `json:"-"`
}

type V2SemanticEntityMention struct {
	Ref             string                      `json:"ref"`
	Surface         string                      `json:"surface"`
	Kind            string                      `json:"kind"`
	EvidenceID      string                      `json:"evidence_id"`
	Start           int                         `json:"start"`
	End             int                         `json:"end"`
	Candidates      []V2SemanticEntityCandidate `json:"candidates"`
	IdentityContext map[string]any              `json:"-"`
}

type V2SemanticEntityCandidate struct {
	EntityID        string         `json:"entity_id"`
	CanonicalName   string         `json:"canonical_name"`
	Kind            string         `json:"kind"`
	IdentityContext map[string]any `json:"identity_context,omitempty"`
	TeamID          string         `json:"-"`
	Status          string         `json:"-"`
}

type V2SemanticRelationshipObservation struct {
	Ref                 string                          `json:"ref"`
	SubjectRef          string                          `json:"subject_ref"`
	OriginalPredicate   string                          `json:"original_predicate"`
	PredicateCandidates []V2SemanticPredicateCandidate  `json:"predicate_candidates"`
	ObjectRef           string                          `json:"object_ref,omitempty"`
	ObjectValue         *V2SemanticValueObservation     `json:"object_value,omitempty"`
	EvidenceID          string                          `json:"evidence_id"`
	Quote               string                          `json:"quote"`
	Start               int                             `json:"start"`
	End                 int                             `json:"end"`
	ValidFrom           *time.Time                      `json:"valid_from,omitempty"`
	ValidTo             *time.Time                      `json:"valid_to,omitempty"`
	CorrectionTarget    *V2RelationshipCorrectionTarget `json:"-"`
}

type V2RelationshipCorrectionTarget struct {
	RelationshipID  string
	ExpectedVersion int
}

type V2SemanticPredicateCandidate struct {
	PredicateKey        string   `json:"predicate_key"`
	Version             int      `json:"version"`
	AllowedSubjectKinds []string `json:"allowed_subject_kinds"`
	AllowedObjectKinds  []string `json:"allowed_object_kinds"`
	RelationshipKind    string   `json:"relationship_kind"`
	CurrentCardinality  string   `json:"current_cardinality"`
	LifecycleState      string   `json:"-"`
}

type V2SemanticValueObservation struct {
	Ref     string `json:"ref"`
	Type    string `json:"type"`
	Value   string `json:"value"`
	Display string `json:"display,omitempty"`
	Unit    string `json:"unit,omitempty"`
}

type V2SemanticReviewResponse struct {
	RequestID           string                               `json:"request_id"`
	SecuritySignals     []V2SemanticSecuritySignal           `json:"security_signals"`
	EntityResults       []V2SemanticEntityResult             `json:"entity_results"`
	RelationshipResults []V2SemanticRelationshipReviewResult `json:"relationship_results"`
}

type V2SemanticSecuritySignal struct {
	EvidenceID string `json:"evidence_id"`
	Kind       string `json:"kind"`
	Start      int    `json:"start"`
	End        int    `json:"end"`
}

type V2SemanticEntityResult struct {
	Ref               string  `json:"ref"`
	Action            string  `json:"action"`
	CandidateEntityID *string `json:"candidate_entity_id"`
	Confidence        float64 `json:"confidence"`
	Rationale         string  `json:"rationale"`
}

type V2SemanticRelationshipReviewResult struct {
	Ref             string  `json:"ref"`
	PredicateStatus string  `json:"predicate_status"`
	PredicateKey    *string `json:"predicate_key"`
	EvidenceVerdict string  `json:"evidence_verdict"`
	Confidence      float64 `json:"confidence"`
	Rationale       string  `json:"rationale"`
}

type V2SemanticValidationError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

func (e V2SemanticValidationError) Error() string {
	if e.Field == "" {
		return e.Message
	}
	return e.Field + ": " + e.Message
}

func DecodeV2SemanticReviewResponseJSON(raw []byte) (V2SemanticReviewResponse, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var response V2SemanticReviewResponse
	if err := decoder.Decode(&response); err != nil {
		return V2SemanticReviewResponse{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return V2SemanticReviewResponse{}, errors.New("semantic review response contains trailing JSON")
	}
	return response, nil
}

func PrepareV2SemanticReviewRequest(req V2SemanticReviewRequest) (V2SemanticReviewRequest, []V2SemanticValidationError) {
	req.RequestID = strings.TrimSpace(req.RequestID)
	req.TeamID = strings.TrimSpace(req.TeamID)
	req.OwnerProfileID = strings.TrimSpace(req.OwnerProfileID)
	req.PreviousResponseHash = strings.TrimSpace(req.PreviousResponseHash)
	prepared := req
	errs := validateV2SemanticReviewRequestShape(&prepared)
	if len(errs) > 0 {
		return prepared, errs
	}
	evidenceByID := v2SemanticEvidenceByID(prepared.Evidence)
	for i := range prepared.EntityMentions {
		mention := &prepared.EntityMentions[i]
		mention.Ref = strings.TrimSpace(mention.Ref)
		mention.Surface = strings.TrimSpace(mention.Surface)
		mention.Kind = strings.TrimSpace(mention.Kind)
		mention.EvidenceID = strings.TrimSpace(mention.EvidenceID)
		exact, err := v2SemanticExactSpanQuote(evidenceByID[mention.EvidenceID].Content, mention.Start, mention.End, mention.Surface)
		if err != nil {
			errs = append(errs, v2SemanticErr(fmt.Sprintf("entity_mentions[%d].surface", i), err.Error()))
			continue
		}
		mention.Surface = exact
		mention.Candidates = v2SemanticAuthorizedCandidates(prepared.TeamID, mention.Candidates)
	}
	for i := range prepared.RelationshipObservations {
		obs := &prepared.RelationshipObservations[i]
		obs.Ref = strings.TrimSpace(obs.Ref)
		obs.SubjectRef = strings.TrimSpace(obs.SubjectRef)
		obs.OriginalPredicate = strings.TrimSpace(obs.OriginalPredicate)
		obs.ObjectRef = strings.TrimSpace(obs.ObjectRef)
		obs.EvidenceID = strings.TrimSpace(obs.EvidenceID)
		obs.Quote = strings.TrimSpace(obs.Quote)
		if obs.CorrectionTarget != nil {
			obs.CorrectionTarget.RelationshipID = strings.TrimSpace(obs.CorrectionTarget.RelationshipID)
		}
		exact, err := v2SemanticExactSpanQuote(evidenceByID[obs.EvidenceID].Content, obs.Start, obs.End, obs.Quote)
		if err != nil {
			errs = append(errs, v2SemanticErr(fmt.Sprintf("relationship_observations[%d].quote", i), err.Error()))
			continue
		}
		obs.Quote = exact
		obs.PredicateCandidates = v2SemanticActivePredicates(*obs, prepared.EntityMentions)
	}
	return prepared, errs
}

func ValidateV2SemanticReviewResponse(req V2SemanticReviewRequest, resp V2SemanticReviewResponse) []V2SemanticValidationError {
	errs := validateV2SemanticReviewResponseShape(resp)
	if resp.RequestID != strings.TrimSpace(req.RequestID) {
		errs = append(errs, v2SemanticErr("request_id", fmt.Sprintf("expected %q", req.RequestID)))
	}
	evidenceByID := v2SemanticEvidenceByID(req.Evidence)
	for i, signal := range resp.SecuritySignals {
		evidence, ok := evidenceByID[strings.TrimSpace(signal.EvidenceID)]
		if !ok {
			errs = append(errs, v2SemanticErr(fmt.Sprintf("security_signals[%d].evidence_id", i), "unknown evidence_id"))
			continue
		}
		if !v2SemanticSecurityKindAllowed(signal.Kind) {
			errs = append(errs, v2SemanticErr(fmt.Sprintf("security_signals[%d].kind", i), "unsupported security signal kind"))
		}
		if _, err := v2SemanticExactSpanQuote(evidence.Content, signal.Start, signal.End, ""); err != nil {
			errs = append(errs, v2SemanticErr(fmt.Sprintf("security_signals[%d].span", i), err.Error()))
		}
	}
	errs = append(errs, validateV2SemanticEntityResults(req, resp.EntityResults)...)
	errs = append(errs, validateV2SemanticRelationshipResults(req, resp.RelationshipResults)...)
	return errs
}

func V2SemanticReviewResponseSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"request_id", "security_signals", "entity_results", "relationship_results"},
		"properties": map[string]any{
			"request_id":           map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
			"security_signals":     v2SemanticSecuritySignalSchema(),
			"entity_results":       v2SemanticEntityResultSchema(),
			"relationship_results": v2SemanticRelationshipResultSchema(),
		},
	}
}

func validateV2SemanticReviewRequestShape(req *V2SemanticReviewRequest) []V2SemanticValidationError {
	var errs []V2SemanticValidationError
	if req.RequestID == "" {
		errs = append(errs, v2SemanticErr("request_id", "is required"))
	}
	if req.TeamID == "" {
		errs = append(errs, v2SemanticErr("team_id", "is required"))
	}
	if len(req.Evidence) == 0 {
		errs = append(errs, v2SemanticErr("evidence", "is required"))
	}
	evidenceRefs := map[string]struct{}{}
	for i := range req.Evidence {
		item := &req.Evidence[i]
		item.EvidenceID = strings.TrimSpace(item.EvidenceID)
		item.FragmentID = strings.TrimSpace(item.FragmentID)
		item.SourceRevisionID = strings.TrimSpace(item.SourceRevisionID)
		item.CurrentSourceRevisionID = strings.TrimSpace(item.CurrentSourceRevisionID)
		if item.EvidenceID == "" {
			errs = append(errs, v2SemanticErr(fmt.Sprintf("evidence[%d].evidence_id", i), "is required"))
		}
		if _, exists := evidenceRefs[item.EvidenceID]; exists {
			errs = append(errs, v2SemanticErr(fmt.Sprintf("evidence[%d].evidence_id", i), "is duplicated"))
		}
		evidenceRefs[item.EvidenceID] = struct{}{}
		if strings.TrimSpace(item.Content) == "" {
			errs = append(errs, v2SemanticErr(fmt.Sprintf("evidence[%d].content", i), "is required"))
		}
		if item.CurrentSourceRevisionID != "" && item.SourceRevisionID != "" && item.CurrentSourceRevisionID != item.SourceRevisionID {
			errs = append(errs, v2SemanticErr(fmt.Sprintf("evidence[%d].source_revision_id", i), "is not current"))
		}
	}
	entityRefs := map[string]V2SemanticEntityMention{}
	for i := range req.EntityMentions {
		mention := &req.EntityMentions[i]
		mention.Ref = strings.TrimSpace(mention.Ref)
		mention.Kind = strings.TrimSpace(mention.Kind)
		mention.EvidenceID = strings.TrimSpace(mention.EvidenceID)
		if mention.Ref == "" {
			errs = append(errs, v2SemanticErr(fmt.Sprintf("entity_mentions[%d].ref", i), "is required"))
		}
		if _, exists := entityRefs[mention.Ref]; exists {
			errs = append(errs, v2SemanticErr(fmt.Sprintf("entity_mentions[%d].ref", i), "is duplicated"))
		}
		entityRefs[mention.Ref] = *mention
		if !v2SemanticOneOf(mention.Kind, domain.V2EntityKinds()...) {
			errs = append(errs, v2SemanticErr(fmt.Sprintf("entity_mentions[%d].kind", i), "is unsupported"))
		}
		if _, ok := evidenceRefs[mention.EvidenceID]; !ok {
			errs = append(errs, v2SemanticErr(fmt.Sprintf("entity_mentions[%d].evidence_id", i), "is unknown"))
		}
	}
	relationshipRefs := map[string]struct{}{}
	for i := range req.RelationshipObservations {
		obs := &req.RelationshipObservations[i]
		obs.Ref = strings.TrimSpace(obs.Ref)
		obs.SubjectRef = strings.TrimSpace(obs.SubjectRef)
		obs.ObjectRef = strings.TrimSpace(obs.ObjectRef)
		obs.EvidenceID = strings.TrimSpace(obs.EvidenceID)
		if obs.Ref == "" {
			errs = append(errs, v2SemanticErr(fmt.Sprintf("relationship_observations[%d].ref", i), "is required"))
		}
		if _, exists := relationshipRefs[obs.Ref]; exists {
			errs = append(errs, v2SemanticErr(fmt.Sprintf("relationship_observations[%d].ref", i), "is duplicated"))
		}
		relationshipRefs[obs.Ref] = struct{}{}
		if _, ok := entityRefs[obs.SubjectRef]; !ok {
			errs = append(errs, v2SemanticErr(fmt.Sprintf("relationship_observations[%d].subject_ref", i), "is unknown"))
		}
		if (obs.ObjectRef == "") == (obs.ObjectValue == nil) {
			errs = append(errs, v2SemanticErr(fmt.Sprintf("relationship_observations[%d].object", i), "requires exactly one object_ref or object_value"))
		}
		if obs.ObjectRef != "" {
			if _, ok := entityRefs[obs.ObjectRef]; !ok {
				errs = append(errs, v2SemanticErr(fmt.Sprintf("relationship_observations[%d].object_ref", i), "is unknown"))
			}
		}
		if obs.ObjectValue != nil && !v2SemanticOneOf(strings.TrimSpace(obs.ObjectValue.Type), domain.V2ValueTypes()...) {
			errs = append(errs, v2SemanticErr(fmt.Sprintf("relationship_observations[%d].object_value.type", i), "is unsupported"))
		}
		if obs.ValidFrom != nil && obs.ValidTo != nil && obs.ValidTo.Before(*obs.ValidFrom) {
			errs = append(errs, v2SemanticErr(fmt.Sprintf("relationship_observations[%d].valid_to", i), "must not be before valid_from"))
		}
		if _, ok := evidenceRefs[obs.EvidenceID]; !ok {
			errs = append(errs, v2SemanticErr(fmt.Sprintf("relationship_observations[%d].evidence_id", i), "is unknown"))
		}
	}
	return errs
}

func validateV2SemanticReviewResponseShape(resp V2SemanticReviewResponse) []V2SemanticValidationError {
	var errs []V2SemanticValidationError
	if strings.TrimSpace(resp.RequestID) == "" {
		errs = append(errs, v2SemanticErr("request_id", "is required"))
	}
	if resp.SecuritySignals == nil {
		errs = append(errs, v2SemanticErr("security_signals", "is required"))
	}
	if resp.EntityResults == nil {
		errs = append(errs, v2SemanticErr("entity_results", "is required"))
	}
	if resp.RelationshipResults == nil {
		errs = append(errs, v2SemanticErr("relationship_results", "is required"))
	}
	return errs
}

func validateV2SemanticEntityResults(req V2SemanticReviewRequest, results []V2SemanticEntityResult) []V2SemanticValidationError {
	expected := map[string]V2SemanticEntityMention{}
	for _, mention := range req.EntityMentions {
		expected[mention.Ref] = mention
	}
	seen := map[string]struct{}{}
	var errs []V2SemanticValidationError
	for i, result := range results {
		ref := strings.TrimSpace(result.Ref)
		mention, ok := expected[ref]
		if !ok {
			errs = append(errs, v2SemanticErr(fmt.Sprintf("entity_results[%d].ref", i), "is unknown"))
			continue
		}
		if _, exists := seen[ref]; exists {
			errs = append(errs, v2SemanticErr(fmt.Sprintf("entity_results[%d].ref", i), "is duplicated"))
		}
		seen[ref] = struct{}{}
		if result.Confidence < 0 || result.Confidence > 1 {
			errs = append(errs, v2SemanticErr(fmt.Sprintf("entity_results[%d].confidence", i), "must be between 0 and 1"))
		}
		if rationale := strings.TrimSpace(result.Rationale); rationale == "" || len(rationale) > 1000 {
			errs = append(errs, v2SemanticErr(fmt.Sprintf("entity_results[%d].rationale", i), "is required and must be bounded"))
		}
		switch strings.TrimSpace(result.Action) {
		case string(domain.V2EntityResolutionReuse):
			if result.CandidateEntityID == nil || strings.TrimSpace(*result.CandidateEntityID) == "" {
				errs = append(errs, v2SemanticErr(fmt.Sprintf("entity_results[%d].candidate_entity_id", i), "is required for reuse"))
				continue
			}
			if !v2SemanticCandidateAllowed(mention.Candidates, strings.TrimSpace(*result.CandidateEntityID)) {
				errs = append(errs, v2SemanticErr(fmt.Sprintf("entity_results[%d].candidate_entity_id", i), "is outside candidate allowlist"))
			}
		case string(domain.V2EntityResolutionCreate), string(domain.V2EntityResolutionAmbiguous):
			if result.CandidateEntityID != nil {
				errs = append(errs, v2SemanticErr(fmt.Sprintf("entity_results[%d].candidate_entity_id", i), "must be null"))
			}
		default:
			errs = append(errs, v2SemanticErr(fmt.Sprintf("entity_results[%d].action", i), "is unsupported"))
		}
	}
	for ref := range expected {
		if _, ok := seen[ref]; !ok {
			errs = append(errs, v2SemanticErr("entity_results", "missing result for "+ref))
		}
	}
	return errs
}

func validateV2SemanticRelationshipResults(req V2SemanticReviewRequest, results []V2SemanticRelationshipReviewResult) []V2SemanticValidationError {
	expected := map[string]V2SemanticRelationshipObservation{}
	for _, obs := range req.RelationshipObservations {
		expected[obs.Ref] = obs
	}
	seen := map[string]struct{}{}
	var errs []V2SemanticValidationError
	for i, result := range results {
		ref := strings.TrimSpace(result.Ref)
		obs, ok := expected[ref]
		if !ok {
			errs = append(errs, v2SemanticErr(fmt.Sprintf("relationship_results[%d].ref", i), "is unknown"))
			continue
		}
		if _, exists := seen[ref]; exists {
			errs = append(errs, v2SemanticErr(fmt.Sprintf("relationship_results[%d].ref", i), "is duplicated"))
		}
		seen[ref] = struct{}{}
		if result.Confidence < 0 || result.Confidence > 1 {
			errs = append(errs, v2SemanticErr(fmt.Sprintf("relationship_results[%d].confidence", i), "must be between 0 and 1"))
		}
		if rationale := strings.TrimSpace(result.Rationale); rationale == "" || len(rationale) > 1000 {
			errs = append(errs, v2SemanticErr(fmt.Sprintf("relationship_results[%d].rationale", i), "is required and must be bounded"))
		}
		if !v2SemanticOneOf(result.EvidenceVerdict, domain.V2VerificationVerdicts()...) {
			errs = append(errs, v2SemanticErr(fmt.Sprintf("relationship_results[%d].evidence_verdict", i), "is unsupported"))
		}
		switch strings.TrimSpace(result.PredicateStatus) {
		case "resolved":
			if result.PredicateKey == nil || strings.TrimSpace(*result.PredicateKey) == "" {
				errs = append(errs, v2SemanticErr(fmt.Sprintf("relationship_results[%d].predicate_key", i), "is required for resolved predicate"))
				continue
			}
			if !v2SemanticPredicateAllowed(obs.PredicateCandidates, strings.TrimSpace(*result.PredicateKey)) {
				errs = append(errs, v2SemanticErr(fmt.Sprintf("relationship_results[%d].predicate_key", i), "is outside predicate allowlist"))
			}
		case "needs_review":
			if result.PredicateKey != nil {
				errs = append(errs, v2SemanticErr(fmt.Sprintf("relationship_results[%d].predicate_key", i), "must be null when predicate_status is needs_review; use predicate_status resolved only when selecting a submitted predicate candidate"))
			}
		default:
			errs = append(errs, v2SemanticErr(fmt.Sprintf("relationship_results[%d].predicate_status", i), "is unsupported"))
		}
	}
	for ref := range expected {
		if _, ok := seen[ref]; !ok {
			errs = append(errs, v2SemanticErr("relationship_results", "missing result for "+ref))
		}
	}
	return errs
}

func v2SemanticEvidenceByID(evidence []V2SemanticReviewEvidence) map[string]V2SemanticReviewEvidence {
	out := make(map[string]V2SemanticReviewEvidence, len(evidence))
	for _, item := range evidence {
		out[strings.TrimSpace(item.EvidenceID)] = item
	}
	return out
}

func v2SemanticAuthorizedCandidates(teamID string, candidates []V2SemanticEntityCandidate) []V2SemanticEntityCandidate {
	out := make([]V2SemanticEntityCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		candidate.TeamID = strings.TrimSpace(candidate.TeamID)
		candidate.Status = strings.TrimSpace(candidate.Status)
		candidate.EntityID = strings.TrimSpace(candidate.EntityID)
		candidate.CanonicalName = strings.TrimSpace(candidate.CanonicalName)
		candidate.Kind = strings.TrimSpace(candidate.Kind)
		if candidate.TeamID != teamID || candidate.Status != string(domain.V2RelationshipStatusActive) || candidate.EntityID == "" {
			continue
		}
		out = append(out, candidate)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].EntityID < out[j].EntityID })
	return out
}

func v2SemanticActivePredicates(obs V2SemanticRelationshipObservation, mentions []V2SemanticEntityMention) []V2SemanticPredicateCandidate {
	kinds := v2SemanticEndpointKinds(obs, mentions)
	out := make([]V2SemanticPredicateCandidate, 0, len(obs.PredicateCandidates))
	seen := map[string]struct{}{}
	for _, candidate := range obs.PredicateCandidates {
		candidate.PredicateKey = strings.TrimSpace(candidate.PredicateKey)
		candidate.RelationshipKind = strings.TrimSpace(candidate.RelationshipKind)
		candidate.CurrentCardinality = strings.TrimSpace(candidate.CurrentCardinality)
		candidate.LifecycleState = strings.TrimSpace(candidate.LifecycleState)
		if candidate.LifecycleState == "" {
			candidate.LifecycleState = string(domain.V2PredicateLifecycleActive)
		}
		if candidate.PredicateKey == "" || candidate.Version < 1 || candidate.LifecycleState != string(domain.V2PredicateLifecycleActive) {
			continue
		}
		if !v2SemanticOneOf(candidate.RelationshipKind, domain.V2RelationshipKinds()...) || !v2SemanticOneOf(candidate.CurrentCardinality, domain.V2CurrentCardinalities()...) {
			continue
		}
		if !v2SemanticKindAllowed(kinds.subject, candidate.AllowedSubjectKinds) || !v2SemanticKindAllowed(kinds.object, candidate.AllowedObjectKinds) {
			continue
		}
		key := fmt.Sprintf("%s/%d", candidate.PredicateKey, candidate.Version)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, candidate)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].PredicateKey == out[j].PredicateKey {
			return out[i].Version < out[j].Version
		}
		return out[i].PredicateKey < out[j].PredicateKey
	})
	return out
}

type v2SemanticEndpointKind struct {
	subject string
	object  string
}

func v2SemanticEndpointKinds(obs V2SemanticRelationshipObservation, mentions []V2SemanticEntityMention) v2SemanticEndpointKind {
	out := v2SemanticEndpointKind{}
	for _, mention := range mentions {
		if mention.Ref == obs.SubjectRef {
			out.subject = mention.Kind
		}
		if mention.Ref == obs.ObjectRef {
			out.object = mention.Kind
		}
	}
	if obs.ObjectValue != nil {
		out.object = strings.TrimSpace(obs.ObjectValue.Type)
	}
	return out
}

func V2SemanticEvidenceSpan(content string, start int, end int) (string, error) {
	runes := []rune(content)
	if start < 0 || end <= start || end > len(runes) {
		return "", errors.New("span is invalid")
	}
	return string(runes[start:end]), nil
}

func v2SemanticExactSpanQuote(content string, start int, end int, quote string) (string, error) {
	exact, err := V2SemanticEvidenceSpan(content, start, end)
	if err != nil {
		return "", err
	}
	quote = strings.TrimSpace(quote)
	if quote == "" || quote == exact || v2SemanticWhitespaceEquivalent(exact, quote) {
		return exact, nil
	}
	return "", errors.New("quote does not match the original evidence span")
}

func v2SemanticWhitespaceEquivalent(a string, b string) bool {
	return strings.Join(strings.FieldsFunc(a, unicode.IsSpace), " ") == strings.Join(strings.FieldsFunc(b, unicode.IsSpace), " ")
}

func v2SemanticCandidateAllowed(candidates []V2SemanticEntityCandidate, entityID string) bool {
	for _, candidate := range candidates {
		if candidate.EntityID == entityID {
			return true
		}
	}
	return false
}

func v2SemanticPredicateAllowed(candidates []V2SemanticPredicateCandidate, key string) bool {
	for _, candidate := range candidates {
		if candidate.PredicateKey == key {
			return true
		}
	}
	return false
}

func v2SemanticKindAllowed(kind string, allowed []string) bool {
	for _, candidate := range allowed {
		if strings.TrimSpace(candidate) == kind {
			return true
		}
	}
	return false
}

func v2SemanticSecurityKindAllowed(kind string) bool {
	switch strings.TrimSpace(kind) {
	case "role_control_spoofing", "instruction_override", "prompt_secret_extraction", "tool_exfiltration", "obfuscated_instruction", "hidden_control_markup":
		return true
	default:
		return false
	}
}

func v2SemanticOneOf(value string, allowed ...string) bool {
	value = strings.TrimSpace(value)
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func v2SemanticErr(field string, message string) V2SemanticValidationError {
	return V2SemanticValidationError{Field: field, Message: message}
}

func v2SemanticSecuritySignalSchema() map[string]any {
	return map[string]any{
		"type":     "array",
		"maxItems": 64,
		"items": map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"evidence_id", "kind", "start", "end"},
			"properties": map[string]any{
				"evidence_id": map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
				"kind": map[string]any{"enum": []string{
					"role_control_spoofing",
					"instruction_override",
					"prompt_secret_extraction",
					"tool_exfiltration",
					"obfuscated_instruction",
					"hidden_control_markup",
				}},
				"start": map[string]any{"type": "integer", "minimum": 0},
				"end":   map[string]any{"type": "integer", "minimum": 0},
			},
		},
	}
}

func v2SemanticEntityResultSchema() map[string]any {
	return map[string]any{
		"type": "array",
		"items": map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"ref", "action", "candidate_entity_id", "confidence", "rationale"},
			"properties": map[string]any{
				"ref":                 map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
				"action":              map[string]any{"enum": domain.V2EntityResolutionActions()},
				"candidate_entity_id": map[string]any{"type": []string{"string", "null"}, "maxLength": 128},
				"confidence":          map[string]any{"type": "number", "minimum": 0, "maximum": 1},
				"rationale":           map[string]any{"type": "string", "minLength": 1, "maxLength": 1000},
			},
		},
	}
}

func v2SemanticRelationshipResultSchema() map[string]any {
	return map[string]any{
		"type": "array",
		"items": map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"ref", "predicate_status", "predicate_key", "evidence_verdict", "confidence", "rationale"},
			"properties": map[string]any{
				"ref":              map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
				"predicate_status": map[string]any{"enum": []string{"resolved", "needs_review"}},
				"predicate_key":    map[string]any{"type": []string{"string", "null"}, "maxLength": 128},
				"evidence_verdict": map[string]any{"enum": domain.V2VerificationVerdicts()},
				"confidence":       map[string]any{"type": "number", "minimum": 0, "maximum": 1},
				"rationale":        map[string]any{"type": "string", "minLength": 1, "maxLength": 1000},
			},
		},
	}
}
