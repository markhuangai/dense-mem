package registry

func bindContractTool(tool Tool, deps Dependencies) Tool {
	tool = bindRememberTool(tool, deps)
	tool = bindLifecycleTool(tool, deps)
	tool = bindRecallTool(tool, deps)
	tool = bindRecallFeedbackTool(tool, deps)
	tool = bindTraceTool(tool, deps)
	tool = bindDreamTool(tool, deps)
	return bindMemoryPackTool(tool, deps)
}
