package evalharness

import (
	"context"
	"errors"
	"net/http"

	"github.com/markhuangai/dense-mem/internal/modelprovider"
)

type AnswerTransport struct {
	URL     string
	Key     string
	Client  *http.Client
	Capture *Capture
}

type incompleteAnswer struct{ reason string }

func (e *incompleteAnswer) Error() string { return e.reason }

func (transport AnswerTransport) Complete(ctx context.Context, request modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
	messages := []map[string]string{}
	for _, message := range request.Messages {
		messages = append(messages, map[string]string{"role": message.Role, "content": message.Content})
	}
	body := map[string]any{"model": request.Model, "messages": messages, "max_completion_tokens": request.MaxOutputTokens, "response_format": map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": request.SchemaName, "strict": true, "schema": request.Schema}}}
	var response struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage *struct {
			Input  *int `json:"prompt_tokens"`
			Output *int `json:"completion_tokens"`
			Total  *int `json:"total_tokens"`
		} `json:"usage"`
	}
	client := HTTPClient{Client: transport.Client, ExchangeCapture: transport.Capture}
	if err := client.doJSON(ctx, http.MethodPost, endpoint(transport.URL, "/chat/completions"), transport.Key, body, &response); err != nil {
		if errors.Is(err, errMalformedHTTPJSON) {
			return modelprovider.StructuredResult{}, &incompleteAnswer{reason: "answer provider returned malformed JSON"}
		}
		return modelprovider.StructuredResult{}, err
	}
	result := modelprovider.StructuredResult{}
	if response.Usage != nil {
		result.UsageAvailable = true
		result.PromptTokens, result.CompletionTokens, result.TotalTokens = -1, -1, -1
		if response.Usage.Input != nil {
			result.PromptTokens = *response.Usage.Input
		}
		if response.Usage.Output != nil {
			result.CompletionTokens = *response.Usage.Output
		}
		if response.Usage.Total != nil {
			result.TotalTokens = *response.Usage.Total
		}
	}
	if len(response.Choices) != 1 {
		return result, &incompleteAnswer{reason: "answer provider must return one complete choice"}
	}
	result.Content = response.Choices[0].Message.Content
	if response.Choices[0].FinishReason != "stop" {
		return result, &incompleteAnswer{reason: "answer provider did not finish a complete response"}
	}
	return result, nil
}
