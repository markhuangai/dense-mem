package service

import (
	"context"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service/graphrow"
)

type RecallFeedbackScopedReader interface {
	ScopedRead(ctx context.Context, profileID string, query string, params map[string]any) (any, []map[string]any, error)
}

type RecallFeedbackGraphResolver struct {
	reader RecallFeedbackScopedReader
}

var _ RecallFeedbackResultResolver = (*RecallFeedbackGraphResolver)(nil)

func NewRecallFeedbackGraphResolver(reader RecallFeedbackScopedReader) *RecallFeedbackGraphResolver {
	return &RecallFeedbackGraphResolver{reader: reader}
}

func (r *RecallFeedbackGraphResolver) ResolveRecallFeedbackResults(ctx context.Context, profileID string, refs []domain.RecallFeedbackResultRef) ([]domain.RecallFeedbackResolvedResult, error) {
	out := make([]domain.RecallFeedbackResolvedResult, 0, len(refs))
	if r == nil || r.reader == nil || profileID == "" || len(refs) == 0 {
		for _, ref := range refs {
			out = append(out, missingRecallFeedbackResult(ref))
		}
		return out, nil
	}
	grouped := recallFeedbackIDsByType(refs)
	current := map[string]map[string]map[string]any{}
	for resultType, ids := range grouped {
		rows, err := r.fetchCurrent(ctx, profileID, resultType, ids)
		if err != nil {
			return nil, err
		}
		current[resultType] = rows
	}
	for _, ref := range refs {
		byType := current[ref.Type]
		row := byType[ref.ID]
		if row == nil {
			out = append(out, missingRecallFeedbackResult(ref))
			continue
		}
		status := graphrow.String(row, "status")
		out = append(out, domain.RecallFeedbackResolvedResult{
			Type:             ref.Type,
			ID:               ref.ID,
			Rank:             ref.Rank,
			ResolutionStatus: "found",
			CurrentStatus:    status,
			Current:          row,
			Ref:              ref,
		})
	}
	return out, nil
}

func (r *RecallFeedbackGraphResolver) fetchCurrent(ctx context.Context, profileID, resultType string, ids []string) (map[string]map[string]any, error) {
	if len(ids) == 0 {
		return map[string]map[string]any{}, nil
	}
	query := ""
	switch resultType {
	case domain.RecallFeedbackResultTypeFragment:
		query = `
			MATCH (sf:SourceFragment {team_id: $profileId})
			WHERE sf.fragment_id IN $ids
			RETURN sf.fragment_id AS id,
			       'fragment' AS type,
			       coalesce(sf.status, 'active') AS status,
			       sf.content AS content,
			       sf.source_type AS source_type,
			       sf.source AS source,
			       sf.created_at AS created_at,
			       sf.updated_at AS updated_at,
			       sf.recorded_to AS recorded_to,
			       sf.retracted_at AS retracted_at
		`
	case domain.RecallFeedbackResultTypeClaim:
		query = `
			MATCH (c:Claim {team_id: $profileId})
			WHERE c.claim_id IN $ids
			RETURN c.claim_id AS id,
			       'claim' AS type,
			       c.status AS status,
			       c.subject AS subject,
			       c.predicate AS predicate,
			       c.object AS object,
			       c.entailment_verdict AS entailment_verdict,
			       c.recorded_at AS recorded_at,
			       c.recorded_to AS recorded_to,
			       c.valid_from AS valid_from,
			       c.valid_to AS valid_to
		`
	case domain.RecallFeedbackResultTypeFact:
		query = `
			MATCH (f:Fact {team_id: $profileId})
			WHERE f.fact_id IN $ids
			RETURN f.fact_id AS id,
			       'fact' AS type,
			       f.status AS status,
			       f.subject AS subject,
			       f.predicate AS predicate,
			       f.object AS object,
			       f.authority_state AS authority_state,
			       f.recorded_at AS recorded_at,
			       f.recorded_to AS recorded_to,
			       f.valid_from AS valid_from,
			       f.valid_to AS valid_to,
			       f.retracted_at AS retracted_at
		`
	default:
		return map[string]map[string]any{}, nil
	}
	_, rows, err := r.reader.ScopedRead(ctx, profileID, query, map[string]any{"ids": ids})
	if err != nil {
		return nil, err
	}
	out := make(map[string]map[string]any, len(rows))
	for _, row := range rows {
		id := graphrow.String(row, "id")
		if id != "" {
			out[id] = row
		}
	}
	return out, nil
}

func recallFeedbackIDsByType(refs []domain.RecallFeedbackResultRef) map[string][]string {
	out := map[string][]string{}
	seen := map[string]struct{}{}
	for _, ref := range refs {
		if ref.Type == "" || ref.ID == "" {
			continue
		}
		key := ref.Type + "\x00" + ref.ID
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out[ref.Type] = append(out[ref.Type], ref.ID)
	}
	return out
}

func missingRecallFeedbackResult(ref domain.RecallFeedbackResultRef) domain.RecallFeedbackResolvedResult {
	return domain.RecallFeedbackResolvedResult{
		Type:             ref.Type,
		ID:               ref.ID,
		Rank:             ref.Rank,
		ResolutionStatus: "missing",
		Ref:              ref,
	}
}
