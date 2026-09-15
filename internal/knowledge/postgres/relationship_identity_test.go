package postgres

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestSemanticGroupKeyExcludesMutableValidTo(t *testing.T) {
	open := ApplyRelationshipDecisionInput{
		SubjectEntityID: "8f8a7889-93e0-4332-b22a-abf5c09dc8b7",
		PredicateKey:    "ahead_of",
		ObjectEntityID:  "1d5fce8f-cfaf-4350-a612-0268bd8296bd",
		Polarity:        "+",
	}
	bounded := open
	validTo := time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC)
	bounded.ValidTo = &validTo

	assert.Equal(t, "sg:29bf9ddd7c106f1ec0ece804ed5c304a9d5dd3af1bfd22bc8f89188e6edf3d17", semanticGroupKey(open))
	assert.Equal(t, semanticGroupKey(open), semanticGroupKey(bounded))
}
