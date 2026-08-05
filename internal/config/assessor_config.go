package config

import (
	"math"
	"strings"
)

type aiVerifierAssessmentConfig interface {
	GetAIVerifierMaxInputTokens() int
	GetAIVerifierMaxOutputTokens() int
	GetAIVerifierMaxCandidateContextTokens() int
	GetAIVerifierMaxPredicateOptions() int
	GetAIVerifierTokenizer() string
}

type memoryAutoWriteConfidenceConfig interface {
	GetMemoryAutoWriteConfidenceThreshold() float64
}

// AIVerifierAssessmentBudget contains the semantic limits applied to a single
// integrated assessor request. They intentionally remain optional on
// ConfigProvider so existing test providers do not silently become invalid.
type AIVerifierAssessmentBudget struct {
	MaxInputTokens            int
	MaxOutputTokens           int
	MaxCandidateContextTokens int
	MaxPredicateOptions       int
	Tokenizer                 string
}

// AIVerifierAssessmentBudgetFor reads the assessor budget from cfg and falls
// back to the V2.4 defaults when an older provider does not expose it.
func AIVerifierAssessmentBudgetFor(cfg ConfigProvider) AIVerifierAssessmentBudget {
	budget := AIVerifierAssessmentBudget{
		MaxInputTokens:            DefaultAIVerifierMaxInputTokens,
		MaxOutputTokens:           DefaultAIVerifierMaxOutputTokens,
		MaxCandidateContextTokens: DefaultAIVerifierMaxCandidateContextTokens,
		MaxPredicateOptions:       DefaultAIVerifierMaxPredicateOptions,
		Tokenizer:                 DefaultAIVerifierTokenizer,
	}
	assessmentConfig, ok := cfg.(aiVerifierAssessmentConfig)
	if !ok || assessmentConfig == nil {
		return budget
	}
	if value := assessmentConfig.GetAIVerifierMaxInputTokens(); value > 0 {
		budget.MaxInputTokens = value
	}
	if value := assessmentConfig.GetAIVerifierMaxOutputTokens(); value > 0 {
		budget.MaxOutputTokens = value
	}
	if value := assessmentConfig.GetAIVerifierMaxCandidateContextTokens(); value > 0 {
		budget.MaxCandidateContextTokens = value
	}
	if value := assessmentConfig.GetAIVerifierMaxPredicateOptions(); value > 0 {
		budget.MaxPredicateOptions = value
	}
	if value := strings.TrimSpace(assessmentConfig.GetAIVerifierTokenizer()); value != "" {
		budget.Tokenizer = value
	}
	return budget
}

// MemoryAutoWriteConfidenceThresholdFor returns the global bootstrap threshold
// used when a team has no valid memory_write override.
func MemoryAutoWriteConfidenceThresholdFor(cfg ConfigProvider) float64 {
	if cfg == nil {
		return DefaultMemoryAutoWriteConfidenceThreshold
	}
	thresholdConfig, ok := cfg.(memoryAutoWriteConfidenceConfig)
	if !ok {
		return DefaultMemoryAutoWriteConfidenceThreshold
	}
	value := thresholdConfig.GetMemoryAutoWriteConfidenceThreshold()
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 1 {
		return DefaultMemoryAutoWriteConfidenceThreshold
	}
	return value
}

func (c *Config) GetAIVerifierMaxInputTokens() int {
	if c.AIVerifierMaxInputTokens <= 0 {
		return DefaultAIVerifierMaxInputTokens
	}
	return c.AIVerifierMaxInputTokens
}

func (c *Config) GetAIVerifierMaxOutputTokens() int {
	if c.AIVerifierMaxOutputTokens <= 0 {
		return DefaultAIVerifierMaxOutputTokens
	}
	return c.AIVerifierMaxOutputTokens
}

func (c *Config) GetAIVerifierMaxCandidateContextTokens() int {
	if c.AIVerifierMaxCandidateContextTokens <= 0 {
		return DefaultAIVerifierMaxCandidateContextTokens
	}
	return c.AIVerifierMaxCandidateContextTokens
}

func (c *Config) GetAIVerifierMaxPredicateOptions() int {
	if c.AIVerifierMaxPredicateOptions <= 0 {
		return DefaultAIVerifierMaxPredicateOptions
	}
	return c.AIVerifierMaxPredicateOptions
}

func (c *Config) GetAIVerifierTokenizer() string {
	if tokenizer := strings.TrimSpace(c.AIVerifierTokenizer); tokenizer != "" {
		return tokenizer
	}
	return DefaultAIVerifierTokenizer
}

func (c *Config) GetMemoryAutoWriteConfidenceThreshold() float64 {
	if (!c.memoryAutoWriteConfidenceThresholdSet && c.MemoryAutoWriteConfidenceThreshold == 0) ||
		math.IsNaN(c.MemoryAutoWriteConfidenceThreshold) ||
		math.IsInf(c.MemoryAutoWriteConfidenceThreshold, 0) ||
		c.MemoryAutoWriteConfidenceThreshold < 0 ||
		c.MemoryAutoWriteConfidenceThreshold > 1 {
		return DefaultMemoryAutoWriteConfidenceThreshold
	}
	return c.MemoryAutoWriteConfidenceThreshold
}
