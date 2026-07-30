package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSourceTypeIsValid(t *testing.T) {
	for _, sourceType := range ValidSourceTypes() {
		assert.True(t, sourceType.IsValid())
	}
	assert.False(t, SourceType("typo").IsValid())
	assert.False(t, SourceType("").IsValid())
}

func TestValidSourceTypes(t *testing.T) {
	types := ValidSourceTypes()
	assert.Len(t, types, 4)
	assert.Contains(t, types, SourceTypeConversation)
	assert.Contains(t, types, SourceTypeDocument)
	assert.Contains(t, types, SourceTypeObservation)
	assert.Contains(t, types, SourceTypeManual)
}
