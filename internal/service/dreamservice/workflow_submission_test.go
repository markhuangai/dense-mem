package dreamservice

import "strings"

func dreamFeedbackSubmissionProposal(content, predicate string) ([]map[string]any, []map[string]any) {
	subject := "Dense-Mem"
	object := "PostgreSQL"
	span := func(text string) (int, int) {
		start := strings.Index(content, text)
		if start < 0 {
			panic("test evidence does not contain " + text)
		}
		return len([]rune(content[:start])), len([]rune(content[:start+len(text)]))
	}
	subjectStart, subjectEnd := span(subject)
	predicateStart, predicateEnd := span(predicate)
	objectStart, objectEnd := span(object)
	return []map[string]any{
			{"ref": "dream:subject", "name": subject, "evidence": []any{map[string]any{"evidence_index": 0, "start": subjectStart, "end": subjectEnd}}},
			{"ref": "dream:object", "name": object, "evidence": []any{map[string]any{"evidence_index": 0, "start": objectStart, "end": objectEnd}}},
		}, []map[string]any{{
			"proposal_id": "dream:relationship", "subject_ref": "dream:subject", "object_ref": "dream:object",
			"predicate": map[string]any{"surface": predicate, "evidence_index": 0, "start": predicateStart, "end": predicateEnd},
			"evidence":  []any{map[string]any{"evidence_index": 0, "start": 0, "end": len([]rune(content))}},
		}}
}
