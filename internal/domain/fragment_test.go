package domain

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSourceType_IsValid(t *testing.T) {
	t.Run("valid source types", func(t *testing.T) {
		assert.True(t, SourceTypeConversation.IsValid())
		assert.True(t, SourceTypeDocument.IsValid())
		assert.True(t, SourceTypeObservation.IsValid())
		assert.True(t, SourceTypeManual.IsValid())
	})

	t.Run("invalid source types", func(t *testing.T) {
		assert.False(t, SourceType("typo").IsValid())
		assert.False(t, SourceType("").IsValid())
		assert.False(t, SourceType("invalid").IsValid())
	})
}

func TestValidSourceTypes(t *testing.T) {
	types := ValidSourceTypes()
	assert.Len(t, types, 4)
	assert.Contains(t, types, SourceTypeConversation)
	assert.Contains(t, types, SourceTypeDocument)
	assert.Contains(t, types, SourceTypeObservation)
	assert.Contains(t, types, SourceTypeManual)
}

func TestFragment_JSONSerialization(t *testing.T) {
	f := Fragment{
		FragmentID:          "abc",
		SourceType:          SourceTypeManual,
		Content:             "hi",
		ProfileID:           "profile-123",
		ContentHash:         "hash123",
		EmbeddingModel:      "text-embedding-ada-002",
		EmbeddingDimensions: 1536,
		CreatedAt:           time.Now(),
		UpdatedAt:           time.Now(),
	}

	b, err := json.Marshal(f)
	require.NoError(t, err)
	jsonStr := string(b)

	t.Run("uses id field name not fragment_id", func(t *testing.T) {
		assert.Contains(t, jsonStr, `"id":"abc"`)
		assert.NotContains(t, jsonStr, `"fragment_id"`)
	})

	t.Run("includes content_hash", func(t *testing.T) {
		assert.Contains(t, jsonStr, `"content_hash":"hash123"`)
	})

	t.Run("includes source_type", func(t *testing.T) {
		assert.Contains(t, jsonStr, `"source_type":"manual"`)
	})

	t.Run("includes embedding_model", func(t *testing.T) {
		assert.Contains(t, jsonStr, `"embedding_model":"text-embedding-ada-002"`)
	})

	t.Run("includes embedding_dimensions", func(t *testing.T) {
		assert.Contains(t, jsonStr, `"embedding_dimensions":1536`)
	})
}

func TestFragment_JSONSerialization_OmitEmpty(t *testing.T) {
	f := Fragment{
		FragmentID:          "abc",
		SourceType:          SourceTypeManual,
		Content:             "hi",
		ContentHash:         "hash123",
		EmbeddingModel:      "text-embedding-ada-002",
		EmbeddingDimensions: 1536,
		CreatedAt:           time.Now(),
		UpdatedAt:           time.Now(),
	}

	b, err := json.Marshal(f)
	require.NoError(t, err)
	jsonStr := string(b)

	t.Run("idempotency_key omitted when empty", func(t *testing.T) {
		assert.NotContains(t, jsonStr, `"idempotency_key"`)
	})

	t.Run("source omitted when empty", func(t *testing.T) {
		assert.NotContains(t, jsonStr, `"source"`)
	})

	t.Run("labels omitted when nil", func(t *testing.T) {
		assert.NotContains(t, jsonStr, `"labels"`)
	})

	t.Run("metadata omitted when nil", func(t *testing.T) {
		assert.NotContains(t, jsonStr, `"metadata"`)
	})
}

func TestFragment_JSONSerialization_WithOptionalFields(t *testing.T) {
	f := Fragment{
		FragmentID:          "abc",
		ProfileID:           "profile-123",
		SourceType:          SourceTypeDocument,
		Content:             "test content",
		Source:              "upload",
		Labels:              []string{"tag1", "tag2"},
		Metadata:            map[string]any{"key": "value"},
		ContentHash:         "hash123",
		IdempotencyKey:      "idem-123",
		EmbeddingModel:      "text-embedding-ada-002",
		EmbeddingDimensions: 1536,
		CreatedAt:           time.Now(),
		UpdatedAt:           time.Now(),
	}

	b, err := json.Marshal(f)
	require.NoError(t, err)
	jsonStr := string(b)

	t.Run("idempotency_key present when set", func(t *testing.T) {
		assert.Contains(t, jsonStr, `"idempotency_key":"idem-123"`)
	})

	t.Run("source present when set", func(t *testing.T) {
		assert.Contains(t, jsonStr, `"source":"upload"`)
	})

	t.Run("labels present when set", func(t *testing.T) {
		assert.Contains(t, jsonStr, `"labels"`)
		assert.Contains(t, jsonStr, `"tag1"`)
		assert.Contains(t, jsonStr, `"tag2"`)
	})

	t.Run("metadata present when set", func(t *testing.T) {
		assert.Contains(t, jsonStr, `"metadata"`)
		assert.Contains(t, jsonStr, `"key":"value"`)
	})
}

func TestFragment_JSONRoundTrip(t *testing.T) {
	original := Fragment{
		FragmentID:          "fragment-123",
		ProfileID:           "profile-456",
		Content:             "test content for round trip",
		Source:              "api",
		SourceType:          SourceTypeConversation,
		Labels:              []string{"label1", "label2"},
		Metadata:            map[string]any{"foo": "bar", "num": 42},
		ContentHash:         "sha256:abc123",
		IdempotencyKey:      "unique-key-789",
		EmbeddingModel:      "text-embedding-3-small",
		EmbeddingDimensions: 1536,
		CreatedAt:           time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC),
		UpdatedAt:           time.Date(2024, 1, 15, 11, 45, 0, 0, time.UTC),
	}

	// Marshal
	data, err := json.Marshal(original)
	require.NoError(t, err)

	// Unmarshal
	var restored Fragment
	err = json.Unmarshal(data, &restored)
	require.NoError(t, err)

	// Verify all fields
	assert.Equal(t, original.FragmentID, restored.FragmentID)
	assert.Equal(t, original.ProfileID, restored.ProfileID)
	assert.Equal(t, original.Content, restored.Content)
	assert.Equal(t, original.Source, restored.Source)
	assert.Equal(t, original.SourceType, restored.SourceType)
	assert.Equal(t, original.Labels, restored.Labels)
	assert.Equal(t, original.Metadata["foo"], restored.Metadata["foo"])
	assert.Equal(t, original.ContentHash, restored.ContentHash)
	assert.Equal(t, original.IdempotencyKey, restored.IdempotencyKey)
	assert.Equal(t, original.EmbeddingModel, restored.EmbeddingModel)
	assert.Equal(t, original.EmbeddingDimensions, restored.EmbeddingDimensions)
	assert.True(t, original.CreatedAt.Equal(restored.CreatedAt))
	assert.True(t, original.UpdatedAt.Equal(restored.UpdatedAt))
}

func TestFragmentModel_HasAllRequiredFields(t *testing.T) {
	// This test verifies the struct has all required fields per AC-41
	f := Fragment{
		FragmentID:          "test-id",
		ProfileID:           "profile-id",
		Content:             "content",
		SourceType:          SourceTypeDocument,
		ContentHash:         "hash",
		EmbeddingModel:      "model",
		EmbeddingDimensions: 1536,
		CreatedAt:           time.Now(),
		UpdatedAt:           time.Now(),
	}

	// Verify all AC-41 required fields are present
	assert.NotEmpty(t, f.FragmentID, "FragmentID (id) required")
	assert.NotEmpty(t, f.ProfileID, "ProfileID required")
	assert.NotEmpty(t, f.Content, "Content required")
	assert.NotEmpty(t, f.SourceType, "SourceType required")
	assert.NotEmpty(t, f.ContentHash, "ContentHash required")
	assert.NotEmpty(t, f.EmbeddingModel, "EmbeddingModel required")
	assert.NotZero(t, f.EmbeddingDimensions, "EmbeddingDimensions required")
	assert.NotZero(t, f.CreatedAt, "CreatedAt required")
	assert.NotZero(t, f.UpdatedAt, "UpdatedAt required")

	// Verify optional field is present in struct (may be empty)
	_ = f.IdempotencyKey // field exists

	// Verify SourceType enum values exist
	assert.True(t, SourceTypeConversation.IsValid())
	assert.True(t, SourceTypeDocument.IsValid())
	assert.True(t, SourceTypeObservation.IsValid())
	assert.True(t, SourceTypeManual.IsValid())
}

// TestFragmentStatus is the red-test gate for Unit 45.
// It verifies that FragmentStatus type constants exist and that
// Fragment carries Status and RetractedAt fields.
func TestFragmentStatus(t *testing.T) {
	t.Run("constant values are correct strings", func(t *testing.T) {
		assert.Equal(t, FragmentStatus("active"), FragmentStatusActive)
		assert.Equal(t, FragmentStatus("retracted"), FragmentStatusRetracted)
		assert.Equal(t, FragmentStatus("quarantined"), FragmentStatusQuarantined)
	})

	t.Run("fragment status field accepts active", func(t *testing.T) {
		f := Fragment{Status: FragmentStatusActive}
		assert.Equal(t, FragmentStatusActive, f.Status)
	})

	t.Run("fragment status field accepts retracted", func(t *testing.T) {
		f := Fragment{Status: FragmentStatusRetracted}
		assert.Equal(t, FragmentStatusRetracted, f.Status)
	})

	t.Run("fragment status field accepts quarantined", func(t *testing.T) {
		f := Fragment{Status: FragmentStatusQuarantined}
		assert.Equal(t, FragmentStatusQuarantined, f.Status)
	})

	t.Run("fragment recorded_to field is nil by default", func(t *testing.T) {
		f := Fragment{}
		assert.Nil(t, f.RecordedTo)
	})

	t.Run("fragment recorded_to field accepts a time pointer", func(t *testing.T) {
		now := time.Now().UTC()
		f := Fragment{RecordedTo: &now}
		require.NotNil(t, f.RecordedTo)
		assert.True(t, now.Equal(*f.RecordedTo))
	})

	t.Run("fragment retracted_at field accepts a time pointer", func(t *testing.T) {
		now := time.Now().UTC()
		f := Fragment{RetractedAt: &now}
		require.NotNil(t, f.RetractedAt)
		assert.True(t, now.Equal(*f.RetractedAt))
	})

	t.Run("status serializes to json", func(t *testing.T) {
		f := Fragment{
			FragmentID: "test-id",
			Status:     FragmentStatusRetracted,
		}
		b, err := json.Marshal(f)
		require.NoError(t, err)
		assert.Contains(t, string(b), `"status":"retracted"`)
	})

	t.Run("status omitted from json when zero value", func(t *testing.T) {
		f := Fragment{FragmentID: "test-id"}
		b, err := json.Marshal(f)
		require.NoError(t, err)
		assert.NotContains(t, string(b), `"status"`)
	})

	t.Run("recorded_to omitted from json when nil", func(t *testing.T) {
		f := Fragment{FragmentID: "test-id"}
		b, err := json.Marshal(f)
		require.NoError(t, err)
		assert.NotContains(t, string(b), `"recorded_to"`)
	})

	t.Run("recorded_to serializes to json when set", func(t *testing.T) {
		ts := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
		f := Fragment{
			FragmentID: "test-id",
			RecordedTo: &ts,
		}
		b, err := json.Marshal(f)
		require.NoError(t, err)
		assert.Contains(t, string(b), `"recorded_to"`)
	})

	t.Run("retracted_at serializes to json when set", func(t *testing.T) {
		ts := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
		f := Fragment{
			FragmentID:  "test-id",
			RetractedAt: &ts,
		}
		b, err := json.Marshal(f)
		require.NoError(t, err)
		assert.Contains(t, string(b), `"retracted_at"`)
	})
}
