package assessor

import (
	"encoding/json"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	"github.com/stretchr/testify/require"
	"sort"
	"strings"
	"testing"
)

func partitionResponseForTest(req PartitionRequest, all bool) PartitionResponse {
	r := PartitionResponse{RequestID: req.RequestID, Items: make([]PartitionResult, len(req.Items))}
	for i, item := range req.Items {
		offsets := make([]int, 0, len(item.Refs))
		byOffset := map[int]string{}
		for ref, offset := range item.Refs {
			offsets = append(offsets, offset)
			byOffset[offset] = ref
		}
		sort.Ints(offsets)
		ends := []string{byOffset[offsets[len(offsets)-1]]}
		if all {
			ends = nil
			for _, offset := range offsets {
				if offset > 0 {
					ends = append(ends, byOffset[offset])
				}
			}
		}
		r.Items[i] = PartitionResult{ItemID: item.ItemID, EndRefs: ends}
	}
	return r
}

func TestRememberPartitionReconstructsUnicodeAndWhitespace(t *testing.T) {
	for _, content := range []string{"  First meaning.\r\n\r\nSecond meaning!  ", "主張。\n別の主張。", "Cafe\u0301 is open. 🧑‍🔬 Works here!\n", "| Name | Value |\n| A | 1 |\n| B | 2 |\n", strings.Repeat("界", knowledge.MaxRememberItemRunes)} {
		req := PreparePartitionRequest("partition:unicode", []string{content})
		body, err := json.Marshal(partitionResponseForTest(req, true))
		require.NoError(t, err)
		spans, err := ValidatePartitionResponse(req, body)
		require.NoError(t, err)
		var reconstructed strings.Builder
		runes := []rune(content)
		for _, span := range spans[0] {
			reconstructed.WriteString(string(runes[span[0]:span[1]]))
		}
		require.Equal(t, content, reconstructed.String())
	}
}

func TestRememberPartitionRejectsCompleteResponseViolations(t *testing.T) {
	req := PreparePartitionRequest("partition:closed", []string{"First. Second.", "Other."})
	for name, change := range map[string]func(*PartitionResponse){
		"wrong request":       func(r *PartitionResponse) { r.RequestID = "other" },
		"missing item":        func(r *PartitionResponse) { r.Items = r.Items[:1] },
		"duplicate item":      func(r *PartitionResponse) { r.Items[1] = r.Items[0] },
		"wrong item boundary": func(r *PartitionResponse) { r.Items[0].EndRefs = r.Items[1].EndRefs },
		"repeated boundary":   func(r *PartitionResponse) { r.Items[0].EndRefs = append(r.Items[0].EndRefs, r.Items[0].EndRefs[0]) },
		"missing suffix": func(r *PartitionResponse) {
			for ref, offset := range req.Items[0].Refs {
				if offset > 0 && offset < len([]rune(req.Items[0].Content)) {
					r.Items[0].EndRefs = []string{ref}
					break
				}
			}
		},
		"unknown boundary": func(r *PartitionResponse) { r.Items[0].EndRefs = []string{"forged"} },
		"empty item":       func(r *PartitionResponse) { r.Items[0].EndRefs = nil },
	} {
		t.Run(name, func(t *testing.T) {
			r := partitionResponseForTest(req, false)
			change(&r)
			body, _ := json.Marshal(r)
			_, err := ValidatePartitionResponse(req, body)
			require.Error(t, err)
		})
	}
	body, _ := json.Marshal(partitionResponseForTest(req, false))
	for _, invalid := range []string{"not-json", string(body) + " {}", strings.Replace(string(body), "\"items\":", "\"invented\":true,\"items\":", 1)} {
		_, err := ValidatePartitionResponse(req, []byte(invalid))
		require.Error(t, err)
	}
}

func TestRememberPartitionBoundariesAreSparseAndRequestScoped(t *testing.T) {
	content := strings.Repeat("a", knowledge.MaxRememberItemRunes)
	req := PreparePartitionRequest("one", []string{content, content})
	require.Len(t, req.Items[0].Refs, 2)
	for ref := range req.Items[0].Refs {
		require.NotContains(t, req.Items[1].Refs, ref)
		require.NotContains(t, PreparePartitionRequest("two", []string{content}).Items[0].Refs, ref)
	}
	prepared := PrepareSemanticAssessmentEvidence(SemanticReviewEvidence{Content: "A dependent passage keeps its original wording."})
	require.Less(t, len(prepared.BoundaryRefs), len([]rune(prepared.Content)))
	require.Contains(t, prepared.BoundaryText, "dependent")
}

func TestRememberPartitionCountBound(t *testing.T) {
	req := PreparePartitionRequest("count", []string{strings.Repeat("Meaning. ", 101)})
	body, _ := json.Marshal(partitionResponseForTest(req, true))
	_, err := ValidatePartitionResponse(req, body)
	require.ErrorContains(t, err, "partition count")
}

func TestRememberPartitionLongAssessmentFitsRegenerationBudget(t *testing.T) {
	for _, phrase := range []string{"Boreal keeps this supporting context together ", "界😀 words preserve Unicode boundaries "} {
		content := string([]rune(strings.Repeat(phrase, 600))[:knowledge.MaxRememberItemRunes])
		request, issues := PrepareSemanticAssessmentRequest(SemanticAssessmentRequest{
			RequestID: "long-assessment",
			TeamID:    "team",
			Evidence:  []SemanticReviewEvidence{{EvidenceID: "evidence:0", Content: content}},
		}, DefaultSemanticAssessmentLimits())
		require.Empty(t, issues)
		require.LessOrEqual(t, request.InputTokens, semanticAssessmentConversationInputLimit(DefaultSemanticAssessmentLimits()))
		prepared := request.Evidence[0]
		reconstructed := prepared.BoundaryText
		for ref := range prepared.BoundaryRefs {
			reconstructed = strings.ReplaceAll(reconstructed, "⟦"+ref+"⟧", "")
		}
		require.Equal(t, content, reconstructed)
		require.Less(t, len(prepared.BoundaryRefs), 2200)
	}
}
