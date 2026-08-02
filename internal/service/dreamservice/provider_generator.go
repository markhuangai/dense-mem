package dreamservice

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/verifier"
)

var ErrDreamProviderUnavailable = errors.New("dream generation provider is required")

type dreamGenerationProvider interface {
	GenerateDreams(context.Context, verifier.DreamGenerationRequest) (verifier.DreamGenerationResponse, error)
	ModelName() string
}

type ProviderGenerator struct {
	provider dreamGenerationProvider
}

func NewProviderGenerator(provider dreamGenerationProvider) *ProviderGenerator {
	return &ProviderGenerator{provider: provider}
}

func (g *ProviderGenerator) Model() string {
	if g == nil || g.provider == nil {
		return ""
	}
	return strings.TrimSpace(g.provider.ModelName())
}

func (g *ProviderGenerator) Generate(ctx context.Context, _ string, req GenerateRequest) ([]GeneratedDream, error) {
	generated, _, err := g.GenerateWithDiagnostics(ctx, "", req)
	return generated, err
}

func (g *ProviderGenerator) GenerateWithDiagnostics(ctx context.Context, _ string, req GenerateRequest) ([]GeneratedDream, GenerationDiagnostics, error) {
	if g == nil || g.provider == nil {
		return nil, GenerationDiagnostics{}, ErrDreamProviderUnavailable
	}
	if len(req.Paths) == 0 {
		return []GeneratedDream{}, GenerationDiagnostics{}, nil
	}
	request, err := providerDreamGenerationRequest(req)
	if err != nil {
		return nil, GenerationDiagnostics{}, err
	}
	response, err := g.provider.GenerateDreams(ctx, request)
	if err != nil {
		return nil, GenerationDiagnostics{}, err
	}
	generated := make([]GeneratedDream, 0, len(response.Proposals))
	for _, proposal := range response.Proposals {
		generated = append(generated, GeneratedDream{
			PathRef:         proposal.PathRef,
			PredicateRef:    proposal.PredicateRef,
			EvidenceRefs:    append([]string(nil), proposal.EvidenceRefs...),
			Hypothesis:      proposal.Statement,
			Rationale:       proposal.Rationale,
			WhatIf:          proposal.WhatIf,
			PossibleOutcome: proposal.PossibleOutcome,
			Likelihood:      proposal.Likelihood,
			Confidence:      proposal.Confidence,
		})
	}
	return generated, GenerationDiagnostics{
		ProviderTurns:        response.ProviderTurns,
		ProviderInputTokens:  response.InputTokens,
		ProviderOutputTokens: response.OutputTokens,
		ProviderProposals:    len(response.Proposals),
	}, nil
}

func providerDreamGenerationRequest(req GenerateRequest) (verifier.DreamGenerationRequest, error) {
	paths := make([]verifier.DreamGenerationPath, 0, len(req.Paths))
	for _, path := range req.Paths {
		if len(path.Premises) != 2 {
			return verifier.DreamGenerationRequest{}, fmt.Errorf("dream generation path %q must have two premises", path.PathRef)
		}
		premises := make([]verifier.DreamGenerationPremise, 0, len(path.Premises))
		for _, premise := range path.Premises {
			evidence := make([]verifier.DreamGenerationEvidence, 0, len(premise.Input.Evidence))
			for _, excerpt := range premise.Input.Evidence {
				evidence = append(evidence, verifier.DreamGenerationEvidence{
					EvidenceRef:    excerpt.EvidenceRef,
					Content:        excerpt.Content,
					SourceGroupKey: excerpt.SourceGroupKey,
					Authority:      excerpt.Authority,
				})
			}
			premises = append(premises, verifier.DreamGenerationPremise{
				PremiseRef:          premise.PremiseRef,
				RelationshipRef:     premise.RelationshipRef,
				PredicateLabel:      premise.Input.PredicateKey,
				RelationshipVersion: premise.Input.Version,
				Status:              premise.Input.Status,
				FromRef:             dreamPathPremiseFromRef(path, premise),
				ToRef:               dreamPathPremiseToRef(path, premise),
				Evidence:            evidence,
			})
		}
		predicates := make([]verifier.DreamGenerationPredicate, 0, len(path.AllowedPredicates))
		for _, predicate := range path.AllowedPredicates {
			predicates = append(predicates, verifier.DreamGenerationPredicate{
				PredicateRef:       predicate.PredicateRef,
				Label:              predicate.PredicateKey,
				RelationshipKind:   predicate.RelationshipKind,
				CurrentCardinality: predicate.CurrentCardinality,
			})
		}
		paths = append(paths, verifier.DreamGenerationPath{
			PathRef: path.PathRef,
			Subject: verifier.DreamGenerationNode{
				Ref: path.Subject.Ref, Display: path.Subject.Display, Kind: path.Subject.Kind,
			},
			Middle: verifier.DreamGenerationNode{
				Ref: path.Middle.Ref, Display: path.Middle.Display, Kind: path.Middle.Kind,
			},
			Object: verifier.DreamGenerationNode{
				Ref: path.Object.Ref, Display: path.Object.Display, Kind: path.Object.Kind,
			},
			Premises:          premises,
			AllowedPredicates: predicates,
		})
	}
	maxOutputs := req.MaxOutputs
	if maxOutputs <= 0 {
		maxOutputs = DefaultMaxOutputs
	}
	return verifier.DreamGenerationRequest{
		RequestID:  "dream_request_" + strings.ReplaceAll(uuid.NewString(), "-", ""),
		MaxOutputs: maxOutputs,
		Paths:      paths,
	}, nil
}

func dreamPathPremiseFromRef(path DreamPath, premise DreamPathPremise) string {
	if len(path.Premises) > 0 && premise.RelationshipRef == path.Premises[0].RelationshipRef {
		return path.Subject.Ref
	}
	return path.Middle.Ref
}

func dreamPathPremiseToRef(path DreamPath, premise DreamPathPremise) string {
	if len(path.Premises) > 0 && premise.RelationshipRef == path.Premises[0].RelationshipRef {
		return path.Middle.Ref
	}
	return path.Object.Ref
}

type unavailableGenerator struct{}

func (unavailableGenerator) Model() string { return "" }

func (unavailableGenerator) Generate(context.Context, string, GenerateRequest) ([]GeneratedDream, error) {
	return nil, ErrDreamProviderUnavailable
}
