package verifier

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/markhuangai/dense-mem/internal/domain"
)

const (
	semanticAssessmentMaxCorrectionSpanHints   = 20
	semanticAssessmentMaxCorrectionSpanOptions = 20
	semanticAssessmentDuplicateEntitySpanError = "duplicates an entity evidence span"
)

type semanticAssessmentCorrectionSpanTarget struct {
	Index        int
	Field        string
	RemoveResult bool
}

func semanticAssessmentMessageTokens(messages []openAIVerifierMessage, tokenizerName string) (int, error) {
	encoded, err := json.Marshal(messages)
	if err != nil {
		return 0, err
	}
	return CountTokens(string(encoded), tokenizerName)
}

func boundedSemanticAssessmentCorrectionErrors(errs []SemanticValidationError) []SemanticValidationError {
	bounded := append([]SemanticValidationError(nil), errs...)
	sort.Slice(bounded, func(i, j int) bool {
		if bounded[i].Field == bounded[j].Field {
			return bounded[i].Message < bounded[j].Message
		}
		return bounded[i].Field < bounded[j].Field
	})
	if len(bounded) <= SemanticAssessmentMaxCorrectionErrors {
		return bounded
	}
	bounded = bounded[:SemanticAssessmentMaxCorrectionErrors-1]
	return append(bounded, SemanticValidationError{
		Field:   "response",
		Message: "additional validation errors were omitted; return one complete response matching the schema",
	})
}

func semanticAssessmentCorrectionSpanHints(
	req SemanticAssessmentRequest,
	response SemanticAssessmentResponse,
	errs []SemanticValidationError,
) []semanticAssessmentCorrectionSpanHint {
	evidenceByID := semanticEvidenceByID(req.Evidence)
	candidateGroups := assessmentCandidateGroupsBySpan(req.EntityCandidateGroups)
	targets := semanticAssessmentCorrectionSpanTargets(errs)
	hints := make([]semanticAssessmentCorrectionSpanHint, 0, min(len(targets), semanticAssessmentMaxCorrectionSpanHints))
	for _, target := range targets {
		index := target.Index
		if index >= len(response.EntityResults) {
			continue
		}
		result := response.EntityResults[index]
		evidence, ok := evidenceByID[result.EvidenceID]
		if !ok {
			continue
		}
		validSpans, truncated := semanticAssessmentSurfaceOccurrences(
			evidence.Content,
			result.Surface,
			semanticAssessmentMaxCorrectionSpanOptions,
		)
		if len(validSpans) == 0 {
			hints = append(hints, semanticAssessmentCorrectionSpanHint{
				Field:        target.Field,
				EvidenceID:   result.EvidenceID,
				ValidSpans:   []semanticAssessmentCorrectionSpan{},
				RemoveResult: true,
			})
			if len(hints) == semanticAssessmentMaxCorrectionSpanHints {
				break
			}
			continue
		}
		exactSurface, err := SemanticEvidenceSpan(evidence.Content, validSpans[0].Start, validSpans[0].End)
		if err != nil {
			continue
		}
		occupiedSpans := semanticAssessmentOccupiedEntitySpans(response, index)
		allOccupied := true
		for i := range validSpans {
			_, validSpans[i].OccupiedByOtherResult = occupiedSpans[assessmentSpanKey(
				result.EvidenceID,
				validSpans[i].Start,
				validSpans[i].End,
			)]
			if !validSpans[i].OccupiedByOtherResult {
				allOccupied = false
			}
			group, hasGroup := candidateGroups[assessmentSpanKey(
				result.EvidenceID,
				validSpans[i].Start,
				validSpans[i].End,
			)]
			validSpans[i].Action, validSpans[i].CandidateEntityID =
				semanticAssessmentCorrectionEntitySelection(group, hasGroup, result.Kind)
		}
		hint := semanticAssessmentCorrectionSpanHint{
			Field:        target.Field,
			EvidenceID:   result.EvidenceID,
			ValidSpans:   validSpans,
			RemoveResult: target.RemoveResult || (!truncated && allOccupied),
			Truncated:    truncated,
		}
		if !hint.RemoveResult {
			hint.Surface = exactSurface
		}
		if !hint.RemoveResult && !truncated {
			hint.RecommendedSpan = semanticAssessmentRecommendedCorrectionSpan(
				validSpans,
				result.Start,
				result.End,
				utf8.RuneCountInString(evidence.Content),
			)
		}
		hints = append(hints, hint)
		if len(hints) == semanticAssessmentMaxCorrectionSpanHints {
			break
		}
	}
	return hints
}

func semanticAssessmentCorrectionSpanTargets(errs []SemanticValidationError) []semanticAssessmentCorrectionSpanTarget {
	targetsByIndex := make(map[int]semanticAssessmentCorrectionSpanTarget, len(errs))
	for _, validationError := range errs {
		if index, ok := semanticAssessmentEntityResultErrorIndex(validationError.Field); ok &&
			strings.TrimSpace(validationError.Message) == semanticAssessmentDuplicateEntitySpanError {
			targetsByIndex[index] = semanticAssessmentCorrectionSpanTarget{
				Index:        index,
				Field:        validationError.Field,
				RemoveResult: true,
			}
			continue
		}
		index, ok := semanticAssessmentEntitySurfaceErrorIndex(validationError.Field)
		if !ok {
			continue
		}
		if target, exists := targetsByIndex[index]; exists && target.RemoveResult {
			continue
		}
		targetsByIndex[index] = semanticAssessmentCorrectionSpanTarget{
			Index: index,
			Field: validationError.Field,
		}
	}
	indexes := make([]int, 0, len(targetsByIndex))
	for index := range targetsByIndex {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	targets := make([]semanticAssessmentCorrectionSpanTarget, 0, len(indexes))
	for _, index := range indexes {
		targets = append(targets, targetsByIndex[index])
	}
	return targets
}

func semanticAssessmentOccupiedEntitySpans(
	response SemanticAssessmentResponse,
	currentIndex int,
) map[string]struct{} {
	occupied := make(map[string]struct{}, max(0, len(response.EntityResults)-1))
	for index, result := range response.EntityResults {
		if index == currentIndex {
			continue
		}
		occupied[assessmentSpanKey(result.EvidenceID, result.Start, result.End)] = struct{}{}
	}
	return occupied
}

func semanticAssessmentRecommendedCorrectionSpan(
	spans []semanticAssessmentCorrectionSpan,
	currentStart int,
	currentEnd int,
	contentRunes int,
) *semanticAssessmentCorrectionSpan {
	if len(spans) == 0 {
		return nil
	}
	currentStart = semanticAssessmentClampedOffset(currentStart, contentRunes)
	currentEnd = semanticAssessmentClampedOffset(currentEnd, contentRunes)
	best := 0
	bestDistance := semanticAssessmentOffsetDistance(spans[0].Start, currentStart) +
		semanticAssessmentOffsetDistance(spans[0].End, currentEnd)
	for index := 1; index < len(spans); index++ {
		distance := semanticAssessmentOffsetDistance(spans[index].Start, currentStart) +
			semanticAssessmentOffsetDistance(spans[index].End, currentEnd)
		if distance < bestDistance {
			best = index
			bestDistance = distance
		}
	}
	if spans[best].OccupiedByOtherResult {
		return nil
	}
	recommended := spans[best]
	return &recommended
}

func semanticAssessmentClampedOffset(offset int, contentRunes int) int {
	if offset < 0 {
		return 0
	}
	if offset > contentRunes {
		return contentRunes
	}
	return offset
}

func semanticAssessmentOffsetDistance(left int, right int) int {
	if left >= right {
		return left - right
	}
	return right - left
}

func semanticAssessmentCorrectionEntitySelection(
	group SemanticAssessmentEntityCandidateGroup,
	hasGroup bool,
	kind string,
) (string, *string) {
	if !hasGroup {
		return string(domain.EntityResolutionCreate), nil
	}
	matching := assessmentMatchingEntityCandidates(group, kind)
	if group.CandidateContextTruncated || len(matching) > 1 {
		return string(domain.EntityResolutionAmbiguous), nil
	}
	if len(matching) == 1 {
		entityID := matching[0].EntityID
		return string(domain.EntityResolutionReuse), &entityID
	}
	return string(domain.EntityResolutionCreate), nil
}

func semanticAssessmentCorrectionEntitySelectionHints(
	req SemanticAssessmentRequest,
	response SemanticAssessmentResponse,
	errs []SemanticValidationError,
) []semanticAssessmentCorrectionEntitySelectionHint {
	targets := make(map[int]struct{}, len(errs))
	unsafe := make(map[int]struct{}, len(errs))
	for _, validationError := range errs {
		if index, ok := semanticAssessmentEntitySelectionErrorIndex(validationError.Field); ok {
			targets[index] = struct{}{}
		}
		if index, ok := semanticAssessmentEntitySelectionUnsafeErrorIndex(validationError.Field); ok {
			unsafe[index] = struct{}{}
		}
	}

	indexes := make([]int, 0, len(targets))
	for index := range targets {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)

	candidateGroups := assessmentCandidateGroupsBySpan(req.EntityCandidateGroups)
	hints := make([]semanticAssessmentCorrectionEntitySelectionHint, 0, len(indexes))
	for _, index := range indexes {
		if _, blocked := unsafe[index]; blocked || index >= len(response.EntityResults) {
			continue
		}
		result := response.EntityResults[index]
		group, hasGroup := candidateGroups[assessmentSpanKey(
			result.EvidenceID,
			result.Start,
			result.End,
		)]
		action, candidateEntityID := semanticAssessmentCorrectionEntitySelection(
			group,
			hasGroup,
			result.Kind,
		)
		hints = append(hints, semanticAssessmentCorrectionEntitySelectionHint{
			Index:             index,
			Action:            action,
			CandidateEntityID: candidateEntityID,
		})
	}
	return hints
}

func semanticAssessmentEntitySelectionErrorIndex(field string) (int, bool) {
	if index, ok := semanticAssessmentEntityResultFieldIndex(field, "].action"); ok {
		return index, true
	}
	return semanticAssessmentEntityResultFieldIndex(field, "].candidate_entity_id")
}

func semanticAssessmentEntitySelectionUnsafeErrorIndex(field string) (int, bool) {
	for _, suffix := range []string{
		"]",
		"].evidence_id",
		"].surface",
		"].start",
		"].end",
		"].kind",
	} {
		if index, ok := semanticAssessmentEntityResultFieldIndex(field, suffix); ok {
			return index, true
		}
	}
	return 0, false
}

func semanticAssessmentEntitySurfaceErrorIndex(field string) (int, bool) {
	return semanticAssessmentEntityResultFieldIndex(field, "].surface")
}

func semanticAssessmentEntityResultErrorIndex(field string) (int, bool) {
	return semanticAssessmentEntityResultFieldIndex(field, "]")
}

func semanticAssessmentEntityResultFieldIndex(field string, suffix string) (int, bool) {
	const prefix = "entity_results["
	if !strings.HasPrefix(field, prefix) || !strings.HasSuffix(field, suffix) {
		return 0, false
	}
	rawIndex := strings.TrimSuffix(strings.TrimPrefix(field, prefix), suffix)
	if rawIndex == "" {
		return 0, false
	}
	for _, char := range rawIndex {
		if char < '0' || char > '9' {
			return 0, false
		}
	}
	index, err := strconv.Atoi(rawIndex)
	if err != nil {
		return 0, false
	}
	return index, true
}

func semanticAssessmentSurfaceOccurrences(
	content string,
	surface string,
	maxOptions int,
) ([]semanticAssessmentCorrectionSpan, bool) {
	surface = strings.TrimSpace(surface)
	surfaceRunes := utf8.RuneCountInString(surface)
	if surfaceRunes == 0 || len(surface) > len(content) || maxOptions <= 0 {
		return nil, false
	}
	spans := make([]semanticAssessmentCorrectionSpan, 0, maxOptions)
	searchByte := 0
	searchRune := 0
	for searchByte <= len(content)-len(surface) {
		relativeByte := strings.Index(content[searchByte:], surface)
		if relativeByte < 0 {
			break
		}
		startByte := searchByte + relativeByte
		startRune := searchRune + utf8.RuneCountInString(content[searchByte:startByte])
		if len(spans) == maxOptions {
			return spans, true
		}
		spans = append(spans, semanticAssessmentCorrectionSpan{
			Start: startRune,
			End:   startRune + surfaceRunes,
		})
		_, runeBytes := utf8.DecodeRuneInString(content[startByte:])
		searchByte = startByte + runeBytes
		searchRune = startRune + 1
	}
	return spans, false
}

func semanticAssessmentValidationFieldFamilies(errs []SemanticValidationError) []string {
	seen := make(map[string]struct{}, len(errs))
	families := make([]string, 0, len(errs))
	for _, validationError := range errs {
		family := semanticAssessmentValidationFieldFamily(validationError.Field)
		if _, exists := seen[family]; exists {
			continue
		}
		seen[family] = struct{}{}
		families = append(families, family)
	}
	return families
}

func semanticAssessmentValidationFieldFamily(field string) string {
	field = strings.ToLower(strings.TrimSpace(field))
	switch field {
	case "request_id":
		return "request_id"
	case "output_tokens":
		return "output_tokens"
	case "response":
		return "response"
	}
	switch {
	case strings.HasPrefix(field, "security_signals"):
		return "security_signals"
	case strings.HasPrefix(field, "entity_results"):
		return semanticAssessmentEntityValidationFieldFamily(field)
	case strings.HasPrefix(field, "relationship_results"):
		return semanticAssessmentRelationshipValidationFieldFamily(field)
	default:
		return "other"
	}
}

func semanticAssessmentEntityValidationFieldFamily(field string) string {
	switch semanticAssessmentValidationLeaf(field) {
	case "entity_results":
		return "entity_results"
	case "ref":
		return "entity_results.ref"
	case "evidence_id", "surface", "span", "start", "end":
		return "entity_results.span"
	case "kind":
		return "entity_results.kind"
	case "action", "candidate_entity_id":
		return "entity_results.selection"
	case "confidence", "rationale":
		return "entity_results.quality"
	default:
		return "entity_results.other"
	}
}

func semanticAssessmentRelationshipValidationFieldFamily(field string) string {
	if strings.Contains(field, ".evidence[") {
		return "relationship_results.evidence"
	}
	switch semanticAssessmentValidationLeaf(field) {
	case "relationship_results":
		return "relationship_results"
	case "ref":
		return "relationship_results.ref"
	case "subject_ref":
		return "relationship_results.subject"
	case "original_predicate", "predicate_status", "predicate_key", "predicate_version":
		return "relationship_results.predicate"
	case "object", "object_ref", "object_value":
		return "relationship_results.object"
	case "evidence", "evidence_id", "start", "end":
		return "relationship_results.evidence"
	case "polarity", "modality":
		return "relationship_results.semantics"
	case "evidence_verdict":
		return "relationship_results.verdict"
	case "valid_from", "valid_to", "temporal_verdict":
		return "relationship_results.temporal"
	case "scope_key", "scope_status":
		return "relationship_results.scope"
	case "confidence", "rationale":
		return "relationship_results.quality"
	default:
		return "relationship_results.other"
	}
}

func semanticAssessmentValidationLeaf(field string) string {
	if dot := strings.LastIndexByte(field, '.'); dot >= 0 {
		field = field[dot+1:]
	}
	if bracket := strings.IndexByte(field, '['); bracket >= 0 {
		field = field[:bracket]
	}
	return field
}

func semanticAssessmentResponseForCorrection(
	req SemanticAssessmentRequest,
	result openAIStructuredChatResult,
	limits SemanticAssessmentLimits,
) (SemanticAssessmentResponse, []SemanticValidationError, string) {
	if result.Usage != nil && result.Usage.CompletionTokens > int64(limits.MaxOutputTokens) {
		return SemanticAssessmentResponse{}, []SemanticValidationError{semanticErr(
			"output_tokens",
			fmt.Sprintf("provider reported more than the allowed %d tokens", limits.MaxOutputTokens),
		)}, "response_output_tokens"
	}
	outputTokens, err := CountTokens(result.Content, limits.Tokenizer)
	if err != nil {
		return SemanticAssessmentResponse{}, []SemanticValidationError{
			semanticErr("response", "could not be token-counted"),
		}, "response_json"
	}
	if outputTokens > limits.MaxOutputTokens {
		return SemanticAssessmentResponse{}, []SemanticValidationError{semanticErr(
			"output_tokens",
			fmt.Sprintf("must be less than or equal to %d", limits.MaxOutputTokens),
		)}, "response_output_tokens"
	}
	if validationErrors := validateSemanticAssessmentResponseRaw([]byte(result.Content)); len(validationErrors) > 0 {
		return SemanticAssessmentResponse{}, validationErrors, "response_json"
	}
	response, err := DecodeSemanticAssessmentResponseJSON([]byte(result.Content), limits)
	if err != nil {
		field := "response"
		message := "must be one complete JSON object matching the required field types"
		var typeError *json.UnmarshalTypeError
		if errors.As(err, &typeError) && typeError.Field != "" {
			field = typeError.Field
			message = "must match the required JSON type"
		}
		return SemanticAssessmentResponse{}, []SemanticValidationError{
			semanticErr(field, message),
		}, "response_json"
	}
	normalized, validationErrors := PrepareSemanticAssessmentResponse(req, response, limits)
	if len(validationErrors) > 0 {
		return normalized, validationErrors, "response_contract"
	}
	return normalized, nil, ""
}

func openAIHTTPFailureClass(statusCode int) string {
	switch {
	case statusCode >= 400 && statusCode < 500:
		return ProviderFailureClassHTTPClient
	case statusCode >= 500 && statusCode < 600:
		return ProviderFailureClassHTTPServer
	default:
		return ProviderFailureClassHTTPUnexpected
	}
}

func openAIRetryAfterSeconds(value string, now time.Time) int {
	value = strings.TrimSpace(value)
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds >= int(providerFailureMaxRetryAfter/time.Second) {
			return int(providerFailureMaxRetryAfter / time.Second)
		}
		if seconds > 0 {
			return seconds
		}
		return 0
	}
	retryAt, err := http.ParseTime(value)
	if err != nil {
		return 0
	}
	delay := retryAt.Sub(now)
	if delay <= 0 {
		return 0
	}
	seconds := int((delay + time.Second - 1) / time.Second)
	if seconds >= int(providerFailureMaxRetryAfter/time.Second) {
		return int(providerFailureMaxRetryAfter / time.Second)
	}
	return seconds
}

func openAIRequestTimedOutOrCanceled(ctx context.Context, err error) bool {
	if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var timeout interface{ Timeout() bool }
	return errors.As(err, &timeout) && timeout.Timeout()
}
