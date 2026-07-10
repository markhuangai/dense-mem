package assertionservice

import (
	"context"
	"errors"
	"testing"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/stretchr/testify/require"
)

type captureStore struct {
	profileID string
	bundle    Bundle
	result    WriteResult
	err       error
}

func (s *captureStore) WriteBundle(_ context.Context, profileID string, bundle Bundle) (WriteResult, error) {
	s.profileID = profileID
	s.bundle = bundle
	return s.result, s.err
}

func TestNormalizeGraphTokens(t *testing.T) {
	require.Equal(t, "dense mem", NormalizeName("  Dense   MEM "))
	require.Equal(t, "project_type", NormalizeEntityType("Project-Type"))
	require.Equal(t, "gave_demo_of", NormalizePredicate(" Gave demo-of! "))
	require.Equal(t, "GAVE_DEMO_OF", RelationshipType(" Gave demo-of! "))
	require.Empty(t, NormalizePredicate("---"))
	require.Len(t, NormalizePredicate("abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklmnop"), 64)
}

func TestNewEntity(t *testing.T) {
	first, err := NewEntity("team-1", "Dense-Mem", "Project", []string{"dense mem", "DM", "DM"})
	require.NoError(t, err)
	second, err := NewEntity("team-1", "Dense-Mem", "Project", nil)
	require.NoError(t, err)
	require.Equal(t, first.EntityID, second.EntityID)
	require.Equal(t, "dense mem", first.NormalizedName)
	require.Equal(t, "project", first.EntityType)
	require.Equal(t, []string{"DM"}, first.Aliases)
	require.Equal(t, domain.EntityResolutionCanonical, first.ResolutionStatus)
	_, err = NewEntity("team-1", "", "Project", nil)
	require.Error(t, err)
	_, err = NewEntity("team-1", "Dense-Mem", "---", nil)
	require.Error(t, err)
}

func TestNewValue(t *testing.T) {
	first, err := NewValue("team-1", domain.ValueTypeNumber, "42", "42 seconds", "seconds")
	require.NoError(t, err)
	second, err := NewValue("team-1", domain.ValueTypeNumber, "42", "", "seconds")
	require.NoError(t, err)
	require.Equal(t, first.ValueID, second.ValueID)
	require.Equal(t, "42 seconds", first.Display)
	_, err = NewValue("team-1", "bad", "42", "", "")
	require.Error(t, err)
	_, err = NewValue("team-1", domain.ValueTypeString, "", "", "")
	require.Error(t, err)
}

func TestValidateBundle(t *testing.T) {
	bundle := validBundle(t)
	require.NoError(t, ValidateBundle("team-1", bundle))

	tests := []struct {
		name   string
		mutate func(*Bundle)
	}{
		{"empty team", func(_ *Bundle) {}},
		{"no entities", func(v *Bundle) { v.Entities = nil }},
		{"no assertions", func(v *Bundle) { v.Assertions = nil }},
		{"invalid entity", func(v *Bundle) { v.Entities[0].CanonicalName = "" }},
		{"entity team", func(v *Bundle) { v.Entities[0].ProfileID = "team-2" }},
		{"duplicate entity", func(v *Bundle) { v.Entities = append(v.Entities, v.Entities[0]) }},
		{"invalid assertion", func(v *Bundle) { v.Assertions[0].Tier = "bad" }},
		{"assertion team", func(v *Bundle) { v.Assertions[0].ProfileID = "team-2" }},
		{"duplicate assertion", func(v *Bundle) { v.Assertions = append(v.Assertions, v.Assertions[0]) }},
		{"subject missing", func(v *Bundle) { v.Assertions[0].SubjectEntityID = "missing" }},
		{"object missing", func(v *Bundle) { v.Assertions[0].ObjectEntityID = "missing" }},
		{"unsafe relationship", func(v *Bundle) { v.Assertions[0].RelationshipType = "USES BAD" }},
		{"reserved relationship", func(v *Bundle) {
			v.Assertions[0].PredicateKey = "mentions"
			v.Assertions[0].RelationshipType = "MENTIONS"
		}},
		{"wrong relationship", func(v *Bundle) { v.Assertions[0].RelationshipType = "LIKES" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := validBundle(t)
			tc.mutate(&got)
			profileID := "team-1"
			if tc.name == "empty team" {
				profileID = ""
			}
			require.Error(t, ValidateBundle(profileID, got))
		})
	}
}

func TestValidateBundleWithValue(t *testing.T) {
	bundle := validBundle(t)
	value, err := NewValue("team-1", domain.ValueTypeDate, "2026-07-10", "", "")
	require.NoError(t, err)
	bundle.Assertions[0].ObjectEntityID = ""
	bundle.Assertions[0].ObjectValue = &value
	require.NoError(t, ValidateBundle("team-1", bundle))
}

func TestServiceWriteBundle(t *testing.T) {
	store := &captureStore{}
	service := New(store)
	bundle := validBundle(t)
	store.result = WriteResult{Superseded: []SupersededAssertion{{AssertionID: "old"}}}
	result, err := service.WriteBundle(context.Background(), "team-1", bundle)
	require.NoError(t, err)
	require.Equal(t, store.result, result)
	require.Equal(t, "team-1", store.profileID)
	require.Equal(t, bundle, store.bundle)

	store.err = errors.New("write failed")
	_, err = service.WriteBundle(context.Background(), "team-1", bundle)
	require.ErrorContains(t, err, "write failed")
	_, err = New(nil).WriteBundle(context.Background(), "team-1", bundle)
	require.Error(t, err)
	_, err = (*Service)(nil).WriteBundle(context.Background(), "team-1", bundle)
	require.Error(t, err)
	_, err = service.WriteBundle(context.Background(), "", bundle)
	require.Error(t, err)
}

func validBundle(t *testing.T) Bundle {
	t.Helper()
	subject, err := NewEntity("team-1", "Dense-Mem", "project", nil)
	require.NoError(t, err)
	object, err := NewEntity("team-1", "Neo4j", "technology", nil)
	require.NoError(t, err)
	return Bundle{
		Entities: []domain.Entity{subject, object},
		Assertions: []domain.Assertion{{
			AssertionID:      "assertion-1",
			ProfileID:        "team-1",
			SubjectEntityID:  subject.EntityID,
			PredicateKey:     "uses",
			RelationshipType: "USES",
			ObjectEntityID:   object.EntityID,
			Tier:             domain.AssertionTierValidatedClaim,
			Status:           domain.AssertionStatusActive,
			PolicyFamily:     domain.AssertionPolicyMultiState,
			Polarity:         domain.PolarityPlus,
			Modality:         domain.ModalityAssertion,
			ExtractConf:      0.9,
			ResolutionConf:   0.9,
			SourceQuality:    0.9,
			SupportCount:     1,
			SourceGroupCount: 1,
			Evidence: []domain.EvidenceSpan{{
				FragmentID:  "fragment-1",
				Start:       0,
				End:         10,
				SourceGroup: "turn-1",
			}},
			ProjectionVersion: ProjectionVersion,
		}},
	}
}
