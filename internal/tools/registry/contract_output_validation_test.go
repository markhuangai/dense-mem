package registry

import (
	"strings"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service/contextservice"
	"github.com/markhuangai/dense-mem/internal/service/dreamservice"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
	"github.com/markhuangai/dense-mem/internal/service/skillpackservice"
)

func TestTraceContractOutputMapsCompletePublicLineage(t *testing.T) {
	now := time.Date(2026, 7, 19, 21, 30, 0, 0, time.UTC)
	validFrom := now.Add(-time.Hour)
	validTo := now.Add(time.Hour)
	confidence := 0.91

	out, err := traceContractOutput(&contextservice.SemanticTrace{
		Relationship: &repository.RelationshipTraceRecord{
			RelationshipID:   "rel-1",
			OwnerProfileID:   "owner-1",
			SubjectEntityID:  "entity-subj",
			SubjectName:      "Dense-Mem",
			PredicateKey:     "uses",
			PredicateVersion: 1,
			ObjectEntityID:   "entity-obj",
			Polarity:         "+",
			Status:           "active",
			Version:          2,
			ValidFrom:        &validFrom,
			ValidTo:          &validTo,
			CreatedAt:        now,
			UpdatedAt:        now,
		},
		Observations: []repository.RelationshipObservationRecord{{
			ObservationID:     "obs-1",
			RelationshipID:    "rel-1",
			IngestID:          "ingest-1",
			PlacementItemID:   "item-1",
			SubjectRef:        "subject-ref",
			OriginalPredicate: "uses",
			ObjectRef:         "object-ref",
			Polarity:          "+",
			CreatedAt:         now,
		}},
		EvidenceSupports: []repository.RelationshipEvidenceSupportRecord{{
			SupportID:           "support-1",
			RelationshipID:      "rel-1",
			ObservationID:       "obs-1",
			VerificationEventID: "verify-1",
			FragmentID:          "fragment-1",
			SourceGroupKey:      "source-group",
			SpanStart:           0,
			SpanEnd:             9,
			Quote:               "Dense-Mem",
			Authority:           "primary",
			CreatedAt:           now,
		}},
		EvidenceSupportDecisionEvents: []repository.RelationshipSupportDecisionEvent{{
			SupportDecisionID: "support-decision-1",
			SupportID:         "support-1",
			RelationshipID:    "rel-1",
			ActorProfileID:    "owner-1",
			Decision:          "grant",
			Reason:            "matches evidence",
			CreatedAt:         now,
		}},
		Evidence: []repository.TraceEvidenceFragment{{
			FragmentID:       "fragment-1",
			IngestID:         "ingest-1",
			EvidenceIndex:    0,
			Content:          "Dense-Mem uses PostgreSQL.",
			ContentHash:      "sha256:fragment",
			ContentTruncated: true,
			SourceType:       "conversation",
			SourceRef:        "thread-1",
			CreatedAt:        now,
		}},
		EvidenceLifecycleEvents: []repository.TraceEvidenceLifecycleEvent{{
			LifecycleEventID:      "lifecycle-event-1",
			LifecycleOperationID:  "lifecycle-operation-1",
			TargetFragmentID:      "fragment-1",
			ReplacementFragmentID: "fragment-2",
			Action:                "supersede",
			Reason:                "replacement evidence is more precise",
			CreatedAt:             now,
		}},
		VerificationEvents: []repository.RelationshipVerificationEvent{{
			VerificationEventID: "verify-1",
			ObservationID:       "obs-1",
			EvidenceVerdict:     "entailed",
			Confidence:          &confidence,
			Rationale:           "direct support",
			CreatedAt:           now,
		}},
		Transitions: []repository.RelationshipTransitionEvent{{
			TransitionID:   "transition-1",
			RelationshipID: "rel-1",
			FromStatus:     "active",
			ToStatus:       "active",
			Reason:         "promoted",
			CreatedAt:      now,
		}},
		CrossProfileReferences: []repository.RelationshipCrossReferenceRecord{{
			CrossReferenceID:     "xref-1",
			SourceRelationshipID: "rel-1",
			TargetRelationshipID: "rel-2",
		}},
		IdentityCorrections: []repository.EntityCorrectionEventRecord{{
			CorrectionEventID:      "correction-1",
			Action:                 "merge",
			SurvivorEntityID:       "entity-subj",
			NewEntityID:            "entity-old",
			SelectedObservationIDs: []string{"obs-1"},
			Reason:                 "same project",
			CreatedAt:              now,
		}},
		SupersessionLineage: []repository.RelationshipTraceRecord{{
			RelationshipID: "rel-0",
			ObjectValueID:  "value-old",
			PredicateKey:   "uses",
			Status:         "superseded",
			CreatedAt:      now,
			UpdatedAt:      now,
		}},
		SemanticNodes: []repository.SemanticGraphNode{{
			ID:    "entity-subj",
			Type:  "entity",
			Title: "Dense-Mem",
		}, {
			ID:    "value-1",
			Type:  "value",
			Title: "PostgreSQL",
		}},
		SemanticEdges: []repository.SemanticGraphEdge{{
			RelationshipID: "rel-1",
			Source:         "entity:entity-subj",
			Target:         "value:value-1",
			Relationship:   "uses",
		}},
		VisitedEntityIDs: []string{"entity-subj"},
		StoppedReason:    "max_edges",
	})
	if err != nil {
		t.Fatalf("traceContractOutput returned error: %v", err)
	}
	if err := ValidateInput(Tool{InputSchema: traceMemoryOutputSchema()}, out); err != nil {
		t.Fatalf("trace output schema validation failed: %v; output=%#v", err, out)
	}

	relationship := out["relationship"].(map[string]any)
	if relationship["relationship_id"] != "rel-1" || relationship["relationship_status"] != "active" {
		t.Fatalf("relationship output = %#v", relationship)
	}
	if _, exists := relationship["team_id"]; exists {
		t.Fatalf("relationship leaked internal team_id: %#v", relationship)
	}
	edges := out["semantic_edges"].([]map[string]any)
	if edges[0]["source_id"] != "entity-subj" || edges[0]["target_id"] != "value-1" {
		t.Fatalf("semantic edge ids = %#v", edges[0])
	}
	if out["stopped_reason"] != "max_edges" {
		t.Fatalf("stopped_reason = %#v", out["stopped_reason"])
	}
	lifecycleEvents := out["evidence_lifecycle_events"].([]map[string]any)
	if len(lifecycleEvents) != 1 || lifecycleEvents[0]["target_evidence_id"] != "fragment-1" || lifecycleEvents[0]["replacement_evidence_id"] != "fragment-2" {
		t.Fatalf("evidence lifecycle events = %#v", lifecycleEvents)
	}
}

func TestAuxiliaryContractOutputsMapPublicShapes(t *testing.T) {
	now := time.Date(2026, 7, 19, 22, 15, 0, 0, time.UTC)
	hash := strings.Repeat("a", 64)
	dream := &domain.Dream{
		DreamID:         "dream-1",
		TeamID:          "team-1",
		Hypothesis:      "Dense-Mem may add cutover telemetry.",
		Rationale:       "related implementation signals",
		Likelihood:      0.62,
		Confidence:      0.78,
		SubjectEntityID: "entity-dense-mem",
		PredicateKey:    "may_add",
		ObjectValueID:   "value-cutover-telemetry",
		SourceRefs: []domain.DreamSourceRef{
			{Type: "relationship", ID: "rel-1"},
			{Type: "candidate_relationship", ID: "candidate-1"},
		},
		SourceOwnerProfileIDs: []string{"owner-1"},
		SourceVersions:        map[string]int{"rel-1": 2},
		Derivations: []domain.DreamDerivation{{
			PremisePosition:     1,
			RelationshipID:      "rel-1",
			RelationshipVersion: 2,
			SourceGroupKey:      "source-group-1",
			Quote:               "Dense-Mem may add cutover telemetry.",
			Authority:           "primary",
		}},
		GeneratorKind:    "provider",
		GeneratorVersion: "gpt-4.1-mini",
		Status:           domain.DreamStatusProposed,
		CreatedAt:        now,
	}

	listDreams := listDreamsContractOutput([]*domain.Dream{dream}, "next-dream")
	if err := ValidateInput(Tool{InputSchema: listDreamsOutputSchema()}, listDreams); err != nil {
		t.Fatalf("list dreams output validation failed: %v; output=%#v", err, listDreams)
	}
	dreamOut := dreamContractOutput(dream)
	if err := ValidateInput(Tool{InputSchema: getDreamOutputSchema()}, map[string]any{"hypothesis": dreamOut}); err != nil {
		t.Fatalf("dream output validation failed: %v; output=%#v", err, dreamOut)
	}
	if dreamOut["generator_kind"] != "provider" || dreamOut["source_owner_profile_ids"].([]string)[0] != "owner-1" {
		t.Fatalf("dream output = %#v", dreamOut)
	}
	derivations := dreamOut["derivations"].([]map[string]any)
	if len(derivations) != 1 || derivations[0]["relationship_id"] != "rel-1" {
		t.Fatalf("dream derivations = %#v", derivations)
	}
	feedback := resolveDreamFeedbackContractOutput(&dreamservice.ResolveFeedbackResult{
		Dream:  dream,
		Memory: &memoryservice.RememberResult{SubmissionID: "submission-canonical"},
	})
	if err := ValidateInput(Tool{InputSchema: resolveDreamFeedbackOutputSchema()}, feedback); err != nil {
		t.Fatalf("dream feedback output validation failed: %v; output=%#v", err, feedback)
	}

	candidates := findMemoryPackCandidatesContractOutput(&skillpackservice.FindCandidatesResult{
		Candidates: []skillpackservice.MemoryPackCandidate{{
			RelationshipID:  "rel-1",
			SubjectEntityID: "entity-dense-mem",
			Subject:         "Dense-Mem",
			PredicateKey:    "uses",
			ObjectEntityID:  "entity-postgres",
			Object:          "PostgreSQL",
		}, {
			RelationshipID:  "rel-2",
			SubjectEntityID: "entity-dense-mem",
			Subject:         "Dense-Mem",
			PredicateKey:    "released_on",
			ObjectValueID:   "value-date",
			ObjectValueType: "date",
			Object:          "2026-07-19",
			Polarity:        "-",
		}},
	})
	if err := ValidateInput(Tool{InputSchema: findMemoryPackCandidatesOutputSchema()}, candidates); err != nil {
		t.Fatalf("memory pack candidates validation failed: %v; output=%#v", err, candidates)
	}
	if candidates["candidates"].([]map[string]any)[1]["rank"] != 2 {
		t.Fatalf("candidate ranks = %#v", candidates)
	}

	exported := exportMemoryPackContractOutput(&skillpackservice.ExportResult{
		CanonicalJSON: `{"format":"` + skillpackservice.MemoryPackFormat + `"}`,
		SHA256:        hash,
		Filename:      "dense-mem-pack.json",
		ItemCount:     1,
		Artifact: skillpackservice.MemoryPackArtifact{
			Evidence:         []skillpackservice.MemoryPackEvidence{{EvidenceID: "evidence-1"}},
			EvidenceSupports: []skillpackservice.MemoryPackEvidenceSupport{{EvidenceID: "evidence-1"}},
		},
		Omissions: []string{"support omitted"},
	})
	if err := ValidateInput(Tool{InputSchema: exportMemoryPackOutputSchema()}, exported); err != nil {
		t.Fatalf("memory pack export validation failed: %v; output=%#v", err, exported)
	}

	inspected := inspectMemoryPackContractOutput(&skillpackservice.InspectResult{
		ArtifactHash:   hash,
		Format:         skillpackservice.MemoryPackFormat,
		ItemCount:      2,
		SelectedCount:  1,
		SupportSummary: skillpackservice.SupportSummary{EvidenceCount: 1, SupportCount: 1},
		Items: []skillpackservice.InspectItem{
			{ItemID: "item-ready", Status: "ready"},
			{ItemID: "item-review", Status: "needs_review"},
		},
		DecisionsRequired: []skillpackservice.ConflictPrompt{{
			ItemID:         "item-review",
			Reason:         "predicate_conflict",
			AllowedActions: []string{"skip", "apply"},
		}},
	}, "trusted")
	if err := ValidateInput(Tool{InputSchema: inspectMemoryPackOutputSchema()}, inspected); err != nil {
		t.Fatalf("memory pack inspect validation failed: %v; output=%#v", err, inspected)
	}
	if inspected["expected_outcomes"].(map[string]any)["review"] != 1 {
		t.Fatalf("inspect outcomes = %#v", inspected["expected_outcomes"])
	}

	imported := importMemoryPackContractOutput(&skillpackservice.ImportResult{
		ImportID:     "import-1",
		Status:       domain.SkillPackImportStatusSubmitted,
		SubmissionID: "submission-1",
		Items: []skillpackservice.ImportItemResult{
			{ItemID: "item-skipped", Status: "skipped", Decision: "skip"},
			{ItemID: "item-failed", Status: "failed", Error: "provider unavailable"},
		},
	})
	if err := ValidateInput(Tool{InputSchema: importMemoryPackOutputSchema()}, imported); err != nil {
		t.Fatalf("memory pack import validation failed: %v; output=%#v", err, imported)
	}
	if state := memoryPackProcessingState(&skillpackservice.ImportResult{Status: domain.SkillPackImportStatusInspecting}); state != "processing" {
		t.Fatalf("processing state = %q", state)
	}
	if state := memoryPackProcessingState(&skillpackservice.ImportResult{Status: domain.SkillPackImportStatusApplied}); state != "completed" {
		t.Fatalf("completed state = %q", state)
	}
	if state := memoryPackProcessingState(&skillpackservice.ImportResult{Status: "change_ledger_failed"}); state != "failed" {
		t.Fatalf("failed state = %q", state)
	}

	rolledBack := rollbackMemoryPackContractOutput(&skillpackservice.RollbackResult{
		ImportID:                "import-1",
		Status:                  domain.SkillPackImportStatusRolledBack,
		DryRun:                  false,
		Conflicts:               []string{"active descendants"},
		ImpactToken:             "impact-token",
		AffectedRelationshipIDs: []string{"rel-1"},
	})
	if err := ValidateInput(Tool{InputSchema: rollbackMemoryPackImportOutputSchema()}, rolledBack); err != nil {
		t.Fatalf("memory pack rollback validation failed: %v; output=%#v", err, rolledBack)
	}
	if rolledBack["applied"] != true || rolledBack["safe"] != false {
		t.Fatalf("rollback output = %#v", rolledBack)
	}
}

func TestContractOutputEmptyCollectionResultsRemainSchemaValid(t *testing.T) {
	cases := []struct {
		name   string
		schema map[string]any
		out    map[string]any
	}{
		{
			name:   "dream list",
			schema: listDreamsOutputSchema(),
			out:    listDreamsContractOutput(nil, ""),
		},
		{
			name:   "memory pack candidates",
			schema: findMemoryPackCandidatesOutputSchema(),
			out:    findMemoryPackCandidatesContractOutput(nil),
		},
		{
			name:   "memory pack inspect",
			schema: inspectMemoryPackOutputSchema(),
			out:    inspectMemoryPackContractOutput(nil, ""),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateInput(Tool{InputSchema: tc.schema}, tc.out); err != nil {
				t.Fatalf("empty output validation failed: %v; output=%#v", err, tc.out)
			}
		})
	}
}

func TestDreamContractOutputUsesCanonicalFallbackFields(t *testing.T) {
	dream := &domain.Dream{
		DreamID:    "dream-fallback",
		TeamID:     "team-fallback",
		Hypothesis: "Dense-Mem may need an evidence lifecycle guide.",
		Status:     domain.DreamStatusProposed,
		SourceRefs: []domain.DreamSourceRef{
			{Type: "relationship", ID: "relationship-fallback"},
			{Type: "candidate_relationship", ID: "candidate-fallback"},
			{Type: "relationship", ID: ""},
		},
	}

	out := dreamContractOutput(dream)
	if owners := out["source_owner_profile_ids"].([]string); len(owners) != 0 {
		t.Fatalf("source owner fallback = %#v", owners)
	}
	if relationships := out["source_relationship_ids"].([]string); len(relationships) != 1 || relationships[0] != "relationship-fallback" {
		t.Fatalf("relationship source fallback = %#v", relationships)
	}
	if candidates := out["source_candidate_relationship_ids"].([]string); len(candidates) != 1 || candidates[0] != "candidate-fallback" {
		t.Fatalf("candidate source fallback = %#v", candidates)
	}
	if versions := out["source_versions"].(map[string]int); len(versions) != 0 {
		t.Fatalf("source versions fallback = %#v", versions)
	}
	if out["generator_kind"] != "deterministic" || out["generator_version"] != "dream-v2" || out["created_at"] != "1970-01-01T00:00:00Z" {
		t.Fatalf("dream fallback output = %#v", out)
	}
}

func TestMemoryPackContractOutputsMapCurrentLifecycleStates(t *testing.T) {
	cases := []struct {
		name string
		res  *skillpackservice.ImportResult
		want string
	}{
		{
			name: "failed",
			res: &skillpackservice.ImportResult{
				ImportID: "import-failed",
				Status:   domain.SkillPackImportStatusFailed,
				Items: []skillpackservice.ImportItemResult{
					{ItemID: "item-skipped", Status: "skipped"},
					{ItemID: "item-failed", Status: "failed", Error: "verification failed"},
				},
			},
			want: "failed",
		},
		{
			name: "processing",
			res:  &skillpackservice.ImportResult{ImportID: "import-processing", Status: domain.SkillPackImportStatusInspecting},
			want: "processing",
		},
		{
			name: "completed without staged evidence",
			res:  &skillpackservice.ImportResult{ImportID: "import-completed", Status: domain.SkillPackImportStatusApplied},
			want: "completed",
		},
		{
			name: "queued with staged submission",
			res:  &skillpackservice.ImportResult{ImportID: "import-queued", Status: domain.SkillPackImportStatusSubmitted, SubmissionID: "submission-queued"},
			want: "queued",
		},
		{
			name: "unknown historical status remains completed",
			res:  &skillpackservice.ImportResult{ImportID: "import-unknown", Status: "unknown_historical_status"},
			want: "completed",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := importMemoryPackContractOutput(tc.res)
			if err := ValidateInput(Tool{InputSchema: importMemoryPackOutputSchema()}, out); err != nil {
				t.Fatalf("import output validation failed: %v; output=%#v", err, out)
			}
			if out["processing_state"] != tc.want {
				t.Fatalf("processing_state = %#v, want %q", out["processing_state"], tc.want)
			}
		})
	}

	export := exportMemoryPackContractOutput(&skillpackservice.ExportResult{
		SHA256:    strings.Repeat("a", 64),
		Omissions: []string{"", "support was intentionally omitted"},
	})
	if omissions := export["omissions"].([]map[string]any); len(omissions) != 1 || omissions[0]["reason"] != "support was intentionally omitted" {
		t.Fatalf("export omissions = %#v", omissions)
	}

	rollback := rollbackMemoryPackContractOutput(&skillpackservice.RollbackResult{
		ImportID:                "import-rollback",
		Status:                  domain.SkillPackImportStatusRolledBack,
		Conflicts:               []string{"relationship changed"},
		ImpactToken:             "rollback-token",
		AffectedRelationshipIDs: []string{"relationship-1"},
	})
	if err := ValidateInput(Tool{InputSchema: rollbackMemoryPackImportOutputSchema()}, rollback); err != nil {
		t.Fatalf("rollback output validation failed: %v; output=%#v", err, rollback)
	}
	if rollback["safe"] != false || rollback["applied"] != true || rollback["impact_token"] != "rollback-token" {
		t.Fatalf("rollback output = %#v", rollback)
	}
}

func TestContractValidationEdgeBranches(t *testing.T) {
	if err := validateDecisionFields(map[string]any{"action": "resolve"}, "relationship_id"); err == nil {
		t.Fatal("missing decision was accepted")
	}
	if err := validateDecisionFields(map[string]any{
		"action":   "resolve",
		"decision": map[string]any{"relationship_id": " "},
	}, "relationship_id"); err == nil {
		t.Fatal("empty required decision field was accepted")
	}
	if err := validateDecisionFields(map[string]any{
		"action":   "resolve",
		"decision": map[string]any{"relationship_id": "rel-1"},
	}, "relationship_id"); err != nil {
		t.Fatalf("valid decision fields rejected: %v", err)
	}

	correction := map[string]any{
		"owned_observation_ids": []any{"obs-1"},
		"operation":             "merge",
		"target_entity_id":      "entity-survivor",
		"dry_run":               false,
		"impact_token":          "impact-token",
	}
	if err := validateCorrectEntityResolution(correction); err != nil {
		t.Fatalf("valid correction rejected: %v", err)
	}
	if err := validateCorrectEntityResolution(map[string]any{"owned_observation_ids": []any{}}); err == nil {
		t.Fatal("empty owned_observation_ids accepted")
	}
	if err := validateCorrectEntityResolution(map[string]any{
		"owned_observation_ids": []any{"obs-1"},
		"operation":             "merge",
		"dry_run":               true,
	}); err == nil {
		t.Fatal("merge without target accepted")
	}
	if err := validateCorrectEntityResolution(map[string]any{
		"owned_observation_ids": []any{"obs-1"},
		"operation":             "split",
		"target_entity_id":      "entity-target",
		"dry_run":               true,
	}); err == nil {
		t.Fatal("split with target accepted")
	}
	if err := validateCorrectEntityResolution(map[string]any{
		"owned_observation_ids": []any{"obs-1"},
		"operation":             "merge",
		"target_entity_id":      "entity-survivor",
		"dry_run":               false,
	}); err == nil {
		t.Fatal("apply without impact_token accepted")
	}

	hash := strings.Repeat("a", 64)
	if err := validateMemoryPackSource(map[string]any{"url": "https://example.com/pack.json", "expected_sha256": hash}); err != nil {
		t.Fatalf("valid memory pack url rejected: %v", err)
	}
	for name, args := range map[string]map[string]any{
		"neither":      {},
		"both":         {"artifact_json": "{}", "url": "https://example.com/pack.json"},
		"http":         {"url": "http://example.com/pack.json", "expected_sha256": hash},
		"missing_hash": {"url": "https://example.com/pack.json"},
		"bad_hash":     {"artifact_json": "{}", "expected_sha256": "not-hex"},
	} {
		if err := validateMemoryPackSource(args); err == nil {
			t.Fatalf("%s memory pack source accepted", name)
		}
	}
	if err := validateRollbackMemoryPackImport(map[string]any{"dry_run": true}); err != nil {
		t.Fatalf("dry-run rollback rejected: %v", err)
	}
	if err := validateRollbackMemoryPackImport(map[string]any{"dry_run": false}); err == nil {
		t.Fatal("rollback apply without impact_token accepted")
	}

	evidence := []any{map[string]any{"content": "Dense-Mem uses PostgreSQL."}}
	proposal := validSpanGroundedProposal()
	if err := validateProposalReferencesAndSpans(proposal, evidence); err != nil {
		t.Fatalf("valid proposal rejected: %v", err)
	}
	badProposal := validSpanGroundedProposal()
	badProposal["relationships"].([]any)[0].(map[string]any)["subject_ref"] = "missing"
	if err := validateProposalReferencesAndSpans(badProposal, evidence); err == nil {
		t.Fatal("unknown subject ref accepted")
	}
	if err := validateTypedValue(map[string]any{"type": "boolean", "value": "true"}, "value"); err == nil {
		t.Fatal("boolean typed value accepted string")
	}
	if err := validateTypedValue(map[string]any{"type": "date", "value": 20260719}, "value"); err == nil {
		t.Fatal("date typed value accepted number")
	}
}
