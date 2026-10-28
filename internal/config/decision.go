package config

import (
	"fmt"
	"net/url"
	"os"
	"strings"
)

type DecisionProviderConfig struct {
	Provider               string
	APIURL                 string
	APIKey                 string `json:"-"`
	Model                  string
	MaxInputTokens         int
	MaxQuestionInputTokens int
	TimeoutSeconds         int
	MaxConcurrency         int
	LongContextEnabled     bool
}

func (c DecisionProviderConfig) Configured() bool {
	return (c.Provider == "openai" || c.Provider == "typesafe") &&
		c.APIURL != "" && c.APIKey != "" && c.Model != "" &&
		c.MaxInputTokens > 0 && c.MaxQuestionInputTokens > 0
}

func loadDecisionProviderConfig() (DecisionProviderConfig, error) {
	c := DecisionProviderConfig{
		Provider: strings.TrimSpace(os.Getenv("AI_DECISION_PROVIDER")),
		APIURL:   strings.TrimRight(strings.TrimSpace(os.Getenv("AI_DECISION_API_URL")), "/"),
		APIKey:   strings.TrimSpace(os.Getenv("AI_DECISION_API_KEY")),
		Model:    strings.TrimSpace(os.Getenv("AI_DECISION_MODEL")),
	}
	if c.Provider == "" {
		c.Provider = "disabled"
	}
	if c.Provider == "disabled" {
		return c, nil
	}
	var err error
	for _, field := range []struct {
		name     string
		fallback int
		target   *int
	}{
		{"AI_DECISION_MAX_INPUT_TOKENS", 0, &c.MaxInputTokens},
		{"AI_DECISION_MAX_QUESTION_INPUT_TOKENS", 0, &c.MaxQuestionInputTokens},
		{"AI_DECISION_TIMEOUT_SECONDS", 10, &c.TimeoutSeconds},
		{"AI_DECISION_MAX_CONCURRENCY", 4, &c.MaxConcurrency},
	} {
		*field.target, err = parseIntOrDefault(field.name, field.fallback)
		if err != nil {
			return c, err
		}
	}
	if c.Provider == "openai" && strings.TrimSpace(os.Getenv("AI_DECISION_MAX_QUESTION_INPUT_TOKENS")) == "" {
		c.MaxQuestionInputTokens = c.MaxInputTokens
	}
	c.LongContextEnabled, err = parseBoolOrDefault("AI_DECISION_LONG_CONTEXT_ENABLED", false)
	if err != nil {
		return c, err
	}
	return c, c.Validate()
}

func (c DecisionProviderConfig) Validate() error {
	if c.Provider != "openai" && c.Provider != "typesafe" {
		return &ValidationError{Field: "AI_DECISION_PROVIDER", Message: "must be disabled, openai, or typesafe"}
	}
	for _, field := range []struct{ name, value string }{
		{"AI_DECISION_API_URL", c.APIURL}, {"AI_DECISION_API_KEY", c.APIKey}, {"AI_DECISION_MODEL", c.Model},
	} {
		if strings.TrimSpace(field.value) == "" {
			return &ValidationError{Field: field.name, Message: "required for an enabled decision provider"}
		}
	}
	endpoint, err := url.Parse(c.APIURL)
	if err != nil || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return &ValidationError{Field: "AI_DECISION_API_URL", Message: "must be an HTTP(S) base URL without credentials, query, or fragment"}
	}
	for _, field := range []struct {
		name  string
		value int
	}{
		{"AI_DECISION_MAX_INPUT_TOKENS", c.MaxInputTokens}, {"AI_DECISION_MAX_QUESTION_INPUT_TOKENS", c.MaxQuestionInputTokens},
		{"AI_DECISION_TIMEOUT_SECONDS", c.TimeoutSeconds}, {"AI_DECISION_MAX_CONCURRENCY", c.MaxConcurrency},
	} {
		if field.value <= 0 {
			return &ValidationError{Field: field.name, Message: "must be greater than zero"}
		}
	}
	if c.MaxQuestionInputTokens > c.MaxInputTokens {
		return &ValidationError{Field: "AI_DECISION_MAX_QUESTION_INPUT_TOKENS", Message: "must not exceed AI_DECISION_MAX_INPUT_TOKENS"}
	}
	if c.TimeoutSeconds > 86400 || c.MaxConcurrency > 128 {
		return &ValidationError{Field: "AI_DECISION_PROVIDER", Message: "timeout must not exceed 86400 seconds and concurrency must not exceed 128"}
	}
	if c.LongContextEnabled && c.Provider != "typesafe" {
		return &ValidationError{Field: "AI_DECISION_LONG_CONTEXT_ENABLED", Message: "is supported only by typesafe"}
	}
	if c.Provider == "typesafe" {
		requestLimit, questionLimit := 64000, 32000
		if c.LongContextEnabled {
			requestLimit, questionLimit = 262000, 262000
		}
		if c.MaxInputTokens > requestLimit || c.MaxQuestionInputTokens > questionLimit {
			return &ValidationError{Field: "AI_DECISION_MAX_INPUT_TOKENS", Message: fmt.Sprintf("configured TypeSafe limits must not exceed %d per request and %d per question", requestLimit, questionLimit)}
		}
	}
	return nil
}
