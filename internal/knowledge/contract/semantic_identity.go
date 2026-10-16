package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
)

func RelationshipSemanticGroupKey(subjectID, predicate, entityID, valueID, polarity, scope string, validFrom *time.Time) string {
	object := "entity:" + entityID
	if entityID == "" {
		object = "value:" + valueID
	}
	from := ""
	if validFrom != nil {
		from = validFrom.UTC().Format(time.RFC3339Nano)
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{subjectID, predicate, object, polarity, scope, from, ""}, "\x00")))
	return "sg:" + hex.EncodeToString(sum[:])
}
