package verifier

import "github.com/markhuangai/dense-mem/internal/modelprovider"

type openAIVerifierUsage struct {
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
}

type openAIStructuredChatResult struct {
	Content       string
	Usage         *openAIVerifierUsage
	ReportedUsage *openAIVerifierUsage
	Diagnostics   *modelprovider.CallDiagnostics
}
