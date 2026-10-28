package modelprovider

import (
	"math"
	"testing"
)

func TestDecisionAnswersValidateCompleteTypedCoverage(t *testing.T) {
	questions := []DecisionQuestion{
		{Name: "safe", Type: DecisionPredicate, Instructions: "Is the evidence safe?"},
		{Name: "position", Type: DecisionChoice, Instructions: "Choose a position.", Choices: []DecisionOption{{Value: "a"}, {Value: "b"}}},
		{Name: "relevance", Type: DecisionScore, Instructions: "Rate relevance.", Levels: []DecisionLevel{{Label: "irrelevant"}, {Label: "relevant"}}},
	}
	value := func(v float64) *float64 { return &v }
	valid := func() map[string]DecisionAnswer {
		return map[string]DecisionAnswer{
			"safe":      {Type: DecisionPredicate, Probability: value(0.99)},
			"position":  {Type: DecisionChoice, Choice: "a", Probabilities: map[string]float64{"a": 0.99, "b": 0.01}, Confidence: value(0.98)},
			"relevance": {Type: DecisionScore, Score: value(0.8), Probabilities: map[string]float64{"0": 0.2, "1": 0.8}, Confidence: value(0.5)},
		}
	}
	if err := ValidateDecisionAnswers(questions, valid()); err != nil {
		t.Fatal(err)
	}
	for name, edit := range map[string]func(map[string]DecisionAnswer){
		"missing":            func(a map[string]DecisionAnswer) { delete(a, "safe") },
		"unknown":            func(a map[string]DecisionAnswer) { a["extra"] = a["safe"] },
		"wrong type":         func(a map[string]DecisionAnswer) { a["safe"] = a["position"] },
		"absent probability": func(a map[string]DecisionAnswer) { a["safe"] = DecisionAnswer{Type: DecisionPredicate} },
		"nonfinite": func(a map[string]DecisionAnswer) {
			a["safe"] = DecisionAnswer{Type: DecisionPredicate, Probability: value(math.NaN())}
		},
		"out of range": func(a map[string]DecisionAnswer) {
			a["safe"] = DecisionAnswer{Type: DecisionPredicate, Probability: value(1.01)}
		},
		"missing option":       func(a map[string]DecisionAnswer) { delete(a["position"].Probabilities, "b") },
		"foreign option":       func(a map[string]DecisionAnswer) { a["position"].Probabilities["foreign"] = 0 },
		"invalid sum":          func(a map[string]DecisionAnswer) { a["position"].Probabilities["a"] = 0.8 },
		"wrong winner":         func(a map[string]DecisionAnswer) { v := a["position"]; v.Choice = "b"; a["position"] = v },
		"wrong weighted score": func(a map[string]DecisionAnswer) { v := a["relevance"]; v.Score = value(0.2); a["relevance"] = v },
		"unexpected field":     func(a map[string]DecisionAnswer) { v := a["safe"]; v.Score = value(0.5); a["safe"] = v },
	} {
		t.Run(name, func(t *testing.T) {
			answers := valid()
			edit(answers)
			if err := ValidateDecisionAnswers(questions, answers); err == nil {
				t.Fatal("invalid complete response was accepted")
			}
		})
	}
	answers := valid()
	answers["safe"] = DecisionAnswer{Type: DecisionRefusal}
	if err := ValidateDecisionAnswers(questions, answers); err != nil {
		t.Fatalf("refusal is a typed response, not malformed output: %v", err)
	}
}
