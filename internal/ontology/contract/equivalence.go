package contract

import (
	"encoding/json"
	"fmt"
	"regexp"
)

var contextualPronouns = regexp.MustCompile(`(?i)\b(i|me|my|mine|we|us|our|ours|here|there|today|yesterday|tomorrow)\b`)
var relativeTimes = regexp.MustCompile(`(?i)\b(today|yesterday|tomorrow)\b`)

// CompatibleMeaningContext prevents model equivalence from crossing known factual context boundaries.
func CompatibleMeaningContext(left, right SourceSnapshot) bool {
	if !left.Eligible || !right.Eligible || left.Kind != right.Kind {
		return false
	}
	if left.Kind == RelationshipSource {
		if (left.State["predicate"] != right.State["predicate"] || left.State["predicate_version"] != right.State["predicate_version"]) && (left.State["predicate_contract"] == "" || right.State["predicate_contract"] == "") {
			return false
		}
		for _, field := range []string{"subject", "object_entity", "object_value", "kind", "cardinality", "polarity", "scope", "valid_from", "valid_to", "metadata", "predicate_contract"} {
			if left.State[field] != right.State[field] {
				return false
			}
		}
		return true
	}
	if left.Kind != EvidenceSource {
		return false
	}
	if left.State["metadata"] != right.State["metadata"] || left.State["labels"] != right.State["labels"] {
		return false
	}
	if (relativeTimes.MatchString(left.State["content"]) || relativeTimes.MatchString(right.State["content"])) && left.State["created_at"] != right.State["created_at"] {
		return false
	}
	if left.OwnerID != right.OwnerID && (contextualPronouns.MatchString(left.State["content"]) || contextualPronouns.MatchString(right.State["content"])) {
		return false
	}
	return true
}

func ExactMeaning(left, right SourceSnapshot) bool {
	if left.Kind == EvidenceSource && left.State["created_at"] != right.State["created_at"] {
		return false
	}
	return left.MeaningKey != "" && left.MeaningKey == right.MeaningKey && CompatibleMeaningContext(left, right)
}

func AssessedGroupCompatible(record Record, snapshots map[string]SourceSnapshot) error {
	if record.Group == nil || !validID(record.Group.AssessmentID) {
		return ErrInvalid
	}
	expected := EvidenceSource
	if record.Kind == RelationshipGroup {
		expected = RelationshipSource
	}
	for i, member := range record.Group.Members {
		left, ok := snapshots[SourceKey(member)]
		if !ok || left.SourceHandle != member || member.Kind != expected || !left.Eligible {
			return ErrSourceStale
		}
		for _, other := range record.Group.Members[:i] {
			if !CompatibleMeaningContext(left, snapshots[SourceKey(other)]) {
				return fmt.Errorf("%w: assessed group has incompatible factual context", ErrInvalid)
			}
		}
	}
	return nil
}

func SourceDisplayContext(source SourceSnapshot) map[string]string {
	context := map[string]string{}
	if source.Kind == EvidenceSource {
		context["metadata"] = source.State["metadata"]
		context["labels"] = source.State["labels"]
		context["created_at"] = source.State["created_at"]
	} else {
		for field, value := range source.State {
			if field != "supports" && field != "occurrences" {
				context[field] = value
			}
		}
	}
	return context
}

func SourceDisplayText(source SourceSnapshot) string {
	if source.Kind == EvidenceSource {
		return source.State["content"]
	}
	encoded, _ := json.Marshal(SourceDisplayContext(source))
	return string(encoded)
}
