package contract

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/google/uuid"
)

const (
	MaxOrganizationSources    = 20
	MaxVocabularyCandidates   = 20
	OrganizationPolicyVersion = "ontology-organization-v1"
)

type OrganizationInput struct {
	OperationKey string         `json:"operation_key"`
	Sources      []SourceHandle `json:"sources"`
}

type OrganizationContext struct {
	Revision   int64
	Sources    []SourceSnapshot
	Records    []RecordView
	Candidates []RecordView
}

type OrganizationOutcome struct {
	Source    SourceHandle `json:"source"`
	Status    string       `json:"status"`
	Reason    string       `json:"reason,omitempty"`
	RecordIDs []string     `json:"record_ids,omitempty"`
}

type AmbiguousComparison struct {
	Left   SourceHandle `json:"left"`
	Right  SourceHandle `json:"right"`
	Reason string       `json:"reason,omitempty"`
}

type AssessmentAttempt struct {
	Number                 int    `json:"number"`
	EstimatedInputTokens   int    `json:"estimated_input_tokens"`
	EstimatedOutputTokens  int    `json:"estimated_output_tokens"`
	ReportedInputTokens    int    `json:"reported_input_tokens"`
	ReportedOutputTokens   int    `json:"reported_output_tokens"`
	ReportedTotalTokens    int    `json:"reported_total_tokens"`
	ReportedUsageAvailable bool   `json:"reported_usage_available"`
	FailureCode            string `json:"failure_code,omitempty"`
}

type OrganizationResult struct {
	AssessmentID         string                `json:"assessment_id"`
	Existing             bool                  `json:"existing"`
	Current              bool                  `json:"current"`
	Outcomes             []OrganizationOutcome `json:"outcomes"`
	Attempts             []AssessmentAttempt   `json:"attempts"`
	Publication          *PublicationResult    `json:"publication,omitempty"`
	FailureCode          string                `json:"failure_code,omitempty"`
	AmbiguousComparisons []AmbiguousComparison `json:"ambiguous_comparisons,omitempty"`
}

type OrganizationReceipt struct {
	ID               string             `json:"id"`
	OperationKey     string             `json:"operation_key"`
	InputHash        string             `json:"input_hash"`
	BatchHash        string             `json:"batch_hash"`
	ProviderIdentity string             `json:"provider_identity"`
	ContextHash      string             `json:"context_hash"`
	Sources          []SourceDependency `json:"sources"`
	Dependencies     []RevisionRef      `json:"dependencies"`
	Result           OrganizationResult `json:"result"`
}

type OrganizationRepository interface {
	FindOrganization(context.Context, string, OrganizationInput, string) (OrganizationResult, bool, error)
	ReadOrganization(context.Context, string, []SourceHandle) (OrganizationContext, error)
	GetRecord(context.Context, string, string, int64) (RecordView, error)
	CommitOrganization(context.Context, string, OrganizationReceipt, Publication) (OrganizationResult, error)
}

func PrepareOrganizationInput(input OrganizationInput) (OrganizationInput, error) {
	if !boundedText(input.OperationKey, 128) || len(input.Sources) < 1 || len(input.Sources) > MaxOrganizationSources {
		return input, fmt.Errorf("%w: organization operation key and 1..20 sources required", ErrInvalid)
	}
	input.Sources = sortedSources(input.Sources)
	seen := map[string]bool{}
	for _, source := range input.Sources {
		if err := ValidateSourceHandle(source); err != nil {
			return input, err
		}
		if seen[SourceKey(source)] {
			return input, fmt.Errorf("%w: duplicate organization source", ErrInvalid)
		}
		seen[SourceKey(source)] = true
	}
	return input, nil
}

func OrganizationInputHash(input OrganizationInput, providerIdentity string) (string, error) {
	return hashJSON(struct {
		Policy, Provider string
		Sources          []SourceHandle
	}{OrganizationPolicyVersion, providerIdentity, sortedSources(input.Sources)})
}

func OrganizationBatchHash(sources []SourceDependency, providerIdentity string) (string, error) {
	ordered := append([]SourceDependency(nil), sources...)
	sort.Slice(ordered, func(i, j int) bool { return SourceKey(ordered[i].SourceHandle) < SourceKey(ordered[j].SourceHandle) })
	return hashJSON(struct {
		Policy, Provider string
		Sources          []SourceDependency
	}{OrganizationPolicyVersion, providerIdentity, ordered})
}

func OrganizationRecordID(teamID string, kind Kind, key string) string {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("dense-mem:organization:"+teamID+":"+string(kind)+":"+key)).String()
}

func OrganizationContextHash(context OrganizationContext) (string, error) {
	type revision struct {
		ID      string
		Version int64
		Current bool
	}
	refs := func(views []RecordView) []revision {
		result := make([]revision, 0, len(views))
		for _, view := range views {
			result = append(result, revision{view.ID, view.Version, view.Current})
		}
		sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
		return result
	}
	return hashJSON(struct{ Records, Candidates []revision }{refs(context.Records), refs(context.Candidates)})
}

func VocabularyQuery(sources []SourceSnapshot) (string, []string) {
	seen := map[string]bool{}
	var words []string
	for _, source := range sources {
		for _, text := range []string{source.ID, source.EntityKind, source.State["content"], source.State["names"], source.State["predicate"]} {
			for _, word := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) }) {
				if len(words) < 64 && len(word) <= 128 && !seen[word] {
					words = append(words, word)
					seen[word] = true
				}
			}
		}
	}
	return strings.Join(words, " "), words
}

func ValidateOrganizationReceipt(receipt OrganizationReceipt) error {
	if !validID(receipt.ID) || receipt.Result.AssessmentID != receipt.ID || !boundedText(receipt.OperationKey, 128) ||
		!validFingerprint(receipt.InputHash) || !validFingerprint(receipt.BatchHash) || !validFingerprint(receipt.ContextHash) || !boundedText(receipt.ProviderIdentity, 128) ||
		len(receipt.Sources) < 1 || len(receipt.Sources) > MaxOrganizationSources || len(receipt.Dependencies) > MaxDependencyRecords ||
		len(receipt.Result.Outcomes) != len(receipt.Sources) || len(receipt.Result.Attempts) > 3 || len(receipt.Result.AmbiguousComparisons) > 190 {
		return fmt.Errorf("%w: organization receipt shape", ErrInvalid)
	}
	seen := map[string]bool{}
	for _, source := range receipt.Sources {
		if err := ValidateSourceHandle(source.SourceHandle); err != nil {
			return err
		}
		if !validFingerprint(source.Fingerprint) || seen[SourceKey(source.SourceHandle)] {
			return ErrInvalid
		}
		seen[SourceKey(source.SourceHandle)] = true
	}
	pairs := map[string]bool{}
	for _, comparison := range receipt.Result.AmbiguousComparisons {
		left, right := SourceKey(comparison.Left), SourceKey(comparison.Right)
		if !seen[left] || !seen[right] || left >= right || len(comparison.Reason) > 512 || pairs[left+"|"+right] {
			return ErrInvalid
		}
		pairs[left+"|"+right] = true
		for _, source := range receipt.Sources {
			if (SourceKey(source.SourceHandle) == left && source.SourceHandle != comparison.Left) || (SourceKey(source.SourceHandle) == right && source.SourceHandle != comparison.Right) {
				return ErrInvalid
			}
		}
	}
	for _, outcome := range receipt.Result.Outcomes {
		key := SourceKey(outcome.Source)
		if !seen[key] {
			return ErrInvalid
		}
		delete(seen, key)
		switch outcome.Status {
		case "organized", "unchanged", "ambiguous", "oversized", "unavailable", "failed":
		default:
			return ErrInvalid
		}
		if len(outcome.Reason) > 128 || len(outcome.RecordIDs) > MaxChanges {
			return ErrInvalid
		}
		for _, id := range outcome.RecordIDs {
			if !validID(id) {
				return ErrInvalid
			}
		}
	}
	for i, attempt := range receipt.Result.Attempts {
		if attempt.Number != i+1 || attempt.EstimatedInputTokens < 0 || attempt.EstimatedOutputTokens < 0 || attempt.ReportedInputTokens < 0 || attempt.ReportedOutputTokens < 0 || attempt.ReportedTotalTokens < 0 || len(attempt.FailureCode) > 128 {
			return ErrInvalid
		}
	}
	for _, dependency := range receipt.Dependencies {
		if !validID(dependency.ID) || dependency.Version < 1 {
			return ErrInvalid
		}
	}
	return nil
}
