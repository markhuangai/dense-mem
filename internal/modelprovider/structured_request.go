package modelprovider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tiktoken-go/tokenizer"
)

type jsonSchema struct {
	Name   string          `json:"name"`
	Strict bool            `json:"strict"`
	Schema json.RawMessage `json:"schema"`
}

type responseFormat struct {
	Type       string     `json:"type"`
	JSONSchema jsonSchema `json:"json_schema"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model          string         `json:"model"`
	Messages       []chatMessage  `json:"messages"`
	Temperature    *float64       `json:"temperature,omitempty"`
	ResponseFormat responseFormat `json:"response_format"`
}

func StructuredTemperature(disabled bool) *float64 {
	if disabled {
		return nil
	}
	value := 0.0
	return &value
}

// MarshalStructuredRequest serializes the provider envelope used both for
// sizing and for dispatch.
func MarshalStructuredRequest(request StructuredRequest) ([]byte, error) {
	schema, err := json.Marshal(request.Schema)
	if err != nil {
		return nil, err
	}
	var messages []chatMessage
	for _, message := range request.Messages {
		messages = append(messages, chatMessage(message))
	}
	return json.Marshal(chatRequest{
		Model: request.Model, Messages: messages, Temperature: request.Temperature,
		ResponseFormat: responseFormat{Type: "json_schema", JSONSchema: jsonSchema{Name: request.SchemaName, Strict: true, Schema: schema}},
	})
}

func CountStructuredRequestTokens(request StructuredRequest, tokenizerName string) (int, error) {
	body, err := MarshalStructuredRequest(request)
	if err != nil {
		return 0, err
	}
	return CountTokens(string(body), tokenizerName)
}

func CountTokens(text string, tokenizerName string) (int, error) {
	codec, err := tokenizer.Get(tokenizer.Encoding(strings.TrimSpace(tokenizerName)))
	if err != nil {
		return 0, fmt.Errorf("tokenizer %q: %w", tokenizerName, err)
	}
	count, err := codec.Count(text)
	if err != nil {
		return 0, fmt.Errorf("count tokens with %q: %w", tokenizerName, err)
	}
	return count, nil
}
