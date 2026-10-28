package modelprovider

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/markhuangai/dense-mem/internal/domain"
)

type DecisionKind string

const (
	DecisionPredicate           DecisionKind = "predicate"
	DecisionChoice              DecisionKind = "choice"
	DecisionScore               DecisionKind = "score"
	DecisionRefusal             DecisionKind = "refusal"
	DecisionMaxBatchQuestions                = 400
	DecisionMaxRequestQuestions              = 4000
	DecisionMaxGroups                        = 400
)

type DecisionOption struct {
	Value       string `json:"value"`
	Description string `json:"description"`
}

type DecisionLevel struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

type DecisionQuestion struct {
	Name         string           `json:"name"`
	Type         DecisionKind     `json:"type"`
	Instructions string           `json:"instructions"`
	Choices      []DecisionOption `json:"choices,omitempty"`
	Levels       []DecisionLevel  `json:"levels,omitempty"`
}

// DecisionGroup is indivisible when a provider packs independent work into requests.
type DecisionGroup struct {
	ID        string
	Reference string
	Input     any
	Questions []DecisionQuestion
}

type DecisionRequest struct {
	Workload        string
	OperationID     string
	SettingsVersion string
	Groups          []DecisionGroup
}

type DecisionAnswer struct {
	Type          DecisionKind       `json:"type"`
	Probability   *float64           `json:"probability,omitempty"`
	Choice        string             `json:"choice,omitempty"`
	Score         *float64           `json:"score,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Confidence    *float64           `json:"confidence,omitempty"`
}

type DecisionUsage struct {
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
	CachedTokens     int `json:"cached_tokens"`
	CacheWriteTokens int `json:"cache_write_tokens"`
}

type DecisionReceipt struct {
	Provider             string           `json:"provider"`
	RequestedModel       string           `json:"requested_model"`
	Model                string           `json:"model"`
	RequestHash          string           `json:"request_hash"`
	ResponseHash         string           `json:"response_hash"`
	ProviderRequestID    string           `json:"provider_request_id,omitempty"`
	Batch                int              `json:"batch"`
	Attempt              int              `json:"attempt"`
	QuestionCount        int              `json:"question_count"`
	EstimatedInputTokens int              `json:"estimated_input_tokens"`
	Tokenizer            string           `json:"tokenizer"`
	Usage                *DecisionUsage   `json:"usage"`
	Diagnostics          *CallDiagnostics `json:"diagnostics,omitempty"`
}

type DecisionResult struct {
	Answers  map[string]DecisionAnswer `json:"answers"`
	Receipts []DecisionReceipt         `json:"receipts"`
}

type DecisionProvider interface {
	Decide(context.Context, DecisionRequest) (DecisionResult, error)
}

type DecisionSettings interface {
	DecisionRuntimeConfig(context.Context) (domain.DecisionRuntimeConfig, error)
}

// DecisionStage binds one complete task result to the authorized inputs and policy.
type DecisionStage struct {
	References     map[string]string            `json:"references,omitempty"`
	SelectedIDs    []string                     `json:"selected_ids,omitempty"`
	Workload       string                       `json:"workload"`
	InputHash      string                       `json:"input_hash"`
	Settings       domain.DecisionRuntimeConfig `json:"settings"`
	PolicyRevision string                       `json:"policy_revision"`
	Thresholds     map[string]float64           `json:"thresholds,omitempty"`
	Action         string                       `json:"action"`
	Reason         string                       `json:"reason"`
	Result         DecisionResult               `json:"result"`
}

func ValidateDecisionRequest(request DecisionRequest) error {
	if strings.TrimSpace(request.Workload) == "" || len(request.Groups) == 0 || len(request.Groups) > DecisionMaxGroups {
		return fmt.Errorf("decision workload and 1–%d groups are required", DecisionMaxGroups)
	}
	groups, names := map[string]bool{}, map[string]bool{}
	for _, group := range request.Groups {
		if strings.TrimSpace(group.ID) == "" || len(group.ID) > 128 || groups[group.ID] || len(group.Questions) == 0 || len(group.Questions) > DecisionMaxBatchQuestions {
			return fmt.Errorf("decision group identity or question count is invalid")
		}
		groups[group.ID] = true
		for _, question := range group.Questions {
			if question.Name == "" || len(question.Name) > 128 || names[question.Name] || strings.TrimSpace(question.Instructions) == "" {
				return fmt.Errorf("decision question identity or instructions are invalid")
			}
			names[question.Name] = true
			if err := validateDecisionQuestion(question); err != nil {
				return err
			}
		}
	}
	if len(names) > DecisionMaxRequestQuestions {
		return fmt.Errorf("decision request exceeds %d questions", DecisionMaxRequestQuestions)
	}
	return nil
}

func validateDecisionQuestion(q DecisionQuestion) error {
	switch q.Type {
	case DecisionPredicate:
		if len(q.Choices) != 0 || len(q.Levels) != 0 {
			return fmt.Errorf("predicate question cannot supply choices or levels")
		}
	case DecisionChoice:
		if len(q.Choices) < 2 || len(q.Choices) > 255 || len(q.Levels) != 0 {
			return fmt.Errorf("choice question requires 2–255 options and no score levels")
		}
		seen := map[string]bool{}
		for _, option := range q.Choices {
			if option.Value == "" || seen[option.Value] {
				return fmt.Errorf("choice options must have unique nonempty values")
			}
			seen[option.Value] = true
		}
	case DecisionScore:
		if len(q.Levels) < 2 || len(q.Levels) > 10 || len(q.Choices) != 0 {
			return fmt.Errorf("score question requires 2–10 ordered levels and no choices")
		}
		for _, level := range q.Levels {
			if strings.TrimSpace(level.Label) == "" {
				return fmt.Errorf("score level labels must be nonempty")
			}
		}
	default:
		return fmt.Errorf("unsupported decision question type")
	}
	return nil
}

func ValidateDecisionAnswers(questions []DecisionQuestion, answers map[string]DecisionAnswer) error {
	if len(answers) != len(questions) {
		return fmt.Errorf("decision answer count mismatch: expected %d, received %d", len(questions), len(answers))
	}
	seen := map[string]bool{}
	for _, question := range questions {
		answer, ok := answers[question.Name]
		if !ok || seen[question.Name] {
			return fmt.Errorf("decision answer identity mismatch for %q", question.Name)
		}
		seen[question.Name] = true
		if err := validateDecisionAnswer(question, answer); err != nil {
			return fmt.Errorf("decision answer %q: %w", question.Name, err)
		}
	}
	return nil
}

func validateDecisionAnswer(q DecisionQuestion, a DecisionAnswer) error {
	if a.Type == DecisionRefusal {
		if a.Probability != nil || a.Confidence != nil || a.Score != nil || a.Choice != "" || len(a.Probabilities) != 0 {
			return fmt.Errorf("refusal contains answer fields")
		}
		return nil
	}
	if a.Type != q.Type {
		return fmt.Errorf("expected type %q, received %q", q.Type, a.Type)
	}
	if q.Type == DecisionPredicate {
		if !decisionProbability(a.Probability) || a.Score != nil || a.Choice != "" || a.Confidence != nil || len(a.Probabilities) != 0 {
			return fmt.Errorf("predicate must contain only one finite probability in [0,1]")
		}
		return nil
	}
	if !decisionProbability(a.Confidence) || a.Probability != nil {
		return fmt.Errorf("choice and score require a separate finite confidence in [0,1]")
	}
	options := make([]string, 0)
	if q.Type == DecisionChoice {
		if a.Score != nil {
			return fmt.Errorf("choice contains score")
		}
		for _, option := range q.Choices {
			options = append(options, option.Value)
		}
	} else {
		if a.Score == nil || math.IsNaN(*a.Score) || math.IsInf(*a.Score, 0) || a.Choice != "" {
			return fmt.Errorf("score must be finite and cannot contain a choice")
		}
		for index := range q.Levels {
			options = append(options, strconv.Itoa(index))
		}
	}
	if len(options) != len(a.Probabilities) {
		return fmt.Errorf("probability distribution does not cover exactly the supplied options")
	}
	total, weighted, highest := 0.0, 0.0, 0.0
	for index, option := range options {
		probability, ok := a.Probabilities[option]
		if !ok || !decisionProbability(&probability) {
			return fmt.Errorf("probability distribution contains a missing or invalid option")
		}
		total += probability
		weighted += float64(index) * probability
		highest = math.Max(highest, probability)
	}
	if math.Abs(total-1) > 0.001 {
		return fmt.Errorf("probability distribution does not sum to one")
	}
	if q.Type == DecisionChoice {
		if probability, ok := a.Probabilities[a.Choice]; !ok || probability < highest {
			return fmt.Errorf("selected choice is not a supplied maximum-probability option")
		}
	} else if *a.Score < 0 || *a.Score > float64(len(options)-1) || math.Abs(*a.Score-weighted) > 0.001 {
		return fmt.Errorf("score does not match the supplied distribution")
	}
	return nil
}

func decisionProbability(value *float64) bool {
	return value != nil && !math.IsNaN(*value) && !math.IsInf(*value, 0) && *value >= 0 && *value <= 1
}
