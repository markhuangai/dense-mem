package service

import (
	"context"
	"errors"
	"sort"
	"time"

	community "github.com/markhuangai/dense-mem/internal/community/contract"
	"github.com/markhuangai/dense-mem/internal/domain"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
)

type ontologyConfig interface {
	OntologyMaintenanceRuntimeConfig(context.Context) (domain.OntologyMaintenanceConfig, error)
}

func (s *service) ontologyEnabled(ctx context.Context) (bool, error) {
	config, ok := s.config.(ontologyConfig)
	if !ok {
		return false, nil
	}
	policy, err := config.OntologyMaintenanceRuntimeConfig(ctx)
	return policy.Enabled, err
}

func (s *service) RunProjectionTurn(ctx context.Context) (bool, error) {
	store, ok := s.store.(community.TopicProjectionRepository)
	if !ok {
		return false, errors.New("community: topic projection repository is required")
	}
	work, err := store.ClaimTopicProjection(ctx, s.now())
	if work == nil {
		return false, err
	}
	if err == nil {
		sort.Slice(work.Inputs, func(i, j int) bool { return work.Inputs[i].RelationshipID < work.Inputs[j].RelationshipID })
		_, members := topEntitiesAndMemberships(work.Inputs)
		batch := community.TopicProjectionBatch{Work: *work, Memberships: members}
		for _, input := range work.Inputs {
			batch.Sources = append(batch.Sources, CommunitySourceInput{RelationshipID: input.RelationshipID, OwnerProfileID: input.OwnerProfileID,
				RelationshipVersion: input.Version, SemanticGroupKey: input.SemanticGroupKey, SourceStateHash: sourceStateHash(input)})
		}
		err = store.AppendTopicProjection(ctx, batch, s.now())
	}
	if err == nil {
		return true, nil
	}
	code := "projection_failed"
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ontology.ErrMaintenanceDisabled) || errors.Is(err, ontology.ErrMaintenancePaused) {
		code = "interrupted"
	}
	if errors.Is(err, community.ErrCommunitySourceStale) || errors.Is(err, ontology.ErrSourceStale) {
		code = "source_changed"
	}
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return true, errors.Join(err, store.FailTopicProjection(finishCtx, *work, code, s.now()))
}
