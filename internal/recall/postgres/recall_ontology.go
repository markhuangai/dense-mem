package postgres

import (
	"context"
	"errors"
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

func hasOrganizationGraphSources(sources []ontology.SourceHandle) bool {
	for _, source := range sources {
		if source.Kind == ontology.EntitySource || source.Kind == ontology.PredicateSource || source.Kind == ontology.RelationshipSource {
			return true
		}
	}
	return false
}

func (r *Store) organizationEvidenceCandidates(ctx context.Context, tx *gorm.DB, input RecallEvidenceInput, contract *ActiveSearchContract, organization ontology.RecallOrganization, limit int) ([]SearchHit, error) {
	if len(organization.Sources) == 0 {
		return nil, nil
	}
	expanded := input
	expanded.ExpandFromEntityIDs = organizationSourceIDs(organization.Sources, ontology.EntitySource)
	expanded.OrganizationSources = organization.Sources
	var hits []SearchHit
	if hasOrganizationGraphSources(organization.Sources) {
		var err error
		hits, err = searchRecallEntityExpansion(ctx, tx, expanded, contract, limit)
		if err != nil {
			return nil, err
		}
	}
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

func (r *Store) organizationRelationshipCandidates(ctx context.Context, tx *gorm.DB, input RecallRelationshipsInput, contract *ActiveSearchContract, organization ontology.RecallOrganization, limit int) ([]SearchHit, error) {
	if len(organization.Sources) == 0 {
		return nil, nil
	}
	expanded := input
	expanded.ExpandFromEntityIDs = organizationSourceIDs(organization.Sources, ontology.EntitySource)
	expanded.OrganizationSources = organization.Sources
	var hits []SearchHit
	if hasOrganizationGraphSources(organization.Sources) {
		var err error
		hits, err = searchRecallRelationshipEntityExpansion(ctx, tx, expanded, contract, limit)
		if err != nil {
			return nil, err
		}
	}
	ids := organizationSourceIDs(organization.Sources, ontology.RelationshipSource)
	for _, hit := range hits {
		ids = append(ids, hit.SourceID)
	}
	if len(ids) == 0 {
		return nil, nil
	}
	hydrated, err := hydrateRecallRelationships(ctx, tx, input, contract, domain.NormalizeReadIDList(ids))
	if err != nil {
		return nil, err
	}
	additional := []SearchHit{}
	for id, hit := range hydrated {
		additional = append(additional, SearchHit{TeamID: input.TeamID, SourceKind: "relationship", SourceID: id, SearchState: hit.SearchState})
	}
	sort.Slice(additional, func(i, j int) bool { return additional[i].SourceID < additional[j].SourceID })
	return mergeRecallExpansion(nil, additional, limit), nil
}
