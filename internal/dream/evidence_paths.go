package dream

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/markhuangai/dense-mem/internal/domain"
	dreamcontract "github.com/markhuangai/dense-mem/internal/dream/contract"
)

const (
	maxDreamPathsPerGeneration       = 40
	maxDreamAllowedPredicatesPerPath = 100
)

func buildDreamPaths(inputs []dreamcontract.DreamInput, predicates []dreamcontract.DreamTargetPredicate, maxOutputs int) []DreamPath {
	bySubject := make(map[string][]dreamcontract.DreamInput, len(inputs))
	for _, input := range inputs {
		if input.SubjectEntityID == "" || len(input.Evidence) == 0 {
			continue
		}
		bySubject[input.SubjectEntityID] = append(bySubject[input.SubjectEntityID], input)
	}
	for subjectID := range bySubject {
		sort.Slice(bySubject[subjectID], func(i, j int) bool {
			return bySubject[subjectID][i].RelationshipID < bySubject[subjectID][j].RelationshipID
		})
	}
	firsts := append([]dreamcontract.DreamInput(nil), inputs...)
	sort.Slice(firsts, func(i, j int) bool { return firsts[i].RelationshipID < firsts[j].RelationshipID })

	nodeRefs := map[string]string{}
	nextNodeRef := 0
	nodeFor := func(id, display, kind string) DreamPathNode {
		key := strings.TrimSpace(kind) + "\x00" + id
		ref := nodeRefs[key]
		if ref == "" {
			nextNodeRef++
			ref = fmt.Sprintf("node_%d", nextNodeRef)
			nodeRefs[key] = ref
		}
		return DreamPathNode{Ref: ref, ID: id, Display: dreamDisplay(display, kind), Kind: kind}
	}
	predicateRefs := map[string]string{}
	nextPredicateRef := 0
	refForPredicate := func(predicate dreamcontract.DreamTargetPredicate) string {
		key := strings.ToLower(strings.TrimSpace(predicate.PredicateKey)) + fmt.Sprintf("\x00%d", predicate.Version)
		ref := predicateRefs[key]
		if ref == "" {
			nextPredicateRef++
			ref = fmt.Sprintf("predicate_%d", nextPredicateRef)
			predicateRefs[key] = ref
		}
		return ref
	}
	nextPathRef := 0
	nextPremiseRef := 0
	nextRelationshipRef := 0
	nextEvidenceRef := 0
	paths := make([]DreamPath, 0, maxDreamPathsPerGeneration)
	for _, first := range firsts {
		if first.ObjectEntityID == "" || first.SubjectEntityID == first.ObjectEntityID || len(first.Evidence) == 0 {
			continue
		}
		for _, second := range bySubject[first.ObjectEntityID] {
			if first.RelationshipID == second.RelationshipID ||
				second.ObjectEntityID == "" && second.ObjectValueID == "" ||
				second.ObjectEntityID != "" && second.SubjectEntityID == second.ObjectEntityID ||
				second.ObjectEntityID == first.SubjectEntityID ||
				len(second.Evidence) == 0 {
				continue
			}
			if first.Status != "active" && second.Status != "active" {
				continue
			}
			allowed := dreamAllowedPredicates(first.SubjectKind, second.ObjectKind, predicates, refForPredicate)
			if len(allowed) == 0 {
				continue
			}
			nextPathRef++
			nextPremiseRef++
			nextRelationshipRef++
			firstCopy := dreamInputWithOpaqueEvidence(first, &nextEvidenceRef)
			firstPremise := DreamPathPremise{
				PremiseRef:      fmt.Sprintf("premise_%d", nextPremiseRef),
				RelationshipRef: fmt.Sprintf("relationship_%d", nextRelationshipRef),
				Input:           firstCopy,
			}
			nextPremiseRef++
			nextRelationshipRef++
			secondCopy := dreamInputWithOpaqueEvidence(second, &nextEvidenceRef)
			secondPremise := DreamPathPremise{
				PremiseRef:      fmt.Sprintf("premise_%d", nextPremiseRef),
				RelationshipRef: fmt.Sprintf("relationship_%d", nextRelationshipRef),
				Input:           secondCopy,
			}
			paths = append(paths, DreamPath{
				PathRef:           fmt.Sprintf("path_%d", nextPathRef),
				Subject:           nodeFor(first.SubjectEntityID, first.SubjectName, first.SubjectKind),
				Middle:            nodeFor(first.ObjectEntityID, first.ObjectName, first.ObjectKind),
				Object:            nodeFor(dreamInputObjectID(second), second.ObjectName, second.ObjectKind),
				Premises:          []DreamPathPremise{firstPremise, secondPremise},
				AllowedPredicates: allowed,
			})
			if len(paths) >= dreamPathLimit(maxOutputs) {
				return paths
			}
		}
	}
	return paths
}

func dreamPathLimit(maxOutputs int) int {
	if maxOutputs <= 0 {
		maxOutputs = DefaultMaxOutputs
	}
	limit := maxOutputs * 4
	if limit > maxDreamPathsPerGeneration {
		return maxDreamPathsPerGeneration
	}
	return limit
}

func dreamAllowedPredicates(subjectKind, objectKind string, predicates []dreamcontract.DreamTargetPredicate, refFor func(dreamcontract.DreamTargetPredicate) string) []dreamcontract.DreamTargetPredicate {
	allowed := make([]dreamcontract.DreamTargetPredicate, 0, len(predicates))
	for _, predicate := range predicates {
		if predicate.Version < 1 || strings.TrimSpace(predicate.PredicateKey) == "" ||
			!dreamKindAllowed(predicate.AllowedSubjectKinds, subjectKind) ||
			!dreamKindAllowed(predicate.AllowedObjectKinds, objectKind) {
			continue
		}
		allowed = append(allowed, predicate)
	}
	sort.Slice(allowed, func(i, j int) bool {
		left := strings.ToLower(strings.TrimSpace(allowed[i].PredicateKey))
		right := strings.ToLower(strings.TrimSpace(allowed[j].PredicateKey))
		if left != right {
			return left < right
		}
		return allowed[i].Version < allowed[j].Version
	})
	if len(allowed) > maxDreamAllowedPredicatesPerPath {
		allowed = allowed[:maxDreamAllowedPredicatesPerPath]
	}
	for index := range allowed {
		allowed[index].PredicateRef = refFor(allowed[index])
	}
	return allowed
}

func dreamKindAllowed(allowed []string, kind string) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, candidate := range allowed {
		if strings.EqualFold(strings.TrimSpace(candidate), strings.TrimSpace(kind)) {
			return true
		}
	}
	return false
}

func dreamInputWithOpaqueEvidence(input dreamcontract.DreamInput, nextRef *int) dreamcontract.DreamInput {
	copyInput := input
	copyInput.Evidence = make([]dreamcontract.DreamEvidence, 0, min(len(input.Evidence), 2))
	for index, excerpt := range input.Evidence {
		if index == 2 {
			break
		}
		(*nextRef)++
		excerpt.EvidenceRef = fmt.Sprintf("evidence_%d", *nextRef)
		copyInput.Evidence = append(copyInput.Evidence, excerpt)
	}
	return copyInput
}

func dreamInputObjectID(input dreamcontract.DreamInput) string {
	if input.ObjectEntityID != "" {
		return input.ObjectEntityID
	}
	return input.ObjectValueID
}

func dreamProposalsFromPaths(generated []GeneratedDream, paths []DreamPath, maxOutputs int, generatorModel string) ([]dreamcontract.UpsertHypothesisInput, int) {
	if maxOutputs <= 0 {
		maxOutputs = DefaultMaxOutputs
	}
	byPathRef := make(map[string]DreamPath, len(paths))
	for _, path := range paths {
		byPathRef[path.PathRef] = path
	}
	proposals := make([]dreamcontract.UpsertHypothesisInput, 0, maxOutputs)
	rejected := 0
	for _, generatedDream := range generated {
		path, ok := byPathRef[strings.TrimSpace(generatedDream.PathRef)]
		if !ok || len(path.Premises) != 2 {
			rejected++
			continue
		}
		predicate, ok := dreamPathPredicate(path, generatedDream.PredicateRef)
		if !ok || !dreamPathEvidenceRefsValid(path, generatedDream.EvidenceRefs) {
			rejected++
			continue
		}
		statement := strings.TrimSpace(generatedDream.Hypothesis)
		if statement == "" {
			rejected++
			continue
		}
		proposal := dreamProposalFromPath(path, predicate, generatedDream)
		proposal.GeneratorKind = "provider"
		proposal.GeneratorVersion = firstNonEmpty(strings.TrimSpace(generatorModel), "dream-v3.provider")
		proposal.ContentHash = hypothesisContentHash(proposal)
		proposals = append(proposals, proposal)
		if len(proposals) >= maxOutputs {
			break
		}
	}
	return proposals, rejected
}

func dreamPathPredicate(path DreamPath, predicateRef string) (dreamcontract.DreamTargetPredicate, bool) {
	for _, predicate := range path.AllowedPredicates {
		if predicate.PredicateRef == strings.TrimSpace(predicateRef) {
			return predicate, true
		}
	}
	return dreamcontract.DreamTargetPredicate{}, false
}

func dreamPathEvidenceRefsValid(path DreamPath, refs []string) bool {
	if len(refs) < 2 || len(refs) > 4 {
		return false
	}
	byRef := map[string]int{}
	for premiseIndex, premise := range path.Premises {
		for _, excerpt := range premise.Input.Evidence {
			byRef[excerpt.EvidenceRef] = premiseIndex
		}
	}
	seen := map[string]struct{}{}
	covered := map[int]struct{}{}
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if _, duplicate := seen[ref]; duplicate {
			return false
		}
		seen[ref] = struct{}{}
		premiseIndex, ok := byRef[ref]
		if !ok {
			return false
		}
		covered[premiseIndex] = struct{}{}
	}
	return len(covered) == len(path.Premises)
}

func dreamProposalFromPath(path DreamPath, predicate dreamcontract.DreamTargetPredicate, generated GeneratedDream) dreamcontract.UpsertHypothesisInput {
	first := path.Premises[0].Input
	second := path.Premises[1].Input
	sourceRefs := []map[string]any{
		{"type": dreamSourceType(first), "id": first.RelationshipID},
		{"type": dreamSourceType(second), "id": second.RelationshipID},
	}
	sourceVersions := map[string]int{first.RelationshipID: first.Version, second.RelationshipID: second.Version}
	owners := normalizeDreamSourceOwners([]dreamcontract.DreamInput{first, second})
	return dreamcontract.UpsertHypothesisInput{
		Statement:             strings.TrimSpace(generated.Hypothesis),
		Rationale:             strings.TrimSpace(generated.Rationale),
		Likelihood:            optionalProbability(generated.Likelihood),
		Confidence:            optionalProbability(generated.Confidence),
		SubjectEntityID:       path.Subject.ID,
		PredicateKey:          predicate.PredicateKey,
		PredicateVersion:      predicate.Version,
		ObjectEntityID:        second.ObjectEntityID,
		ObjectValueID:         second.ObjectValueID,
		SourceRefs:            sourceRefs,
		SourceVersions:        sourceVersions,
		SourceOwnerProfileIDs: owners,
		Derivations:           dreamPathDerivations(path, generated.EvidenceRefs),
		Payload: map[string]any{
			"what_if":          strings.TrimSpace(generated.WhatIf),
			"possible_outcome": strings.TrimSpace(generated.PossibleOutcome),
			"source_statuses":  []string{first.Status, second.Status},
		},
	}
}

func dreamPathDerivations(path DreamPath, evidenceRefs []string) []dreamcontract.DreamDerivationSource {
	byRef := map[string]struct {
		premiseIndex int
		input        dreamcontract.DreamInput
		excerpt      dreamcontract.DreamEvidence
	}{}
	for premiseIndex, premise := range path.Premises {
		for _, excerpt := range premise.Input.Evidence {
			byRef[excerpt.EvidenceRef] = struct {
				premiseIndex int
				input        dreamcontract.DreamInput
				excerpt      dreamcontract.DreamEvidence
			}{premiseIndex: premiseIndex, input: premise.Input, excerpt: excerpt}
		}
	}
	derivations := make([]dreamcontract.DreamDerivationSource, 0, len(evidenceRefs))
	for _, evidenceRef := range evidenceRefs {
		selected, ok := byRef[strings.TrimSpace(evidenceRef)]
		if !ok {
			continue
		}
		derivations = append(derivations, dreamcontract.DreamDerivationSource{
			PremisePosition:     selected.premiseIndex + 1,
			RelationshipID:      selected.input.RelationshipID,
			RelationshipVersion: selected.input.Version,
			SupportID:           selected.excerpt.SupportID,
			ObservationID:       selected.excerpt.ObservationID,
			FragmentID:          selected.excerpt.FragmentID,
			SourceID:            selected.excerpt.SourceID,
			SourceRevisionID:    selected.excerpt.SourceRevisionID,
			SourceGroupKey:      selected.excerpt.SourceGroupKey,
			SpanStart:           selected.excerpt.SpanStart,
			SpanEnd:             selected.excerpt.SpanEnd,
			Quote:               selected.excerpt.Content,
			Authority:           selected.excerpt.Authority,
		})
	}
	return derivations
}

func normalizeDreamSourceOwners(inputs []dreamcontract.DreamInput) []string {
	seen := map[string]struct{}{}
	owners := make([]string, 0, len(inputs))
	for _, input := range inputs {
		if input.OwnerProfileID == "" {
			continue
		}
		if _, exists := seen[input.OwnerProfileID]; exists {
			continue
		}
		seen[input.OwnerProfileID] = struct{}{}
		owners = append(owners, input.OwnerProfileID)
	}
	sort.Strings(owners)
	return owners
}

func dreamPathSourceRefs(path DreamPath) []domain.DreamSourceRef {
	return []domain.DreamSourceRef{
		{Type: dreamSourceType(path.Premises[0].Input), ID: path.Premises[0].Input.RelationshipID},
		{Type: dreamSourceType(path.Premises[1].Input), ID: path.Premises[1].Input.RelationshipID},
	}
}

func dreamPathEvaluationInputs(paths []DreamPath) []dreamcontract.DreamPathEvaluationInput {
	inputs := make([]dreamcontract.DreamPathEvaluationInput, 0, len(paths))
	for _, path := range paths {
		if len(path.Premises) != 2 {
			continue
		}
		inputs = append(inputs, dreamcontract.DreamPathEvaluationInput{
			FirstRelationshipID:         path.Premises[0].Input.RelationshipID,
			FirstRelationshipVersion:    path.Premises[0].Input.Version,
			SecondRelationshipID:        path.Premises[1].Input.RelationshipID,
			SecondRelationshipVersion:   path.Premises[1].Input.Version,
			AllowedPredicateFingerprint: dreamAllowedPredicateFingerprint(path.AllowedPredicates),
		})
	}
	return inputs
}

func dreamPathsForEvaluationInputs(paths []DreamPath, allowed []dreamcontract.DreamPathEvaluationInput) []DreamPath {
	allowedByKey := map[string]struct{}{}
	for _, input := range allowed {
		allowedByKey[dreamPathEvaluationKey(input)] = struct{}{}
	}
	filtered := make([]DreamPath, 0, len(paths))
	for _, path := range paths {
		inputs := dreamPathEvaluationInputs([]DreamPath{path})
		if len(inputs) != 1 {
			continue
		}
		if _, ok := allowedByKey[dreamPathEvaluationKey(inputs[0])]; ok {
			filtered = append(filtered, path)
		}
	}
	return filtered
}

func dreamPathEvaluationKey(input dreamcontract.DreamPathEvaluationInput) string {
	return fmt.Sprintf("%s:%d:%s:%d:%s", input.FirstRelationshipID, input.FirstRelationshipVersion, input.SecondRelationshipID, input.SecondRelationshipVersion, input.AllowedPredicateFingerprint)
}

func dreamAllowedPredicateFingerprint(predicates []dreamcontract.DreamTargetPredicate) string {
	keys := make(map[string]struct{}, len(predicates))
	for _, predicate := range predicates {
		key := strings.ToLower(strings.TrimSpace(predicate.PredicateKey))
		if key == "" || predicate.Version < 1 {
			continue
		}
		keys[fmt.Sprintf("%s\x00%d", key, predicate.Version)] = struct{}{}
	}
	ordered := make([]string, 0, len(keys))
	for key := range keys {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)
	sum := sha256.Sum256([]byte(strings.Join(ordered, "\x00")))
	return hex.EncodeToString(sum[:])
}

func dreamTargetCandidates(paths []DreamPath) []dreamcontract.DreamTargetCandidate {
	candidates := []dreamcontract.DreamTargetCandidate{}
	for _, path := range paths {
		if len(path.Premises) != 2 {
			continue
		}
		object := path.Premises[1].Input
		for _, predicate := range path.AllowedPredicates {
			candidates = append(candidates, dreamcontract.DreamTargetCandidate{
				PathRef:         path.PathRef,
				PredicateRef:    predicate.PredicateRef,
				SubjectEntityID: path.Subject.ID,
				PredicateKey:    predicate.PredicateKey,
				ObjectEntityID:  object.ObjectEntityID,
				ObjectValueID:   object.ObjectValueID,
			})
		}
	}
	return candidates
}

func dreamPathsForAvailableTargets(paths []DreamPath, available []dreamcontract.DreamTargetCandidate) []DreamPath {
	allowed := map[string]struct{}{}
	for _, target := range available {
		allowed[target.PathRef+"\x00"+target.PredicateRef] = struct{}{}
	}
	filtered := make([]DreamPath, 0, len(paths))
	for _, path := range paths {
		copyPath := path
		copyPath.AllowedPredicates = make([]dreamcontract.DreamTargetPredicate, 0, len(path.AllowedPredicates))
		for _, predicate := range path.AllowedPredicates {
			if _, ok := allowed[path.PathRef+"\x00"+predicate.PredicateRef]; ok {
				copyPath.AllowedPredicates = append(copyPath.AllowedPredicates, predicate)
			}
		}
		if len(copyPath.AllowedPredicates) > 0 {
			filtered = append(filtered, copyPath)
		}
	}
	return filtered
}
