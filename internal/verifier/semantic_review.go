package verifier

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/markhuangai/dense-mem/internal/domain"
)

const SemanticReviewResponseSchemaName = "dense_mem_v2_semantic_review_response"

var semanticHiddenControlMarkupPattern = regexp.MustCompile(`(?is)<!--|<\s*(?:script|iframe|object|embed|meta|svg)\b|\bon[a-z]{3,32}\s*=`)

type SemanticReviewRequest struct {
	RequestID                string                            `json:"request_id"`
	TeamID                   string                            `json:"-"`
	OwnerProfileID           string                            `json:"-"`
	Attempt                  int                               `json:"attempt,omitempty"`
	ValidationFeedback       []string                          `json:"validation_feedback,omitempty"`
	PreviousResponseHash     string                            `json:"previous_response_hash,omitempty"`
	Evidence                 []SemanticReviewEvidence          `json:"evidence"`
	EntityMentions           []SemanticEntityMention           `json:"entity_mentions"`
	RelationshipObservations []SemanticRelationshipObservation `json:"relationship_observations"`
}

type SemanticReviewEvidence struct {
	EvidenceID              string         `json:"evidence_id"`
	FragmentID              string         `json:"-"`
	EvidenceIndex           int            `json:"-"`
	Content                 string         `json:"content"`
	BoundaryText            string         `json:"boundary_text,omitempty"`
	BoundaryRefs            map[string]int `json:"-"`
	Authority               string         `json:"-"`
	SourceID                string         `json:"-"`
	SourceRevisionID        string         `json:"-"`
	CurrentSourceRevisionID string         `json:"-"`
}

type SemanticEntityMention struct {
	Ref             string                    `json:"ref"`
	Surface         string                    `json:"surface"`
	Kind            string                    `json:"kind"`
	EvidenceID      string                    `json:"evidence_id"`
	Start           int                       `json:"start"`
	End             int                       `json:"end"`
	Candidates      []SemanticEntityCandidate `json:"candidates"`
	IdentityContext map[string]any            `json:"-"`
}

type SemanticEntityCandidate struct {
	EntityID        string         `json:"entity_id"`
	CanonicalName   string         `json:"canonical_name"`
	Kind            string         `json:"kind"`
	IdentityContext map[string]any `json:"identity_context,omitempty"`
	TeamID          string         `json:"-"`
	Status          string         `json:"-"`
}

type SemanticRelationshipObservation struct {
	Ref                 string                        `json:"ref"`
	SubjectRef          string                        `json:"subject_ref"`
	OriginalPredicate   string                        `json:"original_predicate"`
	Polarity            string                        `json:"polarity"`
	PredicateCandidates []SemanticPredicateCandidate  `json:"predicate_candidates"`
	ObjectRef           string                        `json:"object_ref,omitempty"`
	ObjectValue         *SemanticValueObservation     `json:"object_value,omitempty"`
	EvidenceID          string                        `json:"evidence_id"`
	Quote               string                        `json:"quote"`
	Start               int                           `json:"start"`
	End                 int                           `json:"end"`
	ValidFrom           *time.Time                    `json:"valid_from,omitempty"`
	ValidTo             *time.Time                    `json:"valid_to,omitempty"`
	CorrectionTarget    *RelationshipCorrectionTarget `json:"-"`
	ConflictContext     *RelationshipConflictContext  `json:"-"`
}

type RelationshipCorrectionTarget struct {
	RelationshipID  string
	ExpectedVersion int
}

type RelationshipConflictContext struct {
	ConflictID      string
	ExpectedVersion int
}

type SemanticPredicateCandidate struct {
	PredicateKey        string   `json:"predicate_key"`
	Version             int      `json:"version"`
	AllowedSubjectKinds []string `json:"allowed_subject_kinds"`
	AllowedObjectKinds  []string `json:"allowed_object_kinds"`
	RelationshipKind    string   `json:"relationship_kind"`
	CurrentCardinality  string   `json:"current_cardinality"`
	LifecycleState      string   `json:"-"`
}

type SemanticValueObservation struct {
	Ref     string `json:"ref"`
	Type    string `json:"type"`
	Value   string `json:"value"`
	Display string `json:"display,omitempty"`
	Unit    string `json:"unit,omitempty"`
}

type SemanticReviewResponse struct {
	RequestID           string                             `json:"request_id"`
	SecuritySignals     []SemanticSecuritySignal           `json:"security_signals"`
	EntityResults       []SemanticEntityResult             `json:"entity_results"`
	RelationshipResults []SemanticRelationshipReviewResult `json:"relationship_results"`
}

type SemanticSecuritySignal struct {
	EvidenceID string `json:"evidence_id"`
	Kind       string `json:"kind"`
	Start      int    `json:"start"`
	End        int    `json:"end"`
}

type SemanticEntityResult struct {
	Ref               string  `json:"ref"`
	Action            string  `json:"action"`
	CandidateEntityID *string `json:"candidate_entity_id"`
	Confidence        float64 `json:"confidence"`
	Rationale         string  `json:"rationale"`
}

type SemanticRelationshipReviewResult struct {
	Ref             string  `json:"ref"`
	PredicateStatus string  `json:"predicate_status"`
	PredicateKey    *string `json:"predicate_key"`
	EvidenceVerdict string  `json:"evidence_verdict"`
	Confidence      float64 `json:"confidence"`
	Rationale       string  `json:"rationale"`
}

type SemanticValidationError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

func (e SemanticValidationError) Error() string {
	if e.Field == "" {
		return e.Message
	}
	return e.Field + ": " + e.Message
}

func DecodeSemanticReviewResponseJSON(raw []byte) (SemanticReviewResponse, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var response SemanticReviewResponse
	if err := decoder.Decode(&response); err != nil {
		return SemanticReviewResponse{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return SemanticReviewResponse{}, errors.New("semantic review response contains trailing JSON")
	}
	return response, nil
}

func PrepareSemanticReviewRequest(req SemanticReviewRequest) (SemanticReviewRequest, []SemanticValidationError) {
	req.RequestID = strings.TrimSpace(req.RequestID)
	req.TeamID = strings.TrimSpace(req.TeamID)
	req.OwnerProfileID = strings.TrimSpace(req.OwnerProfileID)
	req.PreviousResponseHash = strings.TrimSpace(req.PreviousResponseHash)
	prepared := req
	errs := validateSemanticReviewRequestShape(&prepared)
	if len(errs) > 0 {
		return prepared, errs
	}
	evidenceByID := semanticEvidenceByID(prepared.Evidence)
	for i := range prepared.EntityMentions {
		mention := &prepared.EntityMentions[i]
		mention.Ref = strings.TrimSpace(mention.Ref)
		mention.Surface = strings.TrimSpace(mention.Surface)
		mention.Kind = strings.TrimSpace(mention.Kind)
		mention.EvidenceID = strings.TrimSpace(mention.EvidenceID)
		exact, err := semanticExactSpanQuote(evidenceByID[mention.EvidenceID].Content, mention.Start, mention.End, mention.Surface)
		if err != nil {
			errs = append(errs, semanticErr(fmt.Sprintf("entity_mentions[%d].surface", i), err.Error()))
			continue
		}
		mention.Surface = exact
		mention.Candidates = semanticAuthorizedCandidates(prepared.TeamID, mention.Candidates)
	}
	for i := range prepared.RelationshipObservations {
		obs := &prepared.RelationshipObservations[i]
		obs.Ref = strings.TrimSpace(obs.Ref)
		obs.SubjectRef = strings.TrimSpace(obs.SubjectRef)
		obs.OriginalPredicate = strings.TrimSpace(obs.OriginalPredicate)
		obs.Polarity = strings.TrimSpace(obs.Polarity)
		if obs.Polarity == "" {
			obs.Polarity = "+"
		}
		obs.ObjectRef = strings.TrimSpace(obs.ObjectRef)
		obs.EvidenceID = strings.TrimSpace(obs.EvidenceID)
		obs.Quote = strings.TrimSpace(obs.Quote)
		if obs.CorrectionTarget != nil {
			obs.CorrectionTarget.RelationshipID = strings.TrimSpace(obs.CorrectionTarget.RelationshipID)
		}
		if obs.ConflictContext != nil {
			obs.ConflictContext.ConflictID = strings.TrimSpace(obs.ConflictContext.ConflictID)
		}
		exact, err := semanticExactSpanQuote(evidenceByID[obs.EvidenceID].Content, obs.Start, obs.End, obs.Quote)
		if err != nil {
			errs = append(errs, semanticErr(fmt.Sprintf("relationship_observations[%d].quote", i), err.Error()))
			continue
		}
		obs.Quote = exact
		obs.PredicateCandidates = semanticActivePredicates(*obs, prepared.EntityMentions)
	}
	return prepared, errs
}

func ValidateSemanticReviewResponse(req SemanticReviewRequest, resp SemanticReviewResponse) []SemanticValidationError {
	errs := validateSemanticReviewResponseShape(resp)
	if resp.RequestID != strings.TrimSpace(req.RequestID) {
		errs = append(errs, semanticErr("request_id", fmt.Sprintf("expected %q", req.RequestID)))
	}
	evidenceByID := semanticEvidenceByID(req.Evidence)
	for i, signal := range resp.SecuritySignals {
		evidence, ok := evidenceByID[strings.TrimSpace(signal.EvidenceID)]
		if !ok {
			errs = append(errs, semanticErr(fmt.Sprintf("security_signals[%d].evidence_id", i), "unknown evidence_id"))
			continue
		}
		if !semanticSecurityKindAllowed(signal.Kind) {
			errs = append(errs, semanticErr(fmt.Sprintf("security_signals[%d].kind", i), "unsupported security signal kind"))
		}
		quote, err := semanticExactSpanQuote(evidence.Content, signal.Start, signal.End, "")
		if err != nil {
			errs = append(errs, semanticErr(fmt.Sprintf("security_signals[%d].span", i), err.Error()))
		} else if !semanticSecuritySignalSpanMatchesKind(signal.Kind, quote) {
			errs = append(errs, semanticErr(fmt.Sprintf("security_signals[%d].span", i), "hidden_control_markup requires a hidden control or active markup"))
		}
	}
	errs = append(errs, validateSemanticEntityResults(req, resp.EntityResults)...)
	errs = append(errs, validateSemanticRelationshipResults(req, resp.RelationshipResults)...)
	return errs
}

func semanticSecuritySignalSpanMatchesKind(kind, quote string) bool {
	if strings.TrimSpace(kind) != "hidden_control_markup" {
		return true
	}
	if semanticHiddenControlMarkupPattern.MatchString(quote) {
		return true
	}
	for _, value := range quote {
		if semanticHiddenControlRune(value) {
			return true
		}
	}
	return false
}

func semanticHiddenControlRune(value rune) bool {
	return value == '\u200b' || value == '\u200c' || value == '\u200d' || value == '\u200e' || value == '\u200f' ||
		value == '\u2060' || value >= '\u202a' && value <= '\u202e' || value >= '\u2066' && value <= '\u2069' ||
		unicode.IsControl(value) && value != '\n' && value != '\r' && value != '\t'
}

func SemanticReviewResponseSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"request_id", "security_signals", "entity_results", "relationship_results"},
		"properties": map[string]any{
			"request_id":           map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
			"security_signals":     semanticSecuritySignalSchema(),
			"entity_results":       semanticEntityResultSchema(),
			"relationship_results": semanticRelationshipResultSchema(),
		},
	}
}

func validateSemanticReviewRequestShape(req *SemanticReviewRequest) []SemanticValidationError {
	var errs []SemanticValidationError
	if req.RequestID == "" {
		errs = append(errs, semanticErr("request_id", "is required"))
	}
	if req.TeamID == "" {
		errs = append(errs, semanticErr("team_id", "is required"))
	}
	if len(req.Evidence) == 0 {
		errs = append(errs, semanticErr("evidence", "is required"))
	}
	evidenceRefs := map[string]struct{}{}
	for i := range req.Evidence {
		item := &req.Evidence[i]
		item.EvidenceID = strings.TrimSpace(item.EvidenceID)
		item.FragmentID = strings.TrimSpace(item.FragmentID)
		item.SourceRevisionID = strings.TrimSpace(item.SourceRevisionID)
		item.CurrentSourceRevisionID = strings.TrimSpace(item.CurrentSourceRevisionID)
		if item.EvidenceID == "" {
			errs = append(errs, semanticErr(fmt.Sprintf("evidence[%d].evidence_id", i), "is required"))
		}
		if _, exists := evidenceRefs[item.EvidenceID]; exists {
			errs = append(errs, semanticErr(fmt.Sprintf("evidence[%d].evidence_id", i), "is duplicated"))
		}
		evidenceRefs[item.EvidenceID] = struct{}{}
		if strings.TrimSpace(item.Content) == "" {
			errs = append(errs, semanticErr(fmt.Sprintf("evidence[%d].content", i), "is required"))
		}
		if item.CurrentSourceRevisionID != "" && item.SourceRevisionID != "" && item.CurrentSourceRevisionID != item.SourceRevisionID {
			errs = append(errs, semanticErr(fmt.Sprintf("evidence[%d].source_revision_id", i), "is not current"))
		}
	}
	entityRefs := map[string]SemanticEntityMention{}
	for i := range req.EntityMentions {
		mention := &req.EntityMentions[i]
		mention.Ref = strings.TrimSpace(mention.Ref)
		mention.Kind = strings.TrimSpace(mention.Kind)
		mention.EvidenceID = strings.TrimSpace(mention.EvidenceID)
		if mention.Ref == "" {
			errs = append(errs, semanticErr(fmt.Sprintf("entity_mentions[%d].ref", i), "is required"))
		}
		if _, exists := entityRefs[mention.Ref]; exists {
			errs = append(errs, semanticErr(fmt.Sprintf("entity_mentions[%d].ref", i), "is duplicated"))
		}
		entityRefs[mention.Ref] = *mention
		if !semanticOneOf(mention.Kind, domain.EntityKinds()...) {
			errs = append(errs, semanticErr(fmt.Sprintf("entity_mentions[%d].kind", i), "is unsupported"))
		}
		if _, ok := evidenceRefs[mention.EvidenceID]; !ok {
			errs = append(errs, semanticErr(fmt.Sprintf("entity_mentions[%d].evidence_id", i), "is unknown"))
		}
	}
	relationshipRefs := map[string]struct{}{}
	for i := range req.RelationshipObservations {
		obs := &req.RelationshipObservations[i]
		obs.Ref = strings.TrimSpace(obs.Ref)
		obs.SubjectRef = strings.TrimSpace(obs.SubjectRef)
		obs.ObjectRef = strings.TrimSpace(obs.ObjectRef)
		obs.EvidenceID = strings.TrimSpace(obs.EvidenceID)
		obs.Polarity = strings.TrimSpace(obs.Polarity)
		if obs.Polarity == "" {
			obs.Polarity = "+"
		}
		if obs.Ref == "" {
			errs = append(errs, semanticErr(fmt.Sprintf("relationship_observations[%d].ref", i), "is required"))
		}
		if _, exists := relationshipRefs[obs.Ref]; exists {
			errs = append(errs, semanticErr(fmt.Sprintf("relationship_observations[%d].ref", i), "is duplicated"))
		}
		relationshipRefs[obs.Ref] = struct{}{}
		if _, ok := entityRefs[obs.SubjectRef]; !ok {
			errs = append(errs, semanticErr(fmt.Sprintf("relationship_observations[%d].subject_ref", i), "is unknown"))
		}
		if (obs.ObjectRef == "") == (obs.ObjectValue == nil) {
			errs = append(errs, semanticErr(fmt.Sprintf("relationship_observations[%d].object", i), "requires exactly one object_ref or object_value"))
		}
		if obs.ObjectRef != "" {
			if _, ok := entityRefs[obs.ObjectRef]; !ok {
				errs = append(errs, semanticErr(fmt.Sprintf("relationship_observations[%d].object_ref", i), "is unknown"))
			}
		}
		if obs.ObjectValue != nil && !semanticOneOf(strings.TrimSpace(obs.ObjectValue.Type), domain.ValueTypes()...) {
			errs = append(errs, semanticErr(fmt.Sprintf("relationship_observations[%d].object_value.type", i), "is unsupported"))
		}
		if !semanticOneOf(obs.Polarity, "+", "-") {
			errs = append(errs, semanticErr(fmt.Sprintf("relationship_observations[%d].polarity", i), "is unsupported"))
		}
		if obs.ValidFrom != nil && obs.ValidTo != nil && obs.ValidTo.Before(*obs.ValidFrom) {
			errs = append(errs, semanticErr(fmt.Sprintf("relationship_observations[%d].valid_to", i), "must not be before valid_from"))
		}
		if _, ok := evidenceRefs[obs.EvidenceID]; !ok {
			errs = append(errs, semanticErr(fmt.Sprintf("relationship_observations[%d].evidence_id", i), "is unknown"))
		}
	}
	return errs
}

func validateSemanticReviewResponseShape(resp SemanticReviewResponse) []SemanticValidationError {
	var errs []SemanticValidationError
	if strings.TrimSpace(resp.RequestID) == "" {
		errs = append(errs, semanticErr("request_id", "is required"))
	}
	if resp.SecuritySignals == nil {
		errs = append(errs, semanticErr("security_signals", "is required"))
	}
	if resp.EntityResults == nil {
		errs = append(errs, semanticErr("entity_results", "is required"))
	}
	if resp.RelationshipResults == nil {
		errs = append(errs, semanticErr("relationship_results", "is required"))
	}
	return errs
}

func validateSemanticEntityResults(req SemanticReviewRequest, results []SemanticEntityResult) []SemanticValidationError {
	expected := map[string]SemanticEntityMention{}
	for _, mention := range req.EntityMentions {
		expected[mention.Ref] = mention
	}
	seen := map[string]struct{}{}
	var errs []SemanticValidationError
	for i, result := range results {
		ref := strings.TrimSpace(result.Ref)
		mention, ok := expected[ref]
		if !ok {
			errs = append(errs, semanticErr(fmt.Sprintf("entity_results[%d].ref", i), "is unknown"))
			continue
		}
		if _, exists := seen[ref]; exists {
			errs = append(errs, semanticErr(fmt.Sprintf("entity_results[%d].ref", i), "is duplicated"))
		}
		seen[ref] = struct{}{}
		if result.Confidence < 0 || result.Confidence > 1 {
			errs = append(errs, semanticErr(fmt.Sprintf("entity_results[%d].confidence", i), "must be between 0 and 1"))
		}
		if rationale := strings.TrimSpace(result.Rationale); rationale == "" || len(rationale) > 1000 {
			errs = append(errs, semanticErr(fmt.Sprintf("entity_results[%d].rationale", i), "is required and must be bounded"))
		}
		switch strings.TrimSpace(result.Action) {
		case string(domain.EntityResolutionReuse):
			if result.CandidateEntityID == nil || strings.TrimSpace(*result.CandidateEntityID) == "" {
				errs = append(errs, semanticErr(fmt.Sprintf("entity_results[%d].candidate_entity_id", i), "is required for reuse"))
				continue
			}
			if !semanticCandidateAllowed(mention.Candidates, strings.TrimSpace(*result.CandidateEntityID)) {
				errs = append(errs, semanticErr(fmt.Sprintf("entity_results[%d].candidate_entity_id", i), "is outside candidate allowlist"))
			}
		case string(domain.EntityResolutionCreate), string(domain.EntityResolutionAmbiguous):
			if result.CandidateEntityID != nil {
				errs = append(errs, semanticErr(fmt.Sprintf("entity_results[%d].candidate_entity_id", i), "must be null"))
			}
		default:
			errs = append(errs, semanticErr(fmt.Sprintf("entity_results[%d].action", i), "is unsupported"))
		}
	}
	for ref := range expected {
		if _, ok := seen[ref]; !ok {
			errs = append(errs, semanticErr("entity_results", "missing result for "+ref))
		}
	}
	return errs
}

func validateSemanticRelationshipResults(req SemanticReviewRequest, results []SemanticRelationshipReviewResult) []SemanticValidationError {
	expected := map[string]SemanticRelationshipObservation{}
	for _, obs := range req.RelationshipObservations {
		expected[obs.Ref] = obs
	}
	seen := map[string]struct{}{}
	var errs []SemanticValidationError
	for i, result := range results {
		ref := strings.TrimSpace(result.Ref)
		obs, ok := expected[ref]
		if !ok {
			errs = append(errs, semanticErr(fmt.Sprintf("relationship_results[%d].ref", i), "is unknown"))
			continue
		}
		if _, exists := seen[ref]; exists {
			errs = append(errs, semanticErr(fmt.Sprintf("relationship_results[%d].ref", i), "is duplicated"))
		}
		seen[ref] = struct{}{}
		if result.Confidence < 0 || result.Confidence > 1 {
			errs = append(errs, semanticErr(fmt.Sprintf("relationship_results[%d].confidence", i), "must be between 0 and 1"))
		}
		if rationale := strings.TrimSpace(result.Rationale); rationale == "" || len(rationale) > 1000 {
			errs = append(errs, semanticErr(fmt.Sprintf("relationship_results[%d].rationale", i), "is required and must be bounded"))
		}
		if !semanticOneOf(result.EvidenceVerdict, domain.VerificationVerdicts()...) {
			errs = append(errs, semanticErr(fmt.Sprintf("relationship_results[%d].evidence_verdict", i), "is unsupported"))
		}
		switch strings.TrimSpace(result.PredicateStatus) {
		case "resolved":
			if result.PredicateKey == nil || strings.TrimSpace(*result.PredicateKey) == "" {
				errs = append(errs, semanticErr(fmt.Sprintf("relationship_results[%d].predicate_key", i), "is required for resolved predicate"))
				continue
			}
			if !semanticPredicateAllowed(obs.PredicateCandidates, strings.TrimSpace(*result.PredicateKey)) {
				errs = append(errs, semanticErr(fmt.Sprintf("relationship_results[%d].predicate_key", i), "is outside predicate allowlist"))
			}
		case "needs_review":
			if result.PredicateKey != nil {
				errs = append(errs, semanticErr(fmt.Sprintf("relationship_results[%d].predicate_key", i), "must be null when predicate_status is needs_review; use predicate_status resolved only when selecting a submitted predicate candidate"))
			}
		default:
			errs = append(errs, semanticErr(fmt.Sprintf("relationship_results[%d].predicate_status", i), "is unsupported"))
		}
	}
	for ref := range expected {
		if _, ok := seen[ref]; !ok {
			errs = append(errs, semanticErr("relationship_results", "missing result for "+ref))
		}
	}
	return errs
}

func semanticEvidenceByID(evidence []SemanticReviewEvidence) map[string]SemanticReviewEvidence {
	out := make(map[string]SemanticReviewEvidence, len(evidence))
	for _, item := range evidence {
		out[strings.TrimSpace(item.EvidenceID)] = item
	}
	return out
}

func semanticAuthorizedCandidates(teamID string, candidates []SemanticEntityCandidate) []SemanticEntityCandidate {
	out := make([]SemanticEntityCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		candidate.TeamID = strings.TrimSpace(candidate.TeamID)
		candidate.Status = strings.TrimSpace(candidate.Status)
		candidate.EntityID = strings.TrimSpace(candidate.EntityID)
		candidate.CanonicalName = strings.TrimSpace(candidate.CanonicalName)
		candidate.Kind = strings.TrimSpace(candidate.Kind)
		if candidate.TeamID != teamID || candidate.Status != string(domain.RelationshipStatusActive) || candidate.EntityID == "" {
			continue
		}
		out = append(out, candidate)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].EntityID < out[j].EntityID })
	return out
}

func semanticActivePredicates(obs SemanticRelationshipObservation, mentions []SemanticEntityMention) []SemanticPredicateCandidate {
	kinds := semanticEndpointKinds(obs, mentions)
	out := make([]SemanticPredicateCandidate, 0, len(obs.PredicateCandidates))
	seen := map[string]struct{}{}
	for _, candidate := range obs.PredicateCandidates {
		candidate.PredicateKey = strings.TrimSpace(candidate.PredicateKey)
		candidate.RelationshipKind = strings.TrimSpace(candidate.RelationshipKind)
		candidate.CurrentCardinality = strings.TrimSpace(candidate.CurrentCardinality)
		candidate.LifecycleState = strings.TrimSpace(candidate.LifecycleState)
		if candidate.LifecycleState == "" {
			candidate.LifecycleState = string(domain.PredicateLifecycleActive)
		}
		if candidate.PredicateKey == "" || candidate.Version < 1 || candidate.LifecycleState != string(domain.PredicateLifecycleActive) {
			continue
		}
		if !semanticOneOf(candidate.RelationshipKind, domain.RelationshipKinds()...) || !semanticOneOf(candidate.CurrentCardinality, domain.CurrentCardinalities()...) {
			continue
		}
		if !semanticKindAllowed(kinds.subject, candidate.AllowedSubjectKinds) || !semanticKindAllowed(kinds.object, candidate.AllowedObjectKinds) {
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

type semanticEndpointKind struct {
	subject string
	object  string
}

func semanticEndpointKinds(obs SemanticRelationshipObservation, mentions []SemanticEntityMention) semanticEndpointKind {
	out := semanticEndpointKind{}
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

func SemanticEvidenceSpan(content string, start int, end int) (string, error) {
	runes := []rune(content)
	if start < 0 || end <= start || end > len(runes) {
		return "", errors.New("span is invalid")
	}
	return string(runes[start:end]), nil
}

func semanticExactSpanQuote(content string, start int, end int, quote string) (string, error) {
	exact, err := SemanticEvidenceSpan(content, start, end)
	if err != nil {
		return "", err
	}
	quote = strings.TrimSpace(quote)
	if quote == "" || quote == exact || semanticWhitespaceEquivalent(exact, quote) {
		return exact, nil
	}
	return "", errors.New("quote does not match the original evidence span")
}

func semanticWhitespaceEquivalent(a string, b string) bool {
	return strings.Join(strings.FieldsFunc(a, unicode.IsSpace), " ") == strings.Join(strings.FieldsFunc(b, unicode.IsSpace), " ")
}

func semanticCandidateAllowed(candidates []SemanticEntityCandidate, entityID string) bool {
	for _, candidate := range candidates {
		if candidate.EntityID == entityID {
			return true
		}
	}
	return false
}

func semanticPredicateAllowed(candidates []SemanticPredicateCandidate, key string) bool {
	for _, candidate := range candidates {
		if candidate.PredicateKey == key {
			return true
		}
	}
	return false
}

func semanticKindAllowed(kind string, allowed []string) bool {
	for _, candidate := range allowed {
		if strings.TrimSpace(candidate) == kind {
			return true
		}
	}
	return false
}

func semanticSecurityKindAllowed(kind string) bool {
	switch strings.TrimSpace(kind) {
	case "role_control_spoofing", "instruction_override", "prompt_secret_extraction", "tool_exfiltration", "obfuscated_instruction", "hidden_control_markup":
		return true
	default:
		return false
	}
}

func semanticSecurityKinds() []string {
	return []string{
		"role_control_spoofing",
		"instruction_override",
		"prompt_secret_extraction",
		"tool_exfiltration",
		"obfuscated_instruction",
		"hidden_control_markup",
	}
}

func semanticOneOf(value string, allowed ...string) bool {
	value = strings.TrimSpace(value)
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func semanticErr(field string, message string) SemanticValidationError {
	return SemanticValidationError{Field: field, Message: message}
}

func semanticSecuritySignalSchema() map[string]any {
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

func semanticEntityResultSchema() map[string]any {
	return map[string]any{
		"type": "array",
		"items": map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"ref", "action", "candidate_entity_id", "confidence", "rationale"},
			"properties": map[string]any{
				"ref":                 map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
				"action":              map[string]any{"enum": domain.EntityResolutionActions()},
				"candidate_entity_id": map[string]any{"type": []string{"string", "null"}, "maxLength": 128},
				"confidence":          map[string]any{"type": "number", "minimum": 0, "maximum": 1},
				"rationale":           map[string]any{"type": "string", "minLength": 1, "maxLength": 1000},
			},
		},
	}
}

func semanticRelationshipResultSchema() map[string]any {
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
				"evidence_verdict": map[string]any{"enum": domain.VerificationVerdicts()},
				"confidence":       map[string]any{"type": "number", "minimum": 0, "maximum": 1},
				"rationale":        map[string]any{"type": "string", "minLength": 1, "maxLength": 1000},
			},
		},
	}
}
