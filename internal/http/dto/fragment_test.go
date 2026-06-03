package dto

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/http/validation"
)

// TestCreateFragmentRequest_Validation covers all AC-18/19/46 validation cases.
func TestCreateFragmentRequest_Validation(t *testing.T) {
	// AC-18: content required, non-blank, ≤8192 bytes
	t.Run("content required", func(t *testing.T) {
		req := &CreateFragmentRequest{}
		err := validation.ValidateStruct(req)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "required")
	})

	t.Run("content non-blank", func(t *testing.T) {
		req := &CreateFragmentRequest{Content: "   "}
		err := validation.ValidateStruct(req)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "notblank")
	})

	t.Run("content max 8192 bytes", func(t *testing.T) {
		req := &CreateFragmentRequest{Content: strings.Repeat("x", 8193)}
		err := validation.ValidateStruct(req)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "max")
	})

	t.Run("content at limit passes", func(t *testing.T) {
		req := &CreateFragmentRequest{Content: strings.Repeat("x", 8192)}
		err := validation.ValidateStruct(req)
		require.NoError(t, err)
	})

	// AC-46: source_type enum validation and defaults
	t.Run("invalid source_type rejected", func(t *testing.T) {
		req := &CreateFragmentRequest{Content: "test", SourceType: "typo"}
		err := validation.ValidateStruct(req)
		require.Error(t, err)
	})

	t.Run("valid source_type conversation accepted", func(t *testing.T) {
		req := &CreateFragmentRequest{Content: "test", SourceType: "conversation"}
		err := validation.ValidateStruct(req)
		require.NoError(t, err)
	})

	t.Run("valid source_type document accepted", func(t *testing.T) {
		req := &CreateFragmentRequest{Content: "test", SourceType: "document"}
		err := validation.ValidateStruct(req)
		require.NoError(t, err)
	})

	t.Run("valid source_type observation accepted", func(t *testing.T) {
		req := &CreateFragmentRequest{Content: "test", SourceType: "observation"}
		err := validation.ValidateStruct(req)
		require.NoError(t, err)
	})

	t.Run("valid source_type manual accepted", func(t *testing.T) {
		req := &CreateFragmentRequest{Content: "test", SourceType: "manual"}
		err := validation.ValidateStruct(req)
		require.NoError(t, err)
	})

	t.Run("omitted source_type accepted (defaults to manual)", func(t *testing.T) {
		req := &CreateFragmentRequest{Content: "test"}
		err := validation.ValidateStruct(req)
		require.NoError(t, err)
	})

	// AC-18: source and idempotency_key length bounds
	t.Run("source max 256 chars", func(t *testing.T) {
		req := &CreateFragmentRequest{
			Content: "test",
			Source:  strings.Repeat("x", 257),
		}
		err := validation.ValidateStruct(req)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "max")
	})

	t.Run("source at limit passes", func(t *testing.T) {
		req := &CreateFragmentRequest{
			Content: "test",
			Source:  strings.Repeat("x", 256),
		}
		err := validation.ValidateStruct(req)
		require.NoError(t, err)
	})

	t.Run("idempotency_key max 128 chars", func(t *testing.T) {
		req := &CreateFragmentRequest{
			Content:        "test",
			IdempotencyKey: strings.Repeat("x", 129),
		}
		err := validation.ValidateStruct(req)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "max")
	})

	t.Run("idempotency_key at limit passes", func(t *testing.T) {
		req := &CreateFragmentRequest{
			Content:        "test",
			IdempotencyKey: strings.Repeat("x", 128),
		}
		err := validation.ValidateStruct(req)
		require.NoError(t, err)
	})

	// AC-19: labels bounded count and per-item length
	t.Run("labels max 20 items", func(t *testing.T) {
		tooMany := make([]string, 21)
		for i := range tooMany {
			tooMany[i] = "x"
		}
		req := &CreateFragmentRequest{
			Content: "test",
			Labels:  tooMany,
		}
		err := validation.ValidateStruct(req)
		require.Error(t, err)
	})

	t.Run("labels at limit passes", func(t *testing.T) {
		atLimit := make([]string, 20)
		for i := range atLimit {
			atLimit[i] = "label"
		}
		req := &CreateFragmentRequest{
			Content: "test",
			Labels:  atLimit,
		}
		err := validation.ValidateStruct(req)
		require.NoError(t, err)
	})

	t.Run("label item max 64 chars", func(t *testing.T) {
		req := &CreateFragmentRequest{
			Content: "test",
			Labels:  []string{strings.Repeat("x", 65)},
		}
		err := validation.ValidateStruct(req)
		require.Error(t, err)
	})

	t.Run("label item at limit passes", func(t *testing.T) {
		req := &CreateFragmentRequest{
			Content: "test",
			Labels:  []string{strings.Repeat("x", 64)},
		}
		err := validation.ValidateStruct(req)
		require.NoError(t, err)
	})

	t.Run("label item non-blank", func(t *testing.T) {
		req := &CreateFragmentRequest{
			Content: "test",
			Labels:  []string{"   "},
		}
		err := validation.ValidateStruct(req)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "notblank")
	})
}

// TestValidateMetadataSize covers AC-18 metadata size validation.
func TestValidateMetadataSize(t *testing.T) {
	t.Run("nil metadata passes", func(t *testing.T) {
		err := ValidateMetadataSize(nil)
		require.NoError(t, err)
	})

	t.Run("empty metadata passes", func(t *testing.T) {
		err := ValidateMetadataSize(map[string]any{})
		require.NoError(t, err)
	})

	t.Run("small metadata passes", func(t *testing.T) {
		metadata := map[string]any{
			"key": "value",
			"num": 42,
		}
		err := ValidateMetadataSize(metadata)
		require.NoError(t, err)
	})

	t.Run("metadata at limit passes", func(t *testing.T) {
		// Create metadata that when JSON-encoded is exactly MaxMetadataBytes
		// JSON encoding of {"k":"..."} is 8 + value length: {"k":" (6) + value + "} (2)
		valueLen := MaxMetadataBytes - 8
		metadata := map[string]any{
			"k": strings.Repeat("x", valueLen),
		}
		data, _ := json.Marshal(metadata)
		require.Len(t, data, MaxMetadataBytes)

		err := ValidateMetadataSize(metadata)
		require.NoError(t, err)
	})

	t.Run("oversized metadata rejected", func(t *testing.T) {
		// Create metadata that exceeds limit
		valueLen := MaxMetadataBytes - 6 // would be MaxMetadataBytes + 1
		metadata := map[string]any{
			"k": strings.Repeat("x", valueLen),
		}
		data, _ := json.Marshal(metadata)
		require.Greater(t, len(data), MaxMetadataBytes)

		err := ValidateMetadataSize(metadata)
		require.Error(t, err)
		var sizeErr *MetadataSizeError
		require.ErrorAs(t, err, &sizeErr)
		assert.Equal(t, MaxMetadataBytes, sizeErr.Max)
		assert.Greater(t, sizeErr.Size, MaxMetadataBytes)
		assert.Equal(t, "metadata size exceeds maximum", sizeErr.Error())
	})

	t.Run("marshal error returned", func(t *testing.T) {
		err := ValidateMetadataSize(map[string]any{"bad": func() {}})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unsupported type")
	})

	t.Run("complex metadata within limit", func(t *testing.T) {
		metadata := map[string]any{
			"string_field": "some value",
			"number_field": 12345,
			"bool_field":   true,
			"nested": map[string]any{
				"inner": "value",
			},
			"array": []string{"a", "b", "c"},
		}
		err := ValidateMetadataSize(metadata)
		require.NoError(t, err)
	})
}

// TestListFragmentsRequest_Validation covers list request validation.
func TestListFragmentsRequest_Validation(t *testing.T) {
	t.Run("limit min 0", func(t *testing.T) {
		req := &ListFragmentsRequest{Limit: 0}
		err := validation.ValidateStruct(req)
		require.NoError(t, err)
	})

	t.Run("limit max 100", func(t *testing.T) {
		req := &ListFragmentsRequest{Limit: 100}
		err := validation.ValidateStruct(req)
		require.NoError(t, err)
	})

	t.Run("limit over max rejected", func(t *testing.T) {
		req := &ListFragmentsRequest{Limit: 101}
		err := validation.ValidateStruct(req)
		require.Error(t, err)
	})

	t.Run("cursor max 256", func(t *testing.T) {
		req := &ListFragmentsRequest{Cursor: strings.Repeat("x", 257)}
		err := validation.ValidateStruct(req)
		require.Error(t, err)
	})

	t.Run("cursor at limit passes", func(t *testing.T) {
		req := &ListFragmentsRequest{Cursor: strings.Repeat("x", 256)}
		err := validation.ValidateStruct(req)
		require.NoError(t, err)
	})

	t.Run("invalid source_type rejected", func(t *testing.T) {
		req := &ListFragmentsRequest{SourceType: "invalid"}
		err := validation.ValidateStruct(req)
		require.Error(t, err)
	})

	t.Run("valid source_type accepted", func(t *testing.T) {
		for _, st := range []string{"conversation", "document", "observation", "manual"} {
			req := &ListFragmentsRequest{SourceType: st}
			err := validation.ValidateStruct(req)
			require.NoError(t, err, "source_type %s should be valid", st)
		}
	})
}

// TestDTOEnumMatchesDomainEnum ensures AC-46: DTO enum stays in sync with domain.ValidSourceTypes().
func TestDTOEnumMatchesDomainEnum(t *testing.T) {
	// The DTO validation uses "oneof=conversation document observation manual"
	// This test ensures that list stays in sync with domain.ValidSourceTypes()
	validTypes := domain.ValidSourceTypes()
	dtoTypes := []string{"conversation", "document", "observation", "manual"}

	require.Len(t, validTypes, len(dtoTypes), "DTO enum count must match domain enum count")

	for i, dtoType := range dtoTypes {
		assert.Equal(t, dtoType, string(validTypes[i]),
			"DTO enum[%d] = %q must match domain enum[%d] = %q",
			i, dtoType, i, validTypes[i])
	}

	// Verify all domain types are in DTO enum
	dtoSet := make(map[string]bool)
	for _, dt := range dtoTypes {
		dtoSet[dt] = true
	}

	for _, domainType := range validTypes {
		assert.True(t, dtoSet[string(domainType)],
			"domain type %q must be in DTO enum", domainType)
	}
}
