package contract

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/jsonstrict"
)

type ExtractionRequest struct {
	RequestID string       `json:"request_id"`
	Window    Window       `json:"window"`
	Prior     []PriorEvent `json:"prior_context"`
}

type EntityProposal struct {
	Ref  string `json:"ref"`
	Name string `json:"name"`
	Kind string `json:"entity_kind"`
}

type ValueProposal struct {
	Type    string          `json:"type"`
	Value   json.RawMessage `json:"value"`
	Unit    string          `json:"unit"`
	Display string          `json:"display"`
}

type Citation struct {
	StartRef string `json:"start_ref"`
	EndRef   string `json:"end_ref"`
}

type RelationshipProposal struct {
	Ref              string         `json:"ref"`
	SubjectRef       string         `json:"subject_ref"`
	Predicate        string         `json:"predicate"`
	ObjectRef        *string        `json:"object_ref"`
	ObjectValue      *ValueProposal `json:"object_value"`
	Polarity         string         `json:"polarity"`
	Citations        []Citation     `json:"citations"`
	KnownEvidenceIDs []string       `json:"known_evidence_ids"`
	ValidFrom        *string        `json:"valid_from"`
	ValidTo          *string        `json:"valid_to"`
}

type ExtractionSignal struct {
	SegmentRef string `json:"segment_ref"`
	Kind       string `json:"kind"`
}

type ExtractionResponse struct {
	Overflow        bool                   `json:"overflow"`
	RequestID       string                 `json:"request_id"`
	Coverage        []string               `json:"coverage"`
	Entities        []EntityProposal       `json:"entities"`
	Relationships   []RelationshipProposal `json:"relationships"`
	SecuritySignals []ExtractionSignal     `json:"security_signals"`
}

type LinkingRequest struct {
	RequestID     string                 `json:"request_id"`
	Entities      []EntityProposal       `json:"entities"`
	Relationships []RelationshipProposal `json:"relationships"`
	Segments      []Segment              `json:"segments"`
}

type EntityGroup struct {
	Ref          string   `json:"ref"`
	CanonicalRef string   `json:"canonical_ref"`
	Members      []string `json:"members"`
}

type LinkingResponse struct {
	RequestID string        `json:"request_id"`
	Groups    []EntityGroup `json:"groups"`
}

type Extractor interface {
	Identity() string
	Extract(context.Context, ExtractionRequest) (ExtractionResponse, error)
	Link(context.Context, LinkingRequest) (LinkingResponse, error)
}

func DecodeExtraction(raw []byte) (ExtractionResponse, error) {
	var response ExtractionResponse
	if err := jsonstrict.Decode(bytes.NewReader(raw), &response, 1<<20); err != nil {
		return response, err
	}
	if err := requireResponseKeys(raw, []string{"request_id", "overflow", "coverage", "entities", "relationships", "security_signals"}, nil); err != nil {
		return response, err
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return response, err
	}
	for _, section := range []struct {
		name               string
		required, nullable []string
	}{
		{"entities", []string{"ref", "name", "entity_kind"}, nil},
		{"relationships", []string{"ref", "subject_ref", "predicate", "object_ref", "object_value", "polarity", "citations", "known_evidence_ids", "valid_from", "valid_to"}, []string{"object_ref", "object_value", "valid_from", "valid_to"}},
		{"security_signals", []string{"segment_ref", "kind"}, nil},
	} {
		var entries []json.RawMessage
		if err := json.Unmarshal(object[section.name], &entries); err != nil {
			return response, err
		}
		for _, entry := range entries {
			if err := requireResponseKeys(entry, section.required, section.nullable); err != nil {
				return response, err
			}
		}
	}
	var relationships []map[string]json.RawMessage
	if err := json.Unmarshal(object["relationships"], &relationships); err != nil {
		return response, err
	}
	for _, relationship := range relationships {
		if value := relationship["object_value"]; string(value) != "null" {
			if err := requireResponseKeys(value, []string{"type", "value", "unit", "display"}, nil); err != nil {
				return response, err
			}
		}
		var citations []json.RawMessage
		if err := json.Unmarshal(relationship["citations"], &citations); err != nil {
			return response, err
		}
		for _, citation := range citations {
			if err := requireResponseKeys(citation, []string{"start_ref", "end_ref"}, nil); err != nil {
				return response, err
			}
		}
	}
	return response, nil
}

func DecodeLinking(raw []byte) (LinkingResponse, error) {
	var response LinkingResponse
	if err := jsonstrict.Decode(bytes.NewReader(raw), &response, 1<<20); err != nil {
		return response, err
	}
	if err := requireResponseKeys(raw, []string{"request_id", "groups"}, nil); err != nil {
		return response, err
	}
	var object struct {
		Groups []json.RawMessage `json:"groups"`
	}
	if err := json.Unmarshal(raw, &object); err != nil {
		return response, err
	}
	for _, group := range object.Groups {
		if err := requireResponseKeys(group, []string{"ref", "canonical_ref", "members"}, nil); err != nil {
			return response, err
		}
	}
	return response, nil
}

func requireResponseKeys(raw []byte, required, nullable []string) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	if len(fields) != len(required) {
		return fmt.Errorf("response contains unknown or missing fields")
	}
	for _, key := range required {
		value, ok := fields[key]
		if !ok {
			return fmt.Errorf("missing %s", key)
		}
		if string(value) == "null" {
			allowed := false
			for _, name := range nullable {
				if name == key {
					allowed = true
					break
				}
			}
			if !allowed {
				return fmt.Errorf("%s cannot be null", key)
			}
		}
	}
	return nil
}

func ValidateExtraction(request ExtractionRequest, response ExtractionResponse) error {
	if response.RequestID != request.RequestID {
		return fmt.Errorf("request_id must equal %q", request.RequestID)
	}
	if response.Coverage == nil || response.Entities == nil || response.Relationships == nil || response.SecuritySignals == nil {
		return fmt.Errorf("extraction response arrays are required")
	}
	if len(response.Entities) > 400 || len(response.Relationships) > 200 || len(response.SecuritySignals) > 64 {
		return fmt.Errorf("extraction response exceeds closed bounds")
	}
	core := map[string]bool{}
	for _, segment := range request.Window.Core {
		core[segment.Ref] = true
	}
	seen := map[string]bool{}
	for _, ref := range response.Coverage {
		if !core[ref] || seen[ref] {
			return fmt.Errorf("unknown or duplicated coverage ref")
		}
		seen[ref] = true
	}
	if len(seen) != len(core) {
		return fmt.Errorf("incomplete extraction coverage")
	}
	segments := WindowSegments(request.Window)
	byRef := map[string]Segment{}
	for _, segment := range segments {
		byRef[segment.Ref] = segment
	}
	entities := map[string]EntityProposal{}
	for _, entity := range response.Entities {
		if !boundedString(entity.Ref, 64) || !boundedString(entity.Name, 256) || !stringMember(domain.EntityKinds(), entity.Kind) {
			return fmt.Errorf("invalid entity proposal")
		}
		if _, exists := entities[entity.Ref]; exists {
			return fmt.Errorf("duplicate entity ref")
		}
		entities[entity.Ref] = entity
	}
	known := map[string]bool{}
	for _, event := range request.Prior {
		for _, id := range event.EvidenceIDs {
			known[id] = true
		}
	}
	relationships, used := map[string]bool{}, map[string]bool{}
	for _, relationship := range response.Relationships {
		if !boundedString(relationship.Ref, 64) || relationships[relationship.Ref] || !boundedString(relationship.Predicate, 128) {
			return fmt.Errorf("invalid relationship ref or predicate")
		}
		relationships[relationship.Ref] = true
		if _, ok := entities[relationship.SubjectRef]; !ok {
			return fmt.Errorf("unknown subject ref")
		}
		used[relationship.SubjectRef] = true
		if (relationship.ObjectRef == nil) == (relationship.ObjectValue == nil) {
			return fmt.Errorf("exactly one object endpoint is required")
		}
		if relationship.ObjectRef != nil {
			if _, ok := entities[*relationship.ObjectRef]; !ok {
				return fmt.Errorf("unknown object ref")
			}
			used[*relationship.ObjectRef] = true
		} else if err := validateProposedValue(*relationship.ObjectValue); err != nil {
			return err
		}
		if relationship.Polarity != "+" && relationship.Polarity != "-" {
			return fmt.Errorf("invalid polarity")
		}
		if len(relationship.Citations) < 1 || len(relationship.Citations) > 20 || relationship.KnownEvidenceIDs == nil || len(relationship.KnownEvidenceIDs) > 20 {
			return fmt.Errorf("citation arrays are incomplete or exceed bounds")
		}
		for _, citation := range relationship.Citations {
			start, startOK := byRef[citation.StartRef]
			end, endOK := byRef[citation.EndRef]
			if !startOK || !endOK || start.EventIndex != end.EventIndex || end.Start < start.Start {
				return fmt.Errorf("citation outside source allowlist")
			}
			citesCore := false
			for _, segment := range request.Window.Core {
				if segment.EventIndex == start.EventIndex && segment.Start >= start.Start && segment.End <= end.End {
					citesCore = true
					break
				}
			}
			if !citesCore {
				return fmt.Errorf("relationship requires current core evidence")
			}
		}
		seenKnown := map[string]bool{}
		for _, id := range relationship.KnownEvidenceIDs {
			if !known[id] || seenKnown[id] {
				return fmt.Errorf("known evidence outside context allowlist")
			}
			seenKnown[id] = true
		}
		if err := validateProposedTimes(relationship.ValidFrom, relationship.ValidTo); err != nil {
			return err
		}
	}
	if len(used) != len(entities) {
		return fmt.Errorf("unused entity proposal")
	}
	seenSignals := map[string]bool{}
	for _, signal := range response.SecuritySignals {
		key := signal.SegmentRef + ":" + signal.Kind
		if seenSignals[key] {
			return fmt.Errorf("duplicated security signal")
		}
		seenSignals[key] = true
		if _, ok := byRef[signal.SegmentRef]; !ok || !stringMember([]string{"prompt_injection", "exfiltration", "hidden_control_markup"}, signal.Kind) {
			return fmt.Errorf("invalid security signal")
		}
	}
	if response.Overflow {
		return ErrBudget
	}
	return nil
}

func ValidateLinking(request LinkingRequest, response LinkingResponse) error {
	if response.RequestID != request.RequestID {
		return fmt.Errorf("request_id must equal %q", request.RequestID)
	}
	if response.Groups == nil || len(response.Groups) > 400 {
		return fmt.Errorf("linking groups are required and must contain at most 400 entries")
	}
	entities := map[string]EntityProposal{}
	for _, entity := range request.Entities {
		if _, exists := entities[entity.Ref]; exists {
			return fmt.Errorf("duplicate draft ref")
		}
		entities[entity.Ref] = entity
	}
	seen, refs := map[string]bool{}, map[string]bool{}
	for _, group := range response.Groups {
		canonical, ok := entities[group.CanonicalRef]
		if !ok || !boundedString(group.Ref, 64) || refs[group.Ref] || len(group.Members) < 1 {
			return fmt.Errorf("invalid linking group")
		}
		refs[group.Ref] = true
		canonicalSeen := false
		for _, member := range group.Members {
			entity, ok := entities[member]
			if !ok || seen[member] || entity.Kind != canonical.Kind {
				return fmt.Errorf("unknown, duplicated, or incompatible group member")
			}
			seen[member] = true
			canonicalSeen = canonicalSeen || member == group.CanonicalRef
		}
		if !canonicalSeen {
			return fmt.Errorf("canonical ref is outside its group")
		}
	}
	if len(seen) != len(entities) {
		return fmt.Errorf("incomplete entity linking coverage")
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

func boundedString(value string, limit int) bool {
	return strings.TrimSpace(value) != "" && len([]rune(value)) <= limit
}
func stringMember(values []string, value string) bool {
	for _, allowed := range values {
		if value == allowed {
			return true
		}
	}
	return false
}

func validateProposedTimes(from, to *string) error {
	var first, last time.Time
	var err error
	if from != nil {
		first, err = time.Parse(time.RFC3339Nano, *from)
		if err != nil {
			return fmt.Errorf("invalid valid_from")
		}
	}
	if to != nil {
		last, err = time.Parse(time.RFC3339Nano, *to)
		if err != nil {
			return fmt.Errorf("invalid valid_to")
		}
	}
	if from != nil && to != nil && last.Before(first) {
		return fmt.Errorf("invalid temporal interval")
	}
	return nil
}

func validateProposedValue(value ValueProposal) error {
	if !stringMember(domain.ValueTypes(), value.Type) || len(value.Value) == 0 || len([]rune(value.Display)) > 4096 || len([]rune(value.Unit)) > 128 {
		return fmt.Errorf("invalid typed value")
	}
	decoder := json.NewDecoder(bytes.NewReader(value.Value))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	switch value.Type {
	case "number":
		if _, ok := decoded.(json.Number); !ok {
			return fmt.Errorf("number value required")
		}
	case "boolean":
		if _, ok := decoded.(bool); !ok {
			return fmt.Errorf("boolean value required")
		}
	default:
		text, ok := decoded.(string)
		if !ok || !boundedString(text, 4096) {
			return fmt.Errorf("string value required")
		}
		if value.Type == "date" {
			if _, err := time.Parse("2006-01-02", text); err != nil {
				return err
			}
		}
		if value.Type == "date_time" {
			if _, err := time.Parse(time.RFC3339Nano, text); err != nil {
				return err
			}
		}
	}
	return nil
}
