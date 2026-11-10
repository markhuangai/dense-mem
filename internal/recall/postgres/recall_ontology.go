package postgres

import (
	"context"
	"errors"
	"fmt"
	"github.com/markhuangai/dense-mem/internal/observability"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
	"log/slog"
	"sort"

	"github.com/markhuangai/dense-mem/internal/domain"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	recallcontract "github.com/markhuangai/dense-mem/internal/recall/contract"
	"gorm.io/gorm"
)

type ontologyReadError struct{ cause error }

func (e *ontologyReadError) Error() string { return "recall: optional ontology read failed" }
func (e *ontologyReadError) Unwrap() error { return e.cause }

func ontologyFailureCode(err error) string {
	if errors.Is(err, ontology.ErrContextBound) {
		return "ontology_bound_exceeded"
	}
	return "ontology_unavailable"
}

func (r *Store) WithEvidenceSnapshot(ctx context.Context, teamID string, fn func(recallcontract.SearchRepository) error) error {
	if r == nil || r.db == nil || r.rls == nil {
		return errors.New("recall: database and RLS helper are required")
	}
	read := func(degradation string) error {
		return r.rls.WithTeamReadOnlyRepeatableTx(ctx, r.db, teamID, func(tx *gorm.DB) error {
			bound := *r
			bound.snapshot, bound.snapshotTeam, bound.organizationFailure = tx, teamID, degradation
			if r.ontology != nil {
				reader := r.ontology()
				bound.ontology = func() OntologyReader { return reader }
			}
			return fn(&bound)
		})
	}
	err := read("")
	var optional *ontologyReadError
	if errors.As(err, &optional) && ctx.Err() == nil {
		return read(ontologyFailureCode(optional.cause))
	}
	return err
}

func (r *Store) withRecallTx(ctx context.Context, teamID string, organized bool, fn func(*gorm.DB) error) error {
	if !organized || r.snapshot != nil {
		return r.withTeamTx(ctx, teamID, fn)
	}
	if r == nil || r.db == nil || r.rls == nil {
		return errors.New("recall: database and RLS helper are required")
	}
	return r.rls.WithTeamReadOnlyRepeatableTx(ctx, r.db, teamID, fn)
}

func (r *Store) readOrganization(ctx context.Context, tx *gorm.DB, input RecallEvidenceInput, ids, candidates []string, limit int) (ontology.RecallOrganization, error) {
	if !input.OrganizationEnabled || (input.SpaceKind != "" && input.SpaceKind != string(domain.MemorySpaceTeamShared)) {
		return ontology.RecallOrganization{}, nil
	}
	if r.organizationFailure != "" {
		return ontology.RecallOrganization{Degradation: r.organizationFailure}, nil
	}
	if input.ValidAt != nil || input.KnownAt != nil {
		return ontology.RecallOrganization{Degradation: "ontology_temporal_not_supported"}, nil
	}
	if r.ontology == nil {
		return ontology.RecallOrganization{Degradation: "ontology_unavailable"}, nil
	}
	result, err := r.ontology()(ctx, tx, input.TeamID, ontology.RecallReadInput{SpaceID: input.SpaceID, Query: input.Query, EvidenceIDs: ids, CandidateEvidenceIDs: candidates, Limit: limit})
	if err != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		return result, &ontologyReadError{cause: err}
	}
	return result, nil
}

func (r *Store) HydrateOrganizedEvidence(ctx context.Context, input RecallEvidenceInput, contract *ActiveSearchContract, ids []string) (*recallcontract.EvidenceHydration, error) {
	result := &recallcontract.EvidenceHydration{}
	err := r.withRecallTx(ctx, input.TeamID, input.OrganizationEnabled, func(tx *gorm.DB) error {
		if err := r.resolveTopicFilter(ctx, tx, &input); err != nil {
			return err
		}
		requested := domain.NormalizeReadIDList(append(append([]string(nil), ids...), input.KnownEvidenceIDs...))
		organization, err := r.readOrganization(ctx, tx, input, requested, nil, recallcontract.MaxRecallCandidateCount)
		if err != nil {
			return err
		}
		result.Groups, result.OrganizationDegradation = organization.Groups, organization.Degradation
		for _, group := range organization.Groups {
			requested = append(requested, group.Members...)
		}
		requested = domain.NormalizeReadIDList(requested)
		if len(requested) > ontology.MaxDependencyRecords {
			return &ontologyReadError{cause: ontology.ErrContextBound}
		}
		result.Hits, err = hydrateRecallEvidence(ctx, tx, input, contract, requested)
		return err
	})
	var optional *ontologyReadError
	if errors.As(err, &optional) && r.snapshot == nil {
		input.OrganizationEnabled = false
		hits, requiredErr := r.HydrateEvidence(ctx, input, contract, ids)
		return &recallcontract.EvidenceHydration{Hits: hits, OrganizationDegradation: ontologyFailureCode(optional.cause)}, requiredErr
	}
	return result, err
}

func mergeRecallExpansion(existing, additional []SearchHit, limit int) []SearchHit {
	result := make([]SearchHit, 0, limit)
	seen := map[string]bool{}
	for _, hits := range [][]SearchHit{existing, additional} {
		for _, hit := range hits {
			if !seen[hit.SourceID] && len(result) < limit {
				seen[hit.SourceID] = true
				result = append(result, hit)
			}
		}
	}
	return result
}

func organizationSourceIDs(sources []ontology.SourceHandle, kind ontology.SourceKind) []string {
	ids := []string{}
	for _, source := range sources {
		if source.Kind == kind {
			ids = append(ids, source.ID)
		}
	}
	return domain.NormalizeReadIDList(ids)
}

func (r *Store) organizationEvidenceCandidates(ctx context.Context, tx *gorm.DB, input RecallEvidenceInput, contract *ActiveSearchContract, organization ontology.RecallOrganization, limit int) ([]SearchHit, error) {
	if len(organization.Sources) == 0 {
		return nil, nil
	}
	var hits []SearchHit
	ids := organizationSourceIDs(organization.Sources, ontology.EvidenceSource)
	states := map[string]string{}
	for _, hit := range hits {
		ids = append(ids, hit.SourceID)
		states[hit.SourceID] = hit.SearchState
	}
	ids = domain.NormalizeReadIDList(ids)
	if len(ids) == 0 {
		return nil, nil
	}
	additional := []SearchHit{}
	for _, id := range ids {
		additional = append(additional, SearchHit{TeamID: input.TeamID, SourceKind: "evidence", SourceID: id, SearchState: states[id]})
	}
	sort.Slice(additional, func(i, j int) bool { return additional[i].SourceID < additional[j].SourceID })
	return mergeRecallExpansion(nil, additional, limit), nil
}

func (r *Store) resolveTopicFilter(ctx context.Context, tx *gorm.DB, input *RecallEvidenceInput) (err error) {
	if input.TopicIDs == nil {
		return nil
	}
	var selection ontology.TopicReadResult
	defer func() {
		if r.topicLogger == nil {
			return
		}
		attrs := []observability.LogAttr{
			observability.String("team_id", input.TeamID), observability.String("space_id", input.SpaceID),
			{Key: "requested_ids", Value: input.TopicIDs}, {Key: "generation", Value: selection.Generation},
			{Key: "memberships", Value: selection.Topics}, {Key: "rejected", Value: selection.Rejected},
			{Key: "known_exclusions", Value: input.KnownEvidenceIDs}, {Key: "organization_enabled", Value: input.OrganizationEnabled},
		}
		level, event := slog.LevelDebug, "recall_topic_selection"
		if err != nil {
			level, event = slog.LevelWarn, "recall_topic_degraded"
			attrs = append(attrs, observability.String("reason", err.Error()), observability.String("outcome", "unavailable"))
		} else {
			attrs = append(attrs, observability.String("outcome", "filter_selected"))
		}
		if logger, ok := r.topicLogger.(observability.ContextLogProvider); ok {
			logger.LogContext(ctx, level, event, attrs...)
		} else if err != nil {
			r.topicLogger.Warn(event, attrs...)
		} else {
			r.topicLogger.Debug(event, attrs...)
		}
	}()
	if !input.OrganizationEnabled || r.topics == nil || (input.SpaceKind != "" && input.SpaceKind != string(domain.MemorySpaceTeamShared)) {
		return recallcontract.ErrTopicUnavailable
	}
	selection, err = r.topics(ctx, tx, input.TeamID, ontology.TopicReadInput{SpaceID: input.SpaceID, TopicIDs: input.TopicIDs})
	if err != nil {
		return fmt.Errorf("%w: %w", recallcontract.ErrTopicUnavailable, err)
	}
	if len(selection.Topics) != len(input.TopicIDs) {
		return recallcontract.ErrTopicUnavailable
	}
	input.TopicEvidenceIDs = []string{}
	for _, topic := range selection.Topics {
		input.TopicEvidenceIDs = append(input.TopicEvidenceIDs, topic.EvidenceIDs...)
	}
	input.TopicEvidenceIDs = domain.NormalizeReadIDList(input.TopicEvidenceIDs)
	return nil
}

func (r *Store) topicEvidenceCandidates(ctx context.Context, tx *gorm.DB, input RecallEvidenceInput, contract *ActiveSearchContract, limit int) ([]SearchHit, error) {
	filter, args := recallEvidenceEligibility(input, "fragment", true)
	parameters := []any{input.TeamID, contract.EmbeddingContractID, recallEventAt(input.ValidAt, input.KnownAt)}
	parameters = append(parameters, args...)
	parameters = append(parameters, limit)
	rows, err := tx.WithContext(ctx).Raw(`SELECT document.team_id::text,document.search_document_id::text,
        document.source_kind,document.source_id::text,document.source_version,document.document_version,
        document.embedding_contract_id::text,document.search_state,0::double precision,0::double precision
        FROM search_documents AS document JOIN evidence_fragments AS fragment
          ON fragment.team_id=document.team_id AND fragment.fragment_id=document.source_id
        WHERE document.team_id=?::uuid AND document.source_kind='evidence' AND document.embedding_contract_id=?::uuid
          AND (document.search_state IN ('pending','current','failed') OR (?::timestamptz IS NOT NULL AND document.search_state='not_required'))
        `+filter+` ORDER BY document.source_id LIMIT ?`, parameters...).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSearchHits(rows)
}

func (r *Store) RelatedEvidenceTopics(ctx context.Context, input RecallEvidenceInput, returned []string, limit int) (recallcontract.RelatedTopicResult, error) {
	result := recallcontract.RelatedTopicResult{Topics: []recallcontract.RelatedTopic{}, Diagnostics: map[string]any{}}
	if limit < 0 || limit > 10 {
		return result, recallcontract.ErrTopicUnavailable
	}
	if !input.OrganizationEnabled || r.topics == nil {
		return result, recallcontract.ErrTopicUnavailable
	}
	err := r.withRecallTx(ctx, input.TeamID, true, func(tx *gorm.DB) error {
		if err := r.resolveTopicFilter(ctx, tx, &input); err != nil {
			return err
		}
		var err error
		input.KnownEvidenceIDs, err = storagepostgres.ResolveMemoryHandlesTx(ctx, tx, input.TeamID, "evidence_fragments", input.KnownEvidenceIDs, input.SpaceID)
		if err != nil {
			return err
		}
		selection, err := r.topics(ctx, tx, input.TeamID, ontology.TopicReadInput{SpaceID: input.SpaceID, EvidenceIDs: returned})
		result.Diagnostics = map[string]any{"space_id": selection.SpaceID, "generation": selection.Generation, "rejected": selection.Rejected, "memberships": selection.Topics}
		if err != nil {
			return err
		}
		ids := []string{}
		for _, topic := range selection.Topics {
			ids = append(ids, topic.EvidenceIDs...)
		}
		ids = domain.NormalizeReadIDList(ids)
		if len(ids) > ontology.MaxDependencyRecords {
			return ontology.ErrContextBound
		}
		contract, err := r.GetActiveSearchContract(ctx)
		if err != nil {
			return err
		}
		countInput := input
		countInput.TopicIDs, countInput.TopicEvidenceIDs = nil, nil
		eligible, err := hydrateRecallEvidence(ctx, tx, countInput, contract, ids)
		if err != nil {
			return err
		}
		known, seen := map[string]bool{}, map[string]bool{}
		for _, id := range input.KnownEvidenceIDs {
			known[id] = true
		}
		for _, id := range returned {
			seen[id] = true
		}
		for _, topic := range selection.Topics {
			counted := map[string]bool{}
			hint := recallcontract.RelatedTopic{TopicID: topic.ID, Name: topic.Name, Description: topic.Description}
			for _, id := range topic.EvidenceIDs {
				if counted[id] || known[id] {
					continue
				}
				counted[id] = true
				if _, ok := eligible[id]; !ok {
					continue
				}
				hint.EligibleEvidenceCount++
				if seen[id] {
					hint.MatchingResultCount++
				} else {
					hint.AdditionalEvidenceCount++
				}
			}
			if hint.MatchingResultCount > 0 {
				result.Topics = append(result.Topics, hint)
			}
		}
		sort.Slice(result.Topics, func(i, j int) bool {
			left, right := result.Topics[i], result.Topics[j]
			if left.MatchingResultCount != right.MatchingResultCount {
				return left.MatchingResultCount > right.MatchingResultCount
			}
			if left.AdditionalEvidenceCount != right.AdditionalEvidenceCount {
				return left.AdditionalEvidenceCount > right.AdditionalEvidenceCount
			}
			return left.TopicID < right.TopicID
		})
		result.Truncated = len(result.Topics) > limit
		if result.Truncated {
			result.Topics = result.Topics[:limit]
		}
		for index := range result.Topics {
			result.Topics[index].Rank = index + 1
		}
		result.Diagnostics["eligible_ids"] = sortedRecallEvidenceIDs(eligible)
		result.Diagnostics["known_exclusions"] = input.KnownEvidenceIDs
		return nil
	})
	return result, err
}

func sortedRecallEvidenceIDs(hits map[string]RecallEvidenceHit) []string {
	ids := make([]string, 0, len(hits))
	for id := range hits {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
