package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"unicode"

	remembercontract "github.com/markhuangai/dense-mem/internal/remember/contract"
	remember "github.com/markhuangai/dense-mem/internal/remember/service"
	session "github.com/markhuangai/dense-mem/internal/session/contract"
)

type excerptRange struct{ event, start, end int }

func buildSessionAssessment(submission *session.Submission, request session.LinkingRequest, linked session.LinkingResponse) ([]remembercontract.EvidenceInput, map[string]any, error) {
	segments := map[string]session.Segment{}
	for _, segment := range request.Segments {
		segments[segment.Ref] = segment
	}
	ranges := []excerptRange{}
	for _, relationship := range request.Relationships {
		for _, citation := range relationship.Citations {
			start, okStart := segments[citation.StartRef]
			end, okEnd := segments[citation.EndRef]
			if !okStart || !okEnd || start.EventIndex != end.EventIndex || end.End <= start.Start {
				return nil, nil, session.ErrInvalidInput
			}
			ranges = append(ranges, excerptRange{start.EventIndex, start.Start, end.End})
		}
	}
	ranges = coalesceExcerptRanges(ranges)
	citationRanges := append([]excerptRange(nil), ranges...)
	for index := range ranges {
		span := &ranges[index]
		runes := []rune(submission.Intake.Request.Events[span.event].Text)
		for span.start < span.end && unicode.IsSpace(runes[span.start]) {
			span.start++
		}
		for span.end > span.start && unicode.IsSpace(runes[span.end-1]) {
			span.end--
		}
		if span.start == span.end {
			return nil, nil, session.ErrInvalidInput
		}
	}
	if len(ranges) > session.MaxExcerpts {
		return nil, nil, session.ErrBudget
	}
	evidence := []remember.RememberEvidenceInput{}
	for _, span := range ranges {
		event := submission.Intake.Request.Events[span.event]
		provenance := map[string]any{
			"framework": submission.Intake.Request.Framework, "app_name": submission.Intake.Request.AppName,
			"user_id": submission.Intake.Request.UserID, "session_id": submission.Intake.Request.SessionID,
			"event_id": event.EventID, "event_index": span.event, "span_start": span.start, "span_end": span.end,
		}
		if event.OccurredAt != nil {
			provenance["occurred_at"] = *event.OccurredAt
		}
		evidence = append(evidence, remember.RememberEvidenceInput{
			Content: string([]rune(event.Text)[span.start:span.end]), SourceType: "conversation",
			Source:      submission.Intake.Request.Framework + ":" + submission.Intake.Request.AppName,
			SourceGroup: "session:" + session.IdentityHash(submission.Intake.Request, ""), Authority: "primary", Metadata: map[string]any{"session": provenance},
		})
	}
	entities := map[string]session.EntityProposal{}
	for _, entity := range request.Entities {
		entities[entity.Ref] = entity
	}
	canonical := map[string]map[string]any{}
	for index, group := range linked.Groups {
		entity := entities[group.CanonicalRef]
		hint := map[string]any{"name": entity.Name, "entity_kind": entity.Kind, "session_ref": fmt.Sprintf("entity:%d", index)}
		for _, member := range group.Members {
			canonical[member] = hint
		}
	}
	proposals := []map[string]any{}
	byKey := map[string]int{}
	for _, relationship := range request.Relationships {
		indices := []int{}
		for _, citation := range relationship.Citations {
			start, end := segments[citation.StartRef], segments[citation.EndRef]
			for index, span := range citationRanges {
				if span.event == start.EventIndex && span.start <= start.Start && span.end >= end.End {
					indices = appendUniqueIndex(indices, index)
					break
				}
			}
		}
		if len(indices) < 1 || len(indices) > 20 {
			return nil, nil, session.ErrBudget
		}
		object := map[string]any{}
		if relationship.ObjectRef != nil {
			object["entity"] = canonical[*relationship.ObjectRef]
		} else {
			value := relationship.ObjectValue
			var decoded any
			decoder := json.NewDecoder(bytes.NewReader(value.Value))
			decoder.UseNumber()
			if err := decoder.Decode(&decoded); err != nil {
				return nil, nil, err
			}
			hint := map[string]any{"type": value.Type, "value": decoded}
			if value.Unit != "" {
				hint["unit"] = value.Unit
			}
			if value.Display != "" {
				hint["display"] = value.Display
			}
			object["value"] = hint
		}
		proposal := map[string]any{"subject": canonical[relationship.SubjectRef], "predicate": map[string]any{"proposed_key": relationship.Predicate}, "object": object, "polarity": relationship.Polarity}
		if relationship.ValidFrom != nil {
			proposal["valid_from"] = *relationship.ValidFrom
		}
		if relationship.ValidTo != nil {
			proposal["valid_to"] = *relationship.ValidTo
		}
		encoded, err := json.Marshal(proposal)
		if err != nil {
			return nil, nil, err
		}
		key := string(encoded)
		if previous, ok := byKey[key]; ok {
			for _, index := range indices {
				proposals[previous]["evidence_indices"] = appendUniqueIndex(proposals[previous]["evidence_indices"].([]int), index)
			}
			proposals[previous]["known_evidence_ids"] = appendUniqueStrings(proposals[previous]["known_evidence_ids"].([]string), relationship.KnownEvidenceIDs)
			continue
		}
		proposal["ref"] = relationship.Ref
		proposal["evidence_indices"] = indices
		proposal["known_evidence_ids"] = append([]string{}, relationship.KnownEvidenceIDs...)
		byKey[key] = len(proposals)
		proposals = append(proposals, proposal)
	}
	if len(proposals) > 200 {
		return nil, nil, session.ErrBudget
	}
	for _, proposal := range proposals {
		if len(proposal["evidence_indices"].([]int)) > 20 || len(proposal["known_evidence_ids"].([]string)) > 20 {
			return nil, nil, session.ErrBudget
		}
	}
	return remember.RepositoryEvidenceInputs(evidence), map[string]any{"session_extraction": true, "entity_hints": []map[string]any{}, "relationship_hints": proposals}, nil
}

func coalesceExcerptRanges(ranges []excerptRange) []excerptRange {
	sort.Slice(ranges, func(i, j int) bool {
		if ranges[i].event != ranges[j].event {
			return ranges[i].event < ranges[j].event
		}
		if ranges[i].start != ranges[j].start {
			return ranges[i].start < ranges[j].start
		}
		return ranges[i].end < ranges[j].end
	})
	merged := []excerptRange{}
	for _, span := range ranges {
		if len(merged) > 0 {
			last := &merged[len(merged)-1]
			if last.event == span.event && span.start <= last.end {
				last.end = max(last.end, span.end)
				continue
			}
		}
		merged = append(merged, span)
	}
	return merged
}

func appendUniqueIndex(indices []int, index int) []int {
	for _, previous := range indices {
		if previous == index {
			return indices
		}
	}
	return append(indices, index)
}
func appendUniqueStrings(existing, additional []string) []string {
	for _, value := range additional {
		found := false
		for _, previous := range existing {
			if previous == value {
				found = true
				break
			}
		}
		if !found {
			existing = append(existing, value)
		}
	}
	return existing
}
