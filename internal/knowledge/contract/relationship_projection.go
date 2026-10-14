package contract

import (
	"strings"
	"time"
)

type RelationshipProjectionNames struct {
	SubjectName     string
	ObjectName      string
	ObjectValueType string
	ObjectValue     string
	ObjectUnit      string
}

func RelationshipProjectionText(relationship *RelationshipRecord, names RelationshipProjectionNames) string {
	lines := []string{
		"relationship",
		"subject: " + firstProjectionValue(names.SubjectName, relationship.SubjectEntityID),
		"predicate: " + strings.ReplaceAll(relationship.PredicateKey, "_", " "),
		"object: " + firstProjectionValue(names.ObjectName, names.ObjectValue, relationship.ObjectEntityID, relationship.ObjectValueID),
		"polarity: " + projectionPolarity(relationship.Polarity),
	}
	if scope := strings.TrimSpace(relationship.ScopeKey); scope != "" {
		lines = append(lines, "scope: "+scope)
	}
	if relationship.ValidFrom != nil {
		lines = append(lines, "valid_from: "+relationship.ValidFrom.UTC().Format(time.RFC3339Nano))
	}
	if relationship.ValidTo != nil {
		lines = append(lines, "valid_to: "+relationship.ValidTo.UTC().Format(time.RFC3339Nano))
	}
	return strings.Join(lines, "\n")
}

func firstProjectionValue(values ...string) string {
	for _, value := range values {
		if v := strings.TrimSpace(value); v != "" {
			return v
		}
	}
	return ""
}
func projectionPolarity(value string) string {
	if strings.TrimSpace(value) == "-" {
		return "negative"
	}
	return "positive"
}
