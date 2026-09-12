//go:build evaluation

package registry

func evaluationTools(deps Dependencies) []Tool {
	deps = bindEvaluationApplication(deps)
	return []Tool{
		evalListKnowledgeRefsTool(deps),
		evalRunDreamCycleTool(deps),
		evalRunRecallCaseTool(deps),
	}
}
