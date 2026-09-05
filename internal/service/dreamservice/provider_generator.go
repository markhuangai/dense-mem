package dreamservice

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/dreamgeneration"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/repository"
)

var ErrDreamProviderUnavailable = errors.New("dream generation provider is required")

type dreamGenerationProvider interface {
	GenerateDreams(context.Context, dreamgeneration.DreamGenerationRequest) (dreamgeneration.DreamGenerationResponse, error)
	ModelName() string
}

type ProviderGenerator struct {
	provider dreamGenerationProvider
}

type evidenceDiscoveryProvider interface {
	GenerateEvidenceDiscoveries(context.Context, dreamgeneration.EvidenceDiscoveryRequest) (dreamgeneration.EvidenceDiscoveryResponse, error)
	ModelName() string
}

// EvidenceProviderGenerator adapts the structured Dream provider to the
// server-owned evidence-discovery request. Durable IDs are mapped to opaque
// request-local references and restored only after complete response validation.
type EvidenceProviderGenerator struct {
	provider evidenceDiscoveryProvider
}

func NewEvidenceProviderGenerator(transport modelprovider.StructuredTransport, model string, limits assessor.SemanticAssessmentLimits) *EvidenceProviderGenerator {
	return &EvidenceProviderGenerator{provider: dreamgeneration.NewProvider(transport, model, limits)}
}

func (g *EvidenceProviderGenerator) Model() string {
	if g == nil || g.provider == nil {
		return ""
	}
	return strings.TrimSpace(g.provider.ModelName())
}

func (g *EvidenceProviderGenerator) GenerateEvidence(ctx context.Context, _ string, req EvidenceGenerationRequest) ([]GeneratedDream, GenerationDiagnostics, error) {
	if g == nil || g.provider == nil {
		return nil, GenerationDiagnostics{}, ErrDreamProviderUnavailable
	}
	request, mappings, err := providerEvidenceDiscoveryRequest(req)
	if err != nil {
		return nil, GenerationDiagnostics{}, err
	}
	response, err := g.provider.GenerateEvidenceDiscoveries(ctx, request)
	diagnostics := GenerationDiagnostics{
		ProviderTurns:        response.ProviderTurns,
		ProviderInputTokens:  response.InputTokens,
		ProviderOutputTokens: response.OutputTokens,
		ProviderProposals:    len(response.Proposals),
	}
	if err != nil {
		return nil, diagnostics, err
	}
	generated := make([]GeneratedDream, 0, len(response.Proposals))
	for _, proposal := range response.Proposals {
		dream, ok := mapEvidenceDiscoveryProposal(proposal, mappings)
		if ok {
			generated = append(generated, dream)
		}
	}
	return generated, diagnostics, nil
}

type evidenceProviderMappings struct {
	nodes      map[string]repository.EvidenceNode
	predicates map[string]repository.DreamTargetPredicate
	contexts   map[string]repository.EvidenceContext
}

func providerEvidenceDiscoveryRequest(req EvidenceGenerationRequest) (dreamgeneration.EvidenceDiscoveryRequest, evidenceProviderMappings, error) {
	maxOutputs := req.MaxOutputs
	if maxOutputs <= 0 {
		maxOutputs = DefaultMaxOutputs
	}
	if req.Target.EvidenceID == "" {
		return dreamgeneration.EvidenceDiscoveryRequest{}, evidenceProviderMappings{}, errors.New("evidence discovery target is required")
	}

	nodes := append([]repository.EvidenceNode(nil), req.Nodes...)
	if len(nodes) == 0 {
		nodes = evidenceNodesFromRecords(req.RelatedRelationships, req.RelatedHypotheses)
	}
	sort.SliceStable(nodes, func(i, j int) bool {
		if nodes[i].Kind != nodes[j].Kind {
			return nodes[i].Kind < nodes[j].Kind
		}
		return nodes[i].ID < nodes[j].ID
	})
	providerNodes := make([]dreamgeneration.EvidenceDiscoveryNode, 0, min(len(nodes), dreamgeneration.EvidenceDiscoveryMaxNodes))
	nodeRefs := make(map[string]string, len(nodes))
	nodeByRef := make(map[string]repository.EvidenceNode, len(nodes))
	for _, node := range nodes {
		key := strings.TrimSpace(node.ID) + "\x00" + strings.TrimSpace(node.Kind)
		if strings.TrimSpace(node.ID) == "" || nodeRefs[key] != "" {
			continue
		}
		ref := fmt.Sprintf("node_%d", len(providerNodes)+1)
		nodeRefs[key] = ref
		nodeByRef[ref] = node
		providerNodes = append(providerNodes, dreamgeneration.EvidenceDiscoveryNode{
			Ref: ref, Display: dreamDisplay(node.Display, node.Kind), Kind: node.Kind,
		})
		if len(providerNodes) >= dreamgeneration.EvidenceDiscoveryMaxNodes {
			break
		}
	}

	predicates := append([]repository.DreamTargetPredicate(nil), req.AllowedPredicates...)
	sort.SliceStable(predicates, func(i, j int) bool {
		left := strings.ToLower(strings.TrimSpace(predicates[i].PredicateKey))
		right := strings.ToLower(strings.TrimSpace(predicates[j].PredicateKey))
		if left != right {
			return left < right
		}
		return predicates[i].Version < predicates[j].Version
	})
	providerPredicates := make([]dreamgeneration.EvidenceDiscoveryPredicate, 0, min(len(predicates), dreamgeneration.EvidenceDiscoveryMaxPredicates))
	predicateRefs := make(map[string]string, len(predicates))
	predicateByRef := make(map[string]repository.DreamTargetPredicate, len(predicates))
	for _, predicate := range predicates {
		key := strings.ToLower(strings.TrimSpace(predicate.PredicateKey))
		if key == "" || predicate.Version < 1 || predicateRefs[key] != "" {
			continue
		}
		ref := fmt.Sprintf("predicate_%d", len(providerPredicates)+1)
		predicateRefs[key] = ref
		predicateByRef[ref] = predicate
		providerPredicates = append(providerPredicates, dreamgeneration.EvidenceDiscoveryPredicate{
			Ref: ref, Label: predicate.PredicateKey, Version: predicate.Version,
			AllowedSubjectKinds: append([]string(nil), predicate.AllowedSubjectKinds...),
			AllowedObjectKinds:  append([]string(nil), predicate.AllowedObjectKinds...),
			RelationshipKind:    predicate.RelationshipKind, CurrentCardinality: predicate.CurrentCardinality,
		})
		if len(providerPredicates) >= dreamgeneration.EvidenceDiscoveryMaxPredicates {
			break
		}
	}

	orderedContexts := make([]repository.EvidenceContext, 0, len(req.Contexts)+1)
	orderedContexts = append(orderedContexts, repository.EvidenceContext{
		EvidenceID: req.Target.EvidenceID, FragmentID: req.Target.FragmentID,
		SourceID: req.Target.SourceID, SourceRevisionID: req.Target.SourceRevisionID,
		SourceGroupKey: req.Target.SourceGroupKey, Authority: req.Target.Authority, Content: req.Target.Content,
	})
	seenContextIDs := map[string]struct{}{req.Target.EvidenceID: {}}
	for _, context := range req.Contexts {
		if context.EvidenceID == "" {
			continue
		}
		if _, exists := seenContextIDs[context.EvidenceID]; exists {
			continue
		}
		seenContextIDs[context.EvidenceID] = struct{}{}
		orderedContexts = append(orderedContexts, context)
		if len(orderedContexts) >= dreamgeneration.EvidenceDiscoveryMaxContexts {
			break
		}
	}
	providerContexts := make([]dreamgeneration.EvidenceDiscoveryContext, 0, len(orderedContexts))
	contextByRef := make(map[string]repository.EvidenceContext, len(orderedContexts))
	for index, context := range orderedContexts {
		ref := "evidence_target"
		if index > 0 {
			ref = fmt.Sprintf("evidence_context_%d", index)
		}
		providerContexts = append(providerContexts, dreamgeneration.EvidenceDiscoveryContext{
			EvidenceRef: ref, Content: context.Content, Authority: context.Authority, SourceGroupKey: context.SourceGroupKey,
		})
		contextByRef[ref] = context
	}
	if len(providerContexts) == 0 {
		return dreamgeneration.EvidenceDiscoveryRequest{}, evidenceProviderMappings{}, errors.New("evidence discovery context is required")
	}

	request := dreamgeneration.EvidenceDiscoveryRequest{
		RequestID:  "evidence_request_" + strings.ReplaceAll(uuid.NewString(), "-", ""),
		MaxOutputs: maxOutputs, TargetRef: "evidence_target", Contexts: providerContexts,
		Nodes: providerNodes, AllowedPredicates: providerPredicates,
		RelatedRelationships: providerRelatedRelationships(req.RelatedRelationships, nodeRefs, predicateRefs),
		RelatedHypotheses:    providerRelatedHypotheses(req.RelatedHypotheses, nodeRefs, predicateRefs),
	}
	if len(request.RelatedRelationships) > dreamgeneration.EvidenceDiscoveryMaxRelated {
		request.RelatedRelationships = request.RelatedRelationships[:dreamgeneration.EvidenceDiscoveryMaxRelated]
	}
	remainingRelated := dreamgeneration.EvidenceDiscoveryMaxRelated - len(request.RelatedRelationships)
	if len(request.RelatedHypotheses) > remainingRelated {
		request.RelatedHypotheses = request.RelatedHypotheses[:remainingRelated]
	}
	return request, evidenceProviderMappings{nodes: nodeByRef, predicates: predicateByRef, contexts: contextByRef}, nil
}

func evidenceNodesFromRecords(relationships []repository.DreamInput, hypotheses []repository.HypothesisRecord) []repository.EvidenceNode {
	seen := map[string]struct{}{}
	out := []repository.EvidenceNode{}
	add := func(id, display, kind string) {
		id = strings.TrimSpace(id)
		kind = strings.TrimSpace(kind)
		if id == "" || kind == "" {
			return
		}
		key := id + "\x00" + kind
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		out = append(out, repository.EvidenceNode{ID: id, Display: display, Kind: kind})
	}
	for _, item := range relationships {
		add(item.SubjectEntityID, item.SubjectName, item.SubjectKind)
		if item.ObjectEntityID != "" {
			add(item.ObjectEntityID, item.ObjectName, item.ObjectKind)
		} else {
			add(item.ObjectValueID, item.ObjectName, "value")
		}
	}
	for _, item := range hypotheses {
		add(item.SubjectEntityID, "", "entity")
		if item.ObjectEntityID != "" {
			add(item.ObjectEntityID, "", "entity")
		} else {
			add(item.ObjectValueID, "", "value")
		}
	}
	return out
}

func providerNodeRef(nodeRefs map[string]string, id, kind string) string {
	id = strings.TrimSpace(id)
	kind = strings.TrimSpace(kind)
	if ref := nodeRefs[id+"\x00"+kind]; ref != "" {
		return ref
	}
	if kind == string(domain.SemanticNodeEntity) {
		for _, candidate := range domain.EntityKinds() {
			if ref := nodeRefs[id+"\x00"+candidate]; ref != "" {
				return ref
			}
		}
	}
	if kind == string(domain.SemanticNodeValue) {
		for _, candidate := range domain.ValueTypes() {
			if ref := nodeRefs[id+"\x00"+candidate]; ref != "" {
				return ref
			}
		}
	}
	return ""
}

func providerRelatedRelationships(items []repository.DreamInput, nodeRefs, predicateRefs map[string]string) []dreamgeneration.EvidenceDiscoveryRelationship {
	out := make([]dreamgeneration.EvidenceDiscoveryRelationship, 0, min(len(items), dreamgeneration.EvidenceDiscoveryMaxRelated))
	for _, item := range items {
		if len(out) >= dreamgeneration.EvidenceDiscoveryMaxRelated {
			break
		}
		objectID, objectKind := item.ObjectEntityID, item.ObjectKind
		if objectID == "" {
			objectID, objectKind = item.ObjectValueID, "value"
		}
		predicateRef := predicateRefs[strings.ToLower(strings.TrimSpace(item.PredicateKey))]
		subjectRef := providerNodeRef(nodeRefs, item.SubjectEntityID, item.SubjectKind)
		objectRef := providerNodeRef(nodeRefs, objectID, objectKind)
		if subjectRef == "" || objectRef == "" || predicateRef == "" {
			continue
		}
		out = append(out, dreamgeneration.EvidenceDiscoveryRelationship{
			Ref: fmt.Sprintf("relationship_%d", len(out)+1), SubjectRef: subjectRef, Predicate: predicateRef,
			ObjectRef: objectRef, Status: item.Status,
		})
	}
	return out
}

func providerRelatedHypotheses(items []repository.HypothesisRecord, nodeRefs, predicateRefs map[string]string) []dreamgeneration.EvidenceDiscoveryHypothesis {
	out := make([]dreamgeneration.EvidenceDiscoveryHypothesis, 0, min(len(items), dreamgeneration.EvidenceDiscoveryMaxRelated))
	for _, item := range items {
		if len(out) >= dreamgeneration.EvidenceDiscoveryMaxRelated {
			break
		}
		objectID, objectKind := item.ObjectEntityID, "entity"
		if objectID == "" {
			objectID, objectKind = item.ObjectValueID, "value"
		}
		predicateRef := predicateRefs[strings.ToLower(strings.TrimSpace(item.PredicateKey))]
		subjectRef := providerNodeRef(nodeRefs, item.SubjectEntityID, "entity")
		objectRef := providerNodeRef(nodeRefs, objectID, objectKind)
		if subjectRef == "" || objectRef == "" || predicateRef == "" {
			continue
		}
		out = append(out, dreamgeneration.EvidenceDiscoveryHypothesis{
			Ref: fmt.Sprintf("hypothesis_%d", len(out)+1), SubjectRef: subjectRef, Predicate: predicateRef,
			ObjectRef: objectRef, Status: item.Status,
		})
	}
	return out
}

func mapEvidenceDiscoveryProposal(proposal dreamgeneration.EvidenceDiscoveryProposal, mappings evidenceProviderMappings) (GeneratedDream, bool) {
	subject, subjectOK := mappings.nodes[proposal.SubjectRef]
	object, objectOK := mappings.nodes[proposal.ObjectRef]
	predicate, predicateOK := mappings.predicates[proposal.PredicateRef]
	if !subjectOK || !objectOK || !predicateOK || !evidenceNodeIsEntity(subject.Kind) ||
		(!evidenceNodeIsEntity(object.Kind) && !evidenceNodeIsValue(object.Kind)) {
		return GeneratedDream{}, false
	}
	derivations := make([]repository.EvidenceDerivationSource, 0, len(proposal.Derivations))
	evidenceRefs := make([]string, 0, len(proposal.Derivations))
	seenEvidence := map[string]struct{}{}
	for _, citation := range proposal.Derivations {
		context, ok := mappings.contexts[citation.EvidenceRef]
		if !ok || citation.Start < 0 || citation.End <= citation.Start {
			return GeneratedDream{}, false
		}
		runes := []rune(context.Content)
		if citation.End > len(runes) {
			return GeneratedDream{}, false
		}
		if _, exists := seenEvidence[context.EvidenceID]; !exists {
			evidenceRefs = append(evidenceRefs, context.EvidenceID)
			seenEvidence[context.EvidenceID] = struct{}{}
		}
		derivations = append(derivations, repository.EvidenceDerivationSource{
			EvidenceID: context.EvidenceID, FragmentID: context.FragmentID, SourceID: context.SourceID,
			SourceRevisionID: context.SourceRevisionID, SourceGroupKey: context.SourceGroupKey,
			SpanStart: citation.Start, SpanEnd: citation.End,
			Quote: string(runes[citation.Start:citation.End]), Authority: context.Authority,
		})
	}
	if len(derivations) == 0 {
		return GeneratedDream{}, false
	}
	dream := GeneratedDream{
		Hypothesis: proposal.Statement, Rationale: proposal.Rationale, WhatIf: proposal.WhatIf,
		PossibleOutcome: proposal.PossibleOutcome, Likelihood: proposal.Likelihood, Confidence: proposal.Confidence,
		SubjectEntityID: subject.ID, PredicateKey: predicate.PredicateKey, PredicateVersion: predicate.Version,
		EvidenceRefs: evidenceRefs, EvidenceDerivations: derivations,
	}
	if evidenceNodeIsEntity(object.Kind) {
		dream.ObjectEntityID = object.ID
	} else {
		dream.ObjectValueID = object.ID
	}
	return dream, true
}

func evidenceNodeIsEntity(kind string) bool {
	kind = strings.TrimSpace(kind)
	if kind == string(domain.SemanticNodeEntity) {
		return true
	}
	for _, candidate := range domain.EntityKinds() {
		if strings.EqualFold(kind, candidate) {
			return true
		}
	}
	return false
}

func evidenceNodeIsValue(kind string) bool {
	kind = strings.TrimSpace(kind)
	if kind == string(domain.SemanticNodeValue) {
		return true
	}
	for _, candidate := range domain.ValueTypes() {
		if strings.EqualFold(kind, candidate) {
			return true
		}
	}
	return false
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

func providerDreamGenerationRequest(req GenerateRequest) (dreamgeneration.DreamGenerationRequest, error) {
	paths := make([]dreamgeneration.DreamGenerationPath, 0, len(req.Paths))
	for _, path := range req.Paths {
		if len(path.Premises) != 2 {
			return dreamgeneration.DreamGenerationRequest{}, fmt.Errorf("dream generation path %q must have two premises", path.PathRef)
		}
		premises := make([]dreamgeneration.DreamGenerationPremise, 0, len(path.Premises))
		for _, premise := range path.Premises {
			evidence := make([]dreamgeneration.DreamGenerationEvidence, 0, len(premise.Input.Evidence))
			for _, excerpt := range premise.Input.Evidence {
				evidence = append(evidence, dreamgeneration.DreamGenerationEvidence{
					EvidenceRef:    excerpt.EvidenceRef,
					Content:        excerpt.Content,
					SourceGroupKey: excerpt.SourceGroupKey,
					Authority:      excerpt.Authority,
				})
			}
			premises = append(premises, dreamgeneration.DreamGenerationPremise{
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
		predicates := make([]dreamgeneration.DreamGenerationPredicate, 0, len(path.AllowedPredicates))
		for _, predicate := range path.AllowedPredicates {
			predicates = append(predicates, dreamgeneration.DreamGenerationPredicate{
				PredicateRef:       predicate.PredicateRef,
				Label:              predicate.PredicateKey,
				RelationshipKind:   predicate.RelationshipKind,
				CurrentCardinality: predicate.CurrentCardinality,
			})
		}
		paths = append(paths, dreamgeneration.DreamGenerationPath{
			PathRef: path.PathRef,
			Subject: dreamgeneration.DreamGenerationNode{
				Ref: path.Subject.Ref, Display: path.Subject.Display, Kind: path.Subject.Kind,
			},
			Middle: dreamgeneration.DreamGenerationNode{
				Ref: path.Middle.Ref, Display: path.Middle.Display, Kind: path.Middle.Kind,
			},
			Object: dreamgeneration.DreamGenerationNode{
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
	return dreamgeneration.DreamGenerationRequest{
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
