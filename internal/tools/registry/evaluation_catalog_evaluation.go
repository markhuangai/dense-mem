//go:build evaluation

package registry

func evaluationTools(deps Dependencies) []Tool {
	return []Tool{
		evalListKnowledgeRefsTool(deps),
		evalRunDreamCycleTool(deps),
		evalRunRecallCaseTool(deps),
	}
}
