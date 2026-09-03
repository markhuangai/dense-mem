package registry

import (
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service/contextservice"
)

func TestTraceContractOutputPreservesPublicSubmissionAndLineageIDs(t *testing.T) {
	now := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	validFrom := now.Add(-time.Hour)
	validTo := now.Add(time.Hour)
	confidence := 0.9
	trace, err := traceContractOutput(&contextservice.SemanticTrace{
		Relationship: &repository.RelationshipTraceRecord{
			RelationshipID: "relationship-1", OwnerProfileID: "profile-1", SubjectEntityID: "entity-1", SubjectName: "Dense-Mem",
			PredicateKey: "uses", PredicateVersion: 1, ObjectEntityID: "entity-2", Polarity: "+", Status: "active", Version: 2,
			ValidFrom: &validFrom, ValidTo: &validTo, CreatedAt: now, UpdatedAt: now,
		},
		Observations: []repository.RelationshipObservationRecord{{
			ObservationID: "observation-1", IngestID: "submission-1", RelationshipID: "relationship-1",
			SubjectRef: "subject", OriginalPredicate: "uses", ObjectRef: "object", Polarity: "+", CreatedAt: now,
		}},
		EvidenceSupports: []repository.RelationshipEvidenceSupportRecord{{
			SupportID: "support-1", RelationshipID: "relationship-1", ObservationID: "observation-1", FragmentID: "evidence-1", OccurrenceID: "occurrence-1",
			SpanStart: 0, SpanEnd: 4, Quote: "Dense", Authority: "primary", CreatedAt: now,
		}},
		EvidenceSupportDecisionEvents: []repository.RelationshipSupportDecisionEvent{{
			SupportDecisionID: "decision-1", SupportID: "support-1", RelationshipID: "relationship-1", Decision: "accepted", CreatedAt: now,
		}},
		Evidence: []repository.TraceEvidenceFragment{{
			FragmentID: "evidence-1", OccurrenceID: "occurrence-1", IngestID: "submission-1", Content: "Dense-Mem uses PostgreSQL", SourceType: "manual", Authority: "primary", CreatedAt: now,
		}},
		EvidenceLifecycleEvents: []repository.TraceEvidenceLifecycleEvent{{
			LifecycleEventID: "lifecycle-1", TargetFragmentID: "evidence-1", Action: "accepted", CreatedAt: now,
		}},
		VerificationEvents: []repository.RelationshipVerificationEvent{{
			VerificationEventID: "verification-1", ObservationID: "observation-1", EvidenceVerdict: "entailed", Confidence: &confidence, CreatedAt: now,
		}},
		Transitions: []repository.RelationshipTransitionEvent{{
			TransitionID: "transition-1", RelationshipID: "relationship-1", FromStatus: "candidate", ToStatus: "active", CreatedAt: now,
		}},
		IdentityCorrections: []repository.EntityCorrectionEventRecord{{
			CorrectionEventID: "correction-1", Action: "merge", SurvivorEntityID: "entity-1", NewEntityID: "entity-2", SelectedObservationIDs: []string{"observation-1"}, CreatedAt: now,
		}},
		SupersessionLineage: []repository.RelationshipTraceRecord{{RelationshipID: "relationship-0", Status: "superseded"}},
		SemanticNodes:       []repository.SemanticGraphNode{{ID: "entity-1", Type: "entity", Title: "Dense-Mem"}},
		SemanticEdges:       []repository.SemanticGraphEdge{{RelationshipID: "relationship-1", Source: "entity:entity-1", Target: "value:value-1", Relationship: "uses"}},
		VisitedEntityIDs:    []string{"entity-1"},
		StoppedReason:       "bounded",
	})
	if err != nil {
		t.Fatalf("traceContractOutput: %v", err)
	}
	observations, ok := trace["observations"].([]map[string]any)
	if !ok || len(observations) != 1 || observations[0]["submission_id"] != "submission-1" {
		t.Fatalf("observation output = %#v", trace["observations"])
	}
	if _, forbidden := observations[0]["ingest_id"]; forbidden {
		t.Fatal("trace exposed internal ingest_id")
	}
	if _, forbidden := observations[0]["placement_item_id"]; forbidden {
		t.Fatal("trace exposed retired placement_item_id")
	}
	if trace["stopped_reason"] != "bounded" {
		t.Fatalf("stopped reason = %#v", trace["stopped_reason"])
	}
	evidence, ok := trace["evidence"].([]map[string]any)
	if !ok || len(evidence) != 1 || evidence[0]["submission_id"] != "submission-1" {
		t.Fatalf("evidence output = %#v", trace["evidence"])
	}
	if evidence[0]["occurrence_id"] != "occurrence-1" {
		t.Fatalf("evidence occurrence output = %#v", evidence[0])
	}
	supports, ok := trace["evidence_supports"].([]map[string]any)
	if !ok || len(supports) != 1 || supports[0]["occurrence_id"] != "occurrence-1" {
		t.Fatalf("support occurrence output = %#v", trace["evidence_supports"])
	}
	lineage, ok := trace["supersession_lineage"].([]map[string]any)
	if !ok || len(lineage) != 1 || lineage[0]["relationship_id"] != "relationship-0" {
		t.Fatalf("supersession lineage = %#v", trace["supersession_lineage"])
	}
}
