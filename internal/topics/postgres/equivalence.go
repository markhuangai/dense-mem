package postgres

import (
	"context"
	"slices"

	ontology "github.com/markhuangai/dense-mem/internal/topics/contract"
	"gorm.io/gorm"
)

func (s *Store) ReadEquivalenceNeighborhoods(ctx context.Context, claim ontology.MaintenanceClaim) ([]ontology.EquivalenceNeighborhood, error) {
	if err := requireAutomatic(ctx); err != nil {
		return nil, err
	}
	if _, err := ontology.PrepareOrganizationInput(ontology.OrganizationInput{OperationKey: "equivalence", Sources: claim.Sources}); err != nil {
		return nil, err
	}
	var result []ontology.EquivalenceNeighborhood
	err := s.withScope(ctx, claim.TeamID, true, func(tx *gorm.DB, fence scope) error {
		if fence.SpaceID != claim.SpaceID || fence.Generation != claim.Generation {
			return ontology.ErrLeaseLost
		}
		seeds, err := organizationSources(tx, fence, claim.Sources)
		if err != nil {
			return err
		}
		sources := make(map[ontology.SourceHandle]ontology.SourceSnapshot, len(seeds))
		for _, seed := range seeds {
			sources[seed.SourceHandle] = seed
		}
		for _, seed := range seeds {
			if !seed.Eligible {
				continue
			}
			related, err := maintenanceRelatedSources(tx, fence, seed)
			if err != nil {
				return err
			}
			if len(related) == 0 {
				continue
			}
			var missing []ontology.SourceHandle
			for _, handle := range related {
				if _, ok := sources[handle]; !ok {
					missing = append(missing, handle)
				}
			}
			if len(missing) > 0 {
				peers, err := organizationSources(tx, fence, missing)
				if err != nil {
					return err
				}
				for _, peer := range peers {
					sources[peer.SourceHandle] = peer
				}
			}
			selected := []ontology.SourceHandle{seed.SourceHandle}
			for _, handle := range related {
				if ontology.CompatibleMeaningContext(seed, sources[handle]) {
					selected = append(selected, handle)
				}
			}
			if slices.ContainsFunc(result, func(previous ontology.EquivalenceNeighborhood) bool {
				for _, handle := range selected {
					if !slices.Contains(previous.Sources, handle) {
						return false
					}
				}
				return true
			}) {
				continue
			}
			for len(selected) > 1 {
				members, exceeded, err := s.maintenanceGroupClosure(tx, fence, selected)
				if err != nil {
					return err
				}
				if exceeded {
					selected = selected[:len(selected)-1]
					continue
				}
				duplicate := slices.ContainsFunc(result, func(previous ontology.EquivalenceNeighborhood) bool {
					return slices.Equal(previous.Sources, members)
				})
				if !duplicate {
					result = append(result, ontology.EquivalenceNeighborhood{Seed: seed.SourceHandle, Sources: members})
				}
				break
			}
		}
		return nil
	})
	return result, err
}
