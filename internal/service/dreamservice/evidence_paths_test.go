package dreamservice

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/repository"
)

func TestBuildDreamPathsUsesOnlySupportedDirectedTwoHopRelationships(t *testing.T) {
	inputs := []repository.DreamInput{
		{
			RelationshipID:   "relationship-a-b",
			OwnerProfileID:   "owner-b",
			Version:          3,
			Status:           "active",
			SubjectEntityID:  "entity-a",
			SubjectName:      "A",
			SubjectKind:      "project",
			PredicateKey:     "uses",
			PredicateVersion: 2,
			ObjectEntityID:   "entity-b",
			ObjectName:       "B",
			ObjectKind:       "product",
			Evidence: []repository.DreamEvidence{
				{SupportID: "support-a-1", Content: "A uses B.", Authority: "primary"},
				{SupportID: "support-a-2", Content: "A keeps using B.", Authority: "primary"},
				{SupportID: "support-a-3", Content: "This third excerpt is not sent.", Authority: "primary"},
			},
		},
		{
			RelationshipID:   "relationship-b-c",
			OwnerProfileID:   "owner-a",
			Version:          4,
			Status:           "pending_evidence",
			SubjectEntityID:  "entity-b",
			SubjectName:      "B",
			SubjectKind:      "product",
			PredicateKey:     "informs",
			PredicateVersion: 7,
			ObjectEntityID:   "entity-c",
			ObjectName:       "C",
			ObjectKind:       "concept",
			Evidence: []repository.DreamEvidence{
				{SupportID: "support-b-1", Content: "B informs C.", Authority: "secondary"},
			},
		},
		{
			RelationshipID:   "relationship-b-a",
			Version:          1,
			Status:           "active",
			SubjectEntityID:  "entity-b",
			SubjectName:      "B",
			SubjectKind:      "product",
			PredicateKey:     "loops_to",
			PredicateVersion: 1,
			ObjectEntityID:   "entity-a",
			ObjectName:       "A",
			ObjectKind:       "project",
			Evidence:         []repository.DreamEvidence{{Content: "B loops to A.", Authority: "primary"}},
		},
		{
			RelationshipID:  "relationship-missing-evidence",
			Version:         1,
			Status:          "active",
			SubjectEntityID: "entity-c",
			SubjectKind:     "concept",
			ObjectEntityID:  "entity-d",
			ObjectKind:      "concept",
		},
	}
	predicates := []repository.DreamTargetPredicate{
		{
			PredicateKey:        "depends_on",
			Version:             2,
			AllowedSubjectKinds: []string{"project"},
			AllowedObjectKinds:  []string{"concept"},
			RelationshipKind:    "state",
			CurrentCardinality:  "many",
		},
		{
			PredicateKey:        "enables",
			Version:             3,
			AllowedSubjectKinds: []string{"project"},
			AllowedObjectKinds:  []string{"concept"},
			RelationshipKind:    "event",
			CurrentCardinality:  "many",
		},
		{
			PredicateKey:        "wrong_kind",
			Version:             1,
			AllowedSubjectKinds: []string{"person"},
			AllowedObjectKinds:  []string{"concept"},
		},
	}

	paths := buildDreamPaths(inputs, predicates, 2)
	require.Len(t, paths, 1)
	path := paths[0]
	assert.Equal(t, "entity-a", path.Subject.ID)
	assert.Equal(t, "entity-b", path.Middle.ID)
	assert.Equal(t, "entity-c", path.Object.ID)
	require.Len(t, path.Premises, 2)
	assert.Equal(t, "relationship-a-b", path.Premises[0].Input.RelationshipID)
	assert.Equal(t, "relationship-b-c", path.Premises[1].Input.RelationshipID)
	assert.Len(t, path.Premises[0].Input.Evidence, 2)
	assert.Equal(t, "evidence_1", path.Premises[0].Input.Evidence[0].EvidenceRef)
	assert.Equal(t, "A uses B.", path.Premises[0].Input.Evidence[0].Content)
	assert.Len(t, path.AllowedPredicates, 2)
	assert.Equal(t, 8, dreamPathLimit(2))
	assert.Equal(t, maxDreamPathsPerGeneration, dreamPathLimit(20))
}

func TestBuildDreamPathsCapsAllowedPredicatesDeterministically(t *testing.T) {
	predicates := make([]repository.DreamTargetPredicate, 0, maxDreamAllowedPredicatesPerPath+1)
	for index := maxDreamAllowedPredicatesPerPath; index >= 0; index-- {
		predicates = append(predicates, repository.DreamTargetPredicate{
			PredicateKey:        fmt.Sprintf("predicate_%03d", index),
			Version:             1,
			AllowedSubjectKinds: []string{"project"},
			AllowedObjectKinds:  []string{"concept"},
			RelationshipKind:    "state",
			CurrentCardinality:  "many",
		})
	}

	paths := buildDreamPaths(testDreamPathInputs(), predicates, 10)
	require.Len(t, paths, 1)
	require.Len(t, paths[0].AllowedPredicates, maxDreamAllowedPredicatesPerPath)
	assert.Equal(t, "predicate_000", paths[0].AllowedPredicates[0].PredicateKey)
	assert.Equal(t, fmt.Sprintf("predicate_%03d", maxDreamAllowedPredicatesPerPath-1), paths[0].AllowedPredicates[maxDreamAllowedPredicatesPerPath-1].PredicateKey)
	assert.Equal(t, "predicate_1", paths[0].AllowedPredicates[0].PredicateRef)
	assert.Equal(t, fmt.Sprintf("predicate_%d", maxDreamAllowedPredicatesPerPath), paths[0].AllowedPredicates[maxDreamAllowedPredicatesPerPath-1].PredicateRef)
}

func TestDreamPathProposalKeepsExactDerivationsAndFiltersUnavailableTargets(t *testing.T) {
	paths := buildDreamPaths(testDreamPathInputs(), testDreamPathPredicates(), 1)
	require.Len(t, paths, 1)
	path := paths[0]
	require.Len(t, path.AllowedPredicates, 2)

	generated := GeneratedDream{
		PathRef:         path.PathRef,
		PredicateRef:    path.AllowedPredicates[0].PredicateRef,
		EvidenceRefs:    []string{"evidence_1", "evidence_3"},
		Hypothesis:      "A may depend on C.",
		Rationale:       "The supplied evidence supports a possible connection.",
		WhatIf:          "What if independent evidence confirms it?",
		PossibleOutcome: "Collect independent evidence before acceptance.",
		Likelihood:      0.4,
		Confidence:      0.6,
	}
	proposals, rejected := dreamProposalsFromPaths(
		[]GeneratedDream{
			generated,
			{PathRef: path.PathRef, PredicateRef: generated.PredicateRef, EvidenceRefs: []string{"evidence_1", "evidence_1"}},
			{PathRef: "unknown", PredicateRef: generated.PredicateRef, EvidenceRefs: generated.EvidenceRefs},
		},
		paths,
		3,
		"provider-model",
	)
	require.Equal(t, 2, rejected)
	require.Len(t, proposals, 1)
	proposal := proposals[0]
	assert.Equal(t, "provider", proposal.GeneratorKind)
	assert.Equal(t, "provider-model", proposal.GeneratorVersion)
	assert.Equal(t, []string{"owner-a", "owner-b"}, proposal.SourceOwnerProfileIDs)
	require.Len(t, proposal.Derivations, 2)
	assert.Equal(t, "support-a-1", proposal.Derivations[0].SupportID)
	assert.Equal(t, "A uses B.", proposal.Derivations[0].Quote)
	assert.Equal(t, "support-b-1", proposal.Derivations[1].SupportID)
	assert.Equal(t, "B informs C.", proposal.Derivations[1].Quote)

	sourceRefs := dreamPathSourceRefs(path)
	require.Len(t, sourceRefs, 2)
	assert.Equal(t, "relationship-a-b", sourceRefs[0].ID)
	assert.Equal(t, "relationship-b-c", sourceRefs[1].ID)

	evaluations := dreamPathEvaluationInputs(append(paths, DreamPath{}))
	require.Len(t, evaluations, 1)
	assert.Equal(t, "relationship-a-b", evaluations[0].FirstRelationshipID)
	assert.Len(t, evaluations[0].AllowedPredicateFingerprint, 64)
	assert.Equal(t, paths, dreamPathsForEvaluationInputs(paths, evaluations))
	assert.Empty(t, dreamPathsForEvaluationInputs(paths, nil))

	targets := dreamTargetCandidates(paths)
	require.Len(t, targets, 2)
	filtered := dreamPathsForAvailableTargets(paths, targets[:1])
	require.Len(t, filtered, 1)
	require.Len(t, filtered[0].AllowedPredicates, 1)
	assert.Equal(t, targets[0].PredicateRef, filtered[0].AllowedPredicates[0].PredicateRef)
	filteredEvaluations := dreamPathEvaluationInputs(filtered)
	require.Len(t, filteredEvaluations, 1)
	assert.NotEqual(t, evaluations[0].AllowedPredicateFingerprint, filteredEvaluations[0].AllowedPredicateFingerprint)
}

func TestBuildDreamPathsSkipsInvalidSecondPremisesAndUsesNonDurableDisplayFallbacks(t *testing.T) {
	inputs := testDreamPathInputs()
	first := inputs[0]
	selfLoop := inputs[1]
	selfLoop.RelationshipID = "relationship-b-b"
	selfLoop.ObjectEntityID = selfLoop.SubjectEntityID
	selfLoop.ObjectName = "B"
	missingEndpoint := inputs[1]
	missingEndpoint.RelationshipID = "relationship-b-missing"
	missingEndpoint.ObjectEntityID = ""
	missingEndpoint.ObjectValueID = ""
	missingEndpoint.ObjectName = ""

	predicates := testDreamPathPredicates()
	assert.Empty(t, buildDreamPaths([]repository.DreamInput{first, selfLoop, missingEndpoint}, predicates, 1))

	inputs[0].ObjectName = ""
	inputs[1].ObjectName = ""
	paths := buildDreamPaths(inputs, predicates, 1)
	require.Len(t, paths, 1)
	assert.Equal(t, "unnamed product", paths[0].Middle.Display)
	assert.Equal(t, "unnamed concept", paths[0].Object.Display)
	assert.NotEqual(t, inputs[0].ObjectEntityID, paths[0].Middle.Display)
	assert.NotEqual(t, inputs[1].ObjectEntityID, paths[0].Object.Display)
}

func testDreamPathInputs() []repository.DreamInput {
	return []repository.DreamInput{
		{
			RelationshipID: "relationship-a-b", OwnerProfileID: "owner-b", Version: 3, Status: "active",
			SubjectEntityID: "entity-a", SubjectName: "A", SubjectKind: "project", PredicateKey: "uses",
			PredicateVersion: 2, ObjectEntityID: "entity-b", ObjectName: "B", ObjectKind: "product",
			Evidence: []repository.DreamEvidence{
				{SupportID: "support-a-1", Content: "A uses B.", Authority: "primary"},
				{SupportID: "support-a-2", Content: "A keeps using B.", Authority: "primary"},
			},
		},
		{
			RelationshipID: "relationship-b-c", OwnerProfileID: "owner-a", Version: 4, Status: "pending_evidence",
			SubjectEntityID: "entity-b", SubjectName: "B", SubjectKind: "product", PredicateKey: "informs",
			PredicateVersion: 7, ObjectEntityID: "entity-c", ObjectName: "C", ObjectKind: "concept",
			Evidence: []repository.DreamEvidence{{SupportID: "support-b-1", Content: "B informs C.", Authority: "secondary"}},
		},
	}
}

func testDreamPathPredicates() []repository.DreamTargetPredicate {
	return []repository.DreamTargetPredicate{
		{
			PredicateKey:        "depends_on",
			Version:             2,
			AllowedSubjectKinds: []string{"project"},
			AllowedObjectKinds:  []string{"concept"},
		},
		{
			PredicateKey:        "enables",
			Version:             3,
			AllowedSubjectKinds: []string{"project"},
			AllowedObjectKinds:  []string{"concept"},
		},
	}
}
