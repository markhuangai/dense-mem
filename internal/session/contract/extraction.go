package contract

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/markhuangai/dense-mem/internal/jsonstrict"
)

type ExtractionRequest struct {
	RequestID string       `json:"request_id"`
	Window    Window       `json:"window"`
	Prior     []PriorEvent `json:"prior_context"`
}

type Excerpt struct {
	EventIndex int    `json:"event_index"`
	Start      int    `json:"start"`
	End        int    `json:"end"`
	Content    string `json:"content"`
}

type ExtractionSignal struct {
	SegmentRef string `json:"segment_ref"`
	Kind       string `json:"kind"`
}

type ExtractionResponse struct {
	Overflow        bool               `json:"overflow"`
	RequestID       string             `json:"request_id"`
	Coverage        []string           `json:"coverage"`
	Excerpts        []Excerpt          `json:"excerpts"`
	SecuritySignals []ExtractionSignal `json:"security_signals"`
}

type Extractor interface {
	Identity() string
	Extract(context.Context, ExtractionRequest) (ExtractionResponse, error)
}

func DecodeExtraction(raw []byte) (ExtractionResponse, error) {
	var response ExtractionResponse
	if len(raw) > 1<<20 {
		return response, &jsonstrict.ValidationError{Stage: "response_json", Errors: []jsonstrict.Issue{{Field: "response", Message: "extraction response exceeds the byte limit"}}}
	}
	validation := &jsonstrict.ValidationError{Stage: "response_json"}
	object, issues := jsonstrict.ValidateResponseObject(raw, "", []string{"request_id", "overflow", "coverage", "excerpts", "security_signals"}, nil, nil)
	validation.Errors = issues
	for _, section := range []struct {
		name     string
		required []string
	}{
		{"excerpts", []string{"event_index", "start", "end", "content"}},
		{"security_signals", []string{"segment_ref", "kind"}},
	} {
		if value, ok := object[section.name]; ok && !bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			validation.Errors = append(validation.Errors, jsonstrict.ValidateResponseArrayObjects(value, section.name, section.required, nil, nil)...)
		}
	}
	if len(validation.Errors) != 0 {
		return response, validation
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		validation.Errors = []jsonstrict.Issue{jsonstrict.ResponseJSONValidationError(err, "")}
		return response, validation
	}
	return response, nil
}

func invalidExtraction(field, reason string) error {
	return &jsonstrict.ValidationError{Stage: "response_contract", Errors: []jsonstrict.Issue{{Field: field, Message: reason}}}
}

func ValidateExtraction(request ExtractionRequest, response ExtractionResponse) error {
	if response.RequestID != request.RequestID {
		return invalidExtraction("request_id", fmt.Sprintf("must equal %q", request.RequestID))
	}
	if response.Coverage == nil || response.Excerpts == nil || response.SecuritySignals == nil {
		return invalidExtraction("response", "extraction response arrays are required")
	}
	if len(response.Excerpts) > MaxExcerpts || len(response.SecuritySignals) > 64 {
		return invalidExtraction("response", "extraction response exceeds closed bounds")
	}
	core := map[string]bool{}
	for _, segment := range request.Window.Core {
		core[segment.Ref] = true
	}
	seen := map[string]bool{}
	for index, ref := range response.Coverage {
		if !core[ref] || seen[ref] {
			return invalidExtraction(fmt.Sprintf("coverage[%d]", index), "unknown or duplicated coverage ref")
		}
		seen[ref] = true
	}
	if len(seen) != len(core) {
		return invalidExtraction("coverage", "incomplete extraction coverage")
	}
	segments := WindowSegments(request.Window)
	sort.Slice(segments, func(i, j int) bool {
		if segments[i].EventIndex != segments[j].EventIndex {
			return segments[i].EventIndex < segments[j].EventIndex
		}
		return segments[i].Start < segments[j].Start
	})
	seenExcerpts := map[string]bool{}
	for index, excerpt := range response.Excerpts {
		if excerpt.Start < 0 || excerpt.End <= excerpt.Start || excerpt.End-excerpt.Start > 999 || strings.TrimSpace(excerpt.Content) == "" {
			return invalidExtraction(fmt.Sprintf("excerpts[%d]", index), "invalid excerpt bounds")
		}
		cursor := excerpt.Start
		var exact strings.Builder
		intersectsCore := false
		for _, segment := range segments {
			if segment.EventIndex != excerpt.EventIndex || segment.End <= cursor || segment.Start >= excerpt.End {
				continue
			}
			if segment.Start > cursor || len([]rune(segment.Text)) != segment.End-segment.Start {
				return invalidExtraction(fmt.Sprintf("excerpts[%d]", index), "excerpt is outside contiguous current-event text")
			}
			end := min(excerpt.End, segment.End)
			exact.WriteString(string([]rune(segment.Text)[cursor-segment.Start : end-segment.Start]))
			intersectsCore = intersectsCore || core[segment.Ref]
			cursor = end
			if cursor == excerpt.End {
				break
			}
		}
		if !intersectsCore || cursor != excerpt.End || exact.String() != excerpt.Content {
			return invalidExtraction(fmt.Sprintf("excerpts[%d]", index), "excerpt does not match exact current-event text and original offsets")
		}
		key := fmt.Sprintf("%d:%d:%d", excerpt.EventIndex, excerpt.Start, excerpt.End)
		if seenExcerpts[key] {
			return invalidExtraction(fmt.Sprintf("excerpts[%d]", index), "duplicated excerpt")
		}
		seenExcerpts[key] = true
	}
	byRef := map[string]bool{}
	for _, segment := range segments {
		byRef[segment.Ref] = true
	}
	seenSignals := map[string]bool{}
	for index, signal := range response.SecuritySignals {
		key := signal.SegmentRef + ":" + signal.Kind
		if seenSignals[key] || !byRef[signal.SegmentRef] || !stringMember([]string{"prompt_injection", "exfiltration", "hidden_control_markup"}, signal.Kind) {
			return invalidExtraction(fmt.Sprintf("security_signals[%d]", index), "invalid or duplicated security signal")
		}
		seenSignals[key] = true
	}
	if response.Overflow {
		return ErrBudget
	}
	return nil
}

func WindowSegments(window Window) []Segment {
	segments := []Segment{}
	if window.Before != nil {
		segments = append(segments, *window.Before)
	}
	segments = append(segments, window.Core...)
	if window.After != nil {
		segments = append(segments, *window.After)
	}
	return segments
}

// SelectedExcerpts is shared by preparation and the authoritative commit so a checkpoint cannot authorize different Evidence.
func SelectedExcerpts(submission *Submission, responses []ExtractionResponse) ([]Excerpt, error) {
	if submission == nil || submission.Intake.ExtractionVersion != ExtractionVersion || len(responses) != len(submission.Intake.Windows) {
		return nil, ErrStale
	}
	allowed := map[int]bool{}
	for _, index := range submission.NewEventIndices {
		allowed[index] = true
	}
	spans := []Excerpt{}
	for index, response := range responses {
		request := ExtractionRequest{RequestID: fmt.Sprintf("session:%s:window:%d", submission.ID, submission.Intake.Windows[index].Index), Window: submission.Intake.Windows[index]}
		if err := ValidateExtraction(request, response); err != nil {
			return nil, err
		}
		if len(response.SecuritySignals) > 0 {
			return nil, ErrSecurity
		}
		for _, excerpt := range response.Excerpts {
			if !allowed[excerpt.EventIndex] || excerpt.EventIndex < 0 || excerpt.EventIndex >= len(submission.Intake.Request.Events) {
				return nil, ErrInvalidInput
			}
			text := []rune(submission.Intake.Request.Events[excerpt.EventIndex].Text)
			if excerpt.End > len(text) || string(text[excerpt.Start:excerpt.End]) != excerpt.Content {
				return nil, ErrInvalidInput
			}
			spans = append(spans, excerpt)
		}
	}
	sort.Slice(spans, func(i, j int) bool {
		if spans[i].EventIndex != spans[j].EventIndex {
			return spans[i].EventIndex < spans[j].EventIndex
		}
		if spans[i].Start != spans[j].Start {
			return spans[i].Start < spans[j].Start
		}
		return spans[i].End < spans[j].End
	})
	merged := []Excerpt{}
	for _, span := range spans {
		if len(merged) > 0 {
			last := &merged[len(merged)-1]
			if last.EventIndex == span.EventIndex && span.Start <= last.End {
				last.End = max(last.End, span.End)
				continue
			}
		}
		merged = append(merged, span)
	}
	selected := []Excerpt{}
	for _, span := range merged {
		text := []rune(submission.Intake.Request.Events[span.EventIndex].Text)
		for start := span.Start; start < span.End; start += 999 {
			end := min(span.End, start+999)
			content := string(text[start:end])
			if strings.TrimSpace(content) == "" {
				continue
			}
			selected = append(selected, Excerpt{EventIndex: span.EventIndex, Start: start, End: end, Content: content})
			if len(selected) > MaxExcerpts {
				return nil, ErrBudget
			}
		}
	}
	return selected, nil
}

func stringMember(values []string, value string) bool {
	for _, allowed := range values {
		if value == allowed {
			return true
		}
	}
	return false
}
