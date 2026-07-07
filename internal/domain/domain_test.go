package domain

import (
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/http/dto"
	"github.com/markhuangai/dense-mem/internal/http/validation"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDreamStatusIsValid(t *testing.T) {
	for _, status := range []DreamStatus{
		DreamStatusProposed,
		DreamStatusReinforced,
		DreamStatusStale,
		DreamStatusRejected,
		DreamStatusPromoted,
	} {
		if !status.IsValid() {
			t.Fatalf("status %q should be valid", status)
		}
	}
	if DreamStatus("unknown").IsValid() {
		t.Fatal("unknown dream status should be invalid")
	}
}

// TestKnowledgeContractFieldNames verifies exact field names on all three contract structs match canonical names.
func TestKnowledgeContractFieldNames(t *testing.T) {
	t.Run("SourceFragmentContract has correct field names", func(t *testing.T) {
		typ := reflect.TypeOf(SourceFragmentContract{})

		expectedFields := map[string]string{
			"FragmentID":     "fragment_id",
			"Connector":      "connector",
			"SourceID":       "source_id",
			"Content":        "content",
			"Embedding":      "embedding",
			"Classification": "classification",
		}

		for fieldName, jsonTag := range expectedFields {
			field, found := typ.FieldByName(fieldName)
			require.True(t, found, "Field %s not found", fieldName)

			tag := field.Tag.Get("json")
			assert.Equal(t, jsonTag, tag, "Field %s json tag mismatch", fieldName)
		}
	})

	t.Run("ClaimContract has correct field names", func(t *testing.T) {
		typ := reflect.TypeOf(ClaimContract{})

		expectedFields := map[string]string{
			"ClaimID":           "claim_id",
			"Predicate":         "predicate",
			"Modality":          "modality",
			"Status":            "status",
			"EntailmentVerdict": "entailment_verdict",
			"ExtractConf":       "extract_conf",
		}

		for fieldName, jsonTag := range expectedFields {
			field, found := typ.FieldByName(fieldName)
			require.True(t, found, "Field %s not found", fieldName)

			tag := field.Tag.Get("json")
			assert.Equal(t, jsonTag, tag, "Field %s json tag mismatch", fieldName)
		}
	})

	t.Run("FactContract has correct field names", func(t *testing.T) {
		typ := reflect.TypeOf(FactContract{})

		expectedFields := map[string]string{
			"FactID":     "fact_id",
			"Status":     "status",
			"TruthScore": "truth_score",
			"ValidFrom":  "valid_from",
			"ValidTo":    "valid_to",
			"RecordedAt": "recorded_at",
			"RecordedTo": "recorded_to",
		}

		for fieldName, jsonTag := range expectedFields {
			field, found := typ.FieldByName(fieldName)
			require.True(t, found, "Field %s not found", fieldName)

			tag := field.Tag.Get("json")
			assert.Equal(t, jsonTag, tag, "Field %s json tag mismatch", fieldName)
		}
	})
}

// TestKnowledgeContractRelationshipConstants verifies all six relationship constants are defined.
func TestKnowledgeContractRelationshipConstants(t *testing.T) {
	expectedConstants := []string{
		SUPPORTED_BY,
		PROMOTES_TO,
		SUPERSEDED_BY,
		CONTRADICTS,
		SUBJECT,
		OBJECT,
	}

	expectedValues := []string{
		"SUPPORTED_BY",
		"PROMOTES_TO",
		"SUPERSEDED_BY",
		"CONTRADICTS",
		"SUBJECT",
		"OBJECT",
	}

	for i, expected := range expectedValues {
		assert.Equal(t, expected, expectedConstants[i], "Constant at index %d mismatch", i)
	}
}

// TestDTOValidationProfileCreate verifies CreateProfileRequest validation rules.
func TestDTOValidationProfileCreate(t *testing.T) {
	t.Run("valid profile passes validation", func(t *testing.T) {
		req := dto.CreateProfileRequest{
			Name:        "Test Profile",
			Description: "A test profile",
			Metadata:    map[string]any{"key": "value"},
			Config:      map[string]any{"setting": true},
		}

		err := validation.ValidateStruct(&req)
		assert.NoError(t, err)
	})

	t.Run("name too short fails validation", func(t *testing.T) {
		req := dto.CreateProfileRequest{
			Name: "ab",
		}

		err := validation.ValidateStruct(&req)
		assert.Error(t, err)
	})

	t.Run("name too long fails validation", func(t *testing.T) {
		req := dto.CreateProfileRequest{
			Name: string(make([]byte, 101)),
		}

		err := validation.ValidateStruct(&req)
		assert.Error(t, err)
	})

	t.Run("blank name fails validation", func(t *testing.T) {
		req := dto.CreateProfileRequest{
			Name: "   ",
		}

		err := validation.ValidateStruct(&req)
		assert.Error(t, err)
	})

	t.Run("description too long fails validation", func(t *testing.T) {
		req := dto.CreateProfileRequest{
			Name:        "Test",
			Description: string(make([]byte, 501)),
		}

		err := validation.ValidateStruct(&req)
		assert.Error(t, err)
	})
}

// TestDTOValidationAPIKeyCreate verifies CreateAPIKeyRequest validation rules.
func TestDTOValidationAPIKeyCreate(t *testing.T) {
	t.Run("valid API key request passes validation", func(t *testing.T) {
		req := dto.CreateAPIKeyRequest{
			RateLimit: 100,
		}

		err := validation.ValidateStruct(&req)
		assert.NoError(t, err)
	})

	t.Run("feedback scope request passes validation", func(t *testing.T) {
		scopes := []string{"read", "write", "feedback:read"}
		req := dto.CreateAPIKeyRequest{
			Scopes:    &scopes,
			RateLimit: 100,
		}

		err := validation.ValidateStruct(&req)
		assert.NoError(t, err)
	})
}

// TestNotBlankValidator verifies whitespace-only strings fail validation.
func TestNotBlankValidator(t *testing.T) {
	testCases := []struct {
		name     string
		input    string
		expected bool // true = should pass, false = should fail
	}{
		{"empty string", "", false},
		{"only spaces", "   ", false},
		{"only tabs", "\t\t", false},
		{"only newlines", "\n\n", false},
		{"mixed whitespace", " \t\n ", false},
		{"single character (fails min)", "a", false}, // min=3 requirement
		{"three characters", "abc", true},
		{"word with spaces", "  hello  ", true},
		{"normal text", "Hello World", true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req := dto.CreateProfileRequest{
				Name: tc.input,
			}

			err := validation.ValidateStruct(&req)
			if tc.expected {
				assert.NoError(t, err, "Expected '%s' to pass validation", tc.input)
			} else {
				assert.Error(t, err, "Expected '%s' to fail validation", tc.input)
			}
		})
	}
}

// TestProfileInterface verifies Profile implements ProfileModel.
func TestProfileInterface(t *testing.T) {
	id := uuid.New()
	now := time.Now()

	profile := &Profile{
		ID:          id,
		Name:        "Test",
		Description: "Test Description",
		Metadata:    map[string]any{"key": "value"},
		Config:      map[string]any{"setting": true},
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	var model ProfileModel = profile

	assert.Equal(t, id, model.GetID())
	assert.Equal(t, "Test", model.GetName())
	assert.Equal(t, "Test Description", model.GetDescription())
	assert.Equal(t, now, model.GetCreatedAt())
	assert.Equal(t, now, model.GetUpdatedAt())
}

// TestAPIKeyInterface verifies APIKey implements APIKeyModel.
func TestAPIKeyInterface(t *testing.T) {
	id := uuid.New()
	profileID := uuid.New()
	now := time.Now()

	apiKey := &APIKey{
		ID:        id,
		ProfileID: profileID,
		Label:     "Test Key",
		KeyHash:   "hash123",
		Scopes:    []string{"read"},
		RateLimit: 100,
		CreatedAt: now,
	}

	var model APIKeyModel = apiKey

	assert.Equal(t, id, model.GetID())
	assert.Equal(t, profileID, model.GetProfileID())
	assert.Equal(t, "Test Key", model.GetLabel())
	assert.Equal(t, "hash123", model.GetKeyHash())
	assert.Equal(t, []string{"read"}, model.GetScopes())
	assert.Equal(t, 100, model.GetRateLimit())
}

// TestAuditLogEntryInterface verifies AuditLogEntry implements AuditEntryModel
// with fields that match the audit_log DB schema 1:1.
func TestAuditLogEntryInterface(t *testing.T) {
	id := uuid.New()
	profileID := uuid.New()
	actorKeyID := uuid.New()
	now := time.Now()

	entry := &AuditLogEntry{
		ID:            id,
		ProfileID:     &profileID,
		Timestamp:     now,
		Operation:     "CREATE",
		EntityType:    "profile",
		EntityID:      profileID.String(),
		BeforePayload: map[string]any{"before": "state"},
		AfterPayload:  map[string]any{"after": "state"},
		ActorKeyID:    &actorKeyID,
		ActorRole:     "system",
		ClientIP:      "127.0.0.1",
		CorrelationID: "corr-123",
		Metadata:      map[string]any{"note": "test"},
	}

	var model AuditEntryModel = entry

	assert.Equal(t, id, model.GetID())
	assert.Equal(t, &profileID, model.GetProfileID())
	assert.Equal(t, now, model.GetTimestamp())
	assert.Equal(t, "CREATE", model.GetOperation())
	assert.Equal(t, "profile", model.GetEntityType())
	assert.Equal(t, profileID.String(), model.GetEntityID())
	assert.Equal(t, &actorKeyID, model.GetActorKeyID())
	assert.Equal(t, "system", model.GetActorRole())
	assert.Equal(t, "127.0.0.1", model.GetClientIP())
	assert.Equal(t, "corr-123", model.GetCorrelationID())
	assert.Equal(t, map[string]any{"before": "state"}, model.GetBeforePayload())
	assert.Equal(t, map[string]any{"after": "state"}, model.GetAfterPayload())
	assert.Equal(t, map[string]any{"note": "test"}, model.GetMetadata())
}
