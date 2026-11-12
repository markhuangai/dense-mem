package registry

import (
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/memorypack"
	"github.com/markhuangai/dense-mem/internal/recall"
	recallcontract "github.com/markhuangai/dense-mem/internal/recall/contract"
	rememberapp "github.com/markhuangai/dense-mem/internal/remember/service"
	tracecontract "github.com/markhuangai/dense-mem/internal/trace/contract"
	"strings"
)

func rememberRequestFromContractInput(input map[string]any) (rememberapp.RememberRequest, error) {
	var req rememberapp.RememberRequest
	if err := remapInput(input, &req); err != nil {
		return req, err
	}
	return req, nil
}

func exportMemoryPackContractOutput(res *memorypack.ExportResult) map[string]any {
	if res == nil {
		return map[string]any{
			"artifact_json":  "",
			"content_sha256": strings.Repeat("0", 64),
			"filename":       "",
			"counts":         map[string]any{},
			"omissions":      []any{},
		}
	}
	return map[string]any{
		"artifact_json":  res.CanonicalJSON,
		"content_sha256": res.SHA256,
		"filename":       res.Filename,
		"counts": map[string]any{

			"evidence": len(res.Artifact.Evidence),
		},
		"omissions": memoryPackOmissionsContractOutput(res.Omissions),
	}
}

func memoryPackOmissionsContractOutput(omissions []string) []map[string]any {
	out := make([]map[string]any, 0, len(omissions))
	indices := make(map[string]int)
	for _, omission := range omissions {
		if strings.TrimSpace(omission) == "" {
			continue
		}
		id, reason, scoped := strings.Cut(omission, ":")
		if !scoped {
			id, reason = "artifact", omission
		}
		if index, exists := indices[id]; exists {
			out[index]["reason"] = out[index]["reason"].(string) + "; " + reason
			continue
		}
		indices[id] = len(out)
		out = append(out, map[string]any{"item_id": id, "reason": reason})
	}
	return out
}

func recallContractOutput(res *recall.RecallResult) map[string]any {
	if res == nil {
		res = &recall.RecallResult{}
	}
	results := make([]map[string]any, 0, len(res.Results))
	for _, item := range res.Results {
		results = append(results, map[string]any{"evidence_id": item.EvidenceID, "equivalent_evidence_ids": append([]string{}, item.EquivalentEvidenceIDs...), "equivalents_truncated": item.EquivalentsTruncated, "context": item.Context, "space_kind": item.SpaceKind})
	}
	if res.RelatedTopics == nil {
		res.RelatedTopics = []recallcontract.RelatedTopic{}
	}
	if res.Conflicts == nil {
		res.Conflicts = []recall.RecallConflictSummary{}
	}
	if res.Degradations == nil {
		res.Degradations = []recall.RecallDegradationResult{}
	}
	if res.SuggestedActions == nil {
		res.SuggestedActions = []recall.RecallSuggestedAction{}
	}
	state := res.SearchStates.Evidence
	if state == "" {
		state = res.SearchState
	}
	if state == "" {
		state = string(domain.SearchProjectionCurrent)
	}
	return map[string]any{"recall_id": res.RecallID, "results": results, "conflicts": res.Conflicts, "related_topics": res.RelatedTopics, "topics_truncated": res.TopicsTruncated, "search_states": map[string]any{"evidence": state}, "degradations": res.Degradations, "suggested_actions": res.SuggestedActions}
}
func traceContractOutput(trace *tracecontract.EvidenceTraceResult) (map[string]any, error) {
	return structToMap(trace)
}
