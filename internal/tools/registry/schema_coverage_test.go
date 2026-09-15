package registry

import (
	"errors"
	"testing"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestContractSchemaBuildersRemainConstructible(t *testing.T) {
	builders := []func() map[string]any{
		entityHandleSchema,
		valueHandleSchema,
		semanticObjectSchema,
		recallDiscoveryPathSchema,
		recallRelationshipSchema,
		recallConflictSchema,
		recallRelationshipConflictSchema,
		recallEvidenceConflictSchema,
		recallEvidenceConflictPositionSchema,
		recallConflictPositionSchema,
		recallConflictSupporterSchema,
		hypothesisSummarySchema,
		hypothesisSchema,
		dreamDerivationSchema,
		dreamEvidenceDerivationSchema,
		versionMapSchema,
		correctionRelationshipChangeSchema,
		correctionEntityCandidateSchema,
		crossProfileReferenceSchema,
		traceRelationshipSchema,
		traceObservationSchema,
		traceEvidenceSupportSchema,
		traceSupportDecisionSchema,
		traceEvidenceSchema,
		traceEvidenceLifecycleEventSchema,
		traceVerificationSchema,
		traceTransitionSchema,
		traceConflictSchema,
		traceIdentityCorrectionSchema,
		semanticNodeSchema,
		semanticEdgeSchema,
		recallFeedbackOutputSchema,
		listDreamsOutputSchema,
		getDreamOutputSchema,
		resolveDreamFeedbackOutputSchema,
		dreamConfirmationBusyOutputSchema,
		TerminalRememberOutputSchema,
		exportMemoryPackOutputSchema,
		memoryPackOmissionSchema,
		countMapSchema,
		sHA256Schema,
	}
	for index, build := range builders {
		if schema := build(); len(schema) == 0 {
			t.Fatalf("schema builder %d returned an empty schema", index)
		}
	}
	for _, version := range []string{domain.PreviousContractVersion, domain.ContractVersion} {
		if len(terminalCorrectionOutputSchema(version)) == 0 || len(terminalErrorSchema()) == 0 || len(dreamTerminalRememberOutputSchema(version)) == 0 {
			t.Fatalf("terminal schemas were empty for version %q", version)
		}
	}
	if len(uuidStringSchema("id")) == 0 || len(nullableDateTime("time")) == 0 || len(nullableNumber("number", 0, 1)) == 0 || len(boundedMap("metadata")) == 0 {
		t.Fatal("primitive schema builders returned empty schemas")
	}
	if len(recallContractOutput(nil)) == 0 {
		t.Fatal("nil recall output was empty")
	}
	if output, err := traceContractOutput(nil); err != nil || len(output) == 0 {
		t.Fatalf("nil trace output = %#v, %v", output, err)
	}
	if len(listDreamsContractOutput(nil, "")) == 0 || len(resolveDreamFeedbackContractOutput(nil)) == 0 || len(exportMemoryPackContractOutput(nil)) == 0 {
		t.Fatal("nil auxiliary output was empty")
	}
	if len(dreamSummaryContractOutput(nil)) != 0 || len(dreamContractOutput(nil)) != 0 || len(dreamDerivationsContractOutput(nil)) != 0 || len(dreamEvidenceDerivationsContractOutput(nil)) != 0 {
		t.Fatal("nil dream output was not empty")
	}
	if len(memoryPackOmissionsContractOutput([]string{"", "omitted"})) != 1 {
		t.Fatal("memory pack omissions did not filter blanks")
	}
	if _, ok := resolveDreamConfirmationBusyOutcome(errors.New("not busy")); ok {
		t.Fatal("ordinary error was classified as dream busy")
	}
}
