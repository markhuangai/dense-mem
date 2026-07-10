package domain

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAssertionEnums(t *testing.T) {
	require.True(t, AssertionTierCandidate.IsValid())
	require.True(t, AssertionTierValidatedClaim.IsValid())
	require.True(t, AssertionTierFact.IsValid())
	require.True(t, AssertionTierDream.IsValid())
	require.False(t, AssertionTier("bad").IsValid())
	require.True(t, AssertionStatusActive.IsValid())
	require.True(t, AssertionStatusNeedsReview.IsValid())
	require.True(t, AssertionStatusQuarantined.IsValid())
	require.True(t, AssertionStatusSuperseded.IsValid())
	require.True(t, AssertionStatusDisputed.IsValid())
	require.True(t, AssertionStatusRetracted.IsValid())
	require.True(t, AssertionStatusRejected.IsValid())
	require.False(t, AssertionStatus("bad").IsValid())
	require.True(t, AssertionPolicyEventAppendOnly.IsValid())
	require.True(t, AssertionPolicyMultiState.IsValid())
	require.True(t, AssertionPolicySingleState.IsValid())
	require.True(t, AssertionPolicyVersioned.IsValid())
	require.False(t, AssertionPolicyFamily("bad").IsValid())
	require.True(t, EntityResolutionCanonical.IsValid())
	require.True(t, EntityResolutionProvisional.IsValid())
	require.True(t, EntityResolutionAmbiguous.IsValid())
	require.False(t, EntityResolutionStatus("bad").IsValid())
	for _, valueType := range []ValueType{
		ValueTypeString,
		ValueTypeNumber,
		ValueTypeBoolean,
		ValueTypeDate,
		ValueTypeDateTime,
	} {
		require.True(t, valueType.IsValid())
	}
	require.False(t, ValueType("bad").IsValid())
}

func TestEntityValidate(t *testing.T) {
	valid := Entity{
		EntityID:         "entity-1",
		ProfileID:        "team-1",
		CanonicalName:    "Dense-Mem",
		NormalizedName:   "dense mem",
		EntityType:       "project",
		ResolutionStatus: EntityResolutionCanonical,
		ResolutionConf:   0.9,
	}
	require.NoError(t, valid.Validate())

	cases := []struct {
		name   string
		mutate func(*Entity)
		want   string
	}{
		{"id", func(v *Entity) { v.EntityID = "" }, "entity_id"},
		{"team", func(v *Entity) { v.ProfileID = "" }, "team_id"},
		{"name", func(v *Entity) { v.CanonicalName = "" }, "canonical_name"},
		{"normalized", func(v *Entity) { v.NormalizedName = "" }, "normalized_name"},
		{"type", func(v *Entity) { v.EntityType = "" }, "entity_type"},
		{"status", func(v *Entity) { v.ResolutionStatus = "bad" }, "resolution_status"},
		{"confidence low", func(v *Entity) { v.ResolutionConf = -1 }, "resolution_conf"},
		{"confidence high", func(v *Entity) { v.ResolutionConf = 2 }, "resolution_conf"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := valid
			tc.mutate(&got)
			require.ErrorContains(t, got.Validate(), tc.want)
		})
	}
}

func TestTypedValueAndEvidenceSpanValidate(t *testing.T) {
	value := TypedValue{ValueID: "value-1", ValueType: ValueTypeString, Value: "Go"}
	require.NoError(t, value.Validate())
	for _, mutate := range []func(*TypedValue){
		func(v *TypedValue) { v.ValueID = "" },
		func(v *TypedValue) { v.ValueType = "bad" },
		func(v *TypedValue) { v.Value = "" },
	} {
		got := value
		mutate(&got)
		require.Error(t, got.Validate())
	}

	span := EvidenceSpan{FragmentID: "fragment-1", Start: 1, End: 3, SourceGroup: "turn-1"}
	require.NoError(t, span.Validate())
	for _, mutate := range []func(*EvidenceSpan){
		func(v *EvidenceSpan) { v.FragmentID = "" },
		func(v *EvidenceSpan) { v.Start = -1 },
		func(v *EvidenceSpan) { v.End = 1 },
		func(v *EvidenceSpan) { v.SourceGroup = "" },
	} {
		got := span
		mutate(&got)
		require.Error(t, got.Validate())
	}
}

func TestAssertionValidate(t *testing.T) {
	now := time.Now().UTC()
	later := now.Add(time.Hour)
	valid := Assertion{
		AssertionID:      "assertion-1",
		ProfileID:        "team-1",
		SubjectEntityID:  "entity-1",
		PredicateKey:     "uses",
		RelationshipType: "USES",
		ObjectEntityID:   "entity-2",
		Tier:             AssertionTierValidatedClaim,
		Status:           AssertionStatusActive,
		PolicyFamily:     AssertionPolicyMultiState,
		Polarity:         PolarityPlus,
		Modality:         ModalityAssertion,
		ValidFrom:        &now,
		ValidTo:          &later,
		ExtractConf:      0.9,
		ResolutionConf:   0.9,
		SourceQuality:    0.9,
		SupportCount:     1,
		SourceGroupCount: 1,
		Evidence: []EvidenceSpan{{
			FragmentID:  "fragment-1",
			Start:       0,
			End:         4,
			SourceGroup: "turn-1",
		}},
	}
	require.NoError(t, valid.Validate())

	tests := []struct {
		name   string
		mutate func(*Assertion)
	}{
		{"id", func(v *Assertion) { v.AssertionID = "" }},
		{"team", func(v *Assertion) { v.ProfileID = "" }},
		{"subject", func(v *Assertion) { v.SubjectEntityID = "" }},
		{"predicate", func(v *Assertion) { v.PredicateKey = "" }},
		{"relationship", func(v *Assertion) { v.RelationshipType = "" }},
		{"no object", func(v *Assertion) { v.ObjectEntityID = "" }},
		{"two objects", func(v *Assertion) { v.ObjectValue = &TypedValue{ValueID: "v", ValueType: ValueTypeString, Value: "x"} }},
		{"tier", func(v *Assertion) { v.Tier = "bad" }},
		{"status", func(v *Assertion) { v.Status = "bad" }},
		{"policy", func(v *Assertion) { v.PolicyFamily = "bad" }},
		{"polarity", func(v *Assertion) { v.Polarity = "bad" }},
		{"modality", func(v *Assertion) { v.Modality = "bad" }},
		{"time", func(v *Assertion) { v.ValidTo = &now }},
		{"extract low", func(v *Assertion) { v.ExtractConf = -1 }},
		{"extract high", func(v *Assertion) { v.ExtractConf = 2 }},
		{"resolution low", func(v *Assertion) { v.ResolutionConf = -1 }},
		{"resolution high", func(v *Assertion) { v.ResolutionConf = 2 }},
		{"quality low", func(v *Assertion) { v.SourceQuality = -1 }},
		{"quality high", func(v *Assertion) { v.SourceQuality = 2 }},
		{"support", func(v *Assertion) { v.SupportCount = 0 }},
		{"source groups zero", func(v *Assertion) { v.SourceGroupCount = 0 }},
		{"source groups high", func(v *Assertion) { v.SourceGroupCount = 2 }},
		{"evidence", func(v *Assertion) { v.Evidence = nil }},
		{"value", func(v *Assertion) { v.ObjectEntityID = ""; v.ObjectValue = &TypedValue{} }},
		{"invalid span", func(v *Assertion) { v.Evidence[0].End = 0 }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := valid
			got.Evidence = append([]EvidenceSpan(nil), valid.Evidence...)
			tc.mutate(&got)
			require.Error(t, got.Validate())
		})
	}
}
