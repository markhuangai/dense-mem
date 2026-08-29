package e2eapp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/cmd/internal/serverapp"
	"github.com/markhuangai/dense-mem/internal/repository"
	rememberapp "github.com/markhuangai/dense-mem/internal/service/remember"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
)

const synchronousSearchGenerationFault = "search-generation-rotation"
const synchronousSupersessionFault = "supersession-rotation"

// synchronousRememberBeforeCommitHook is installed only by the disposable
// remember E2E composition. It rotates the active generation after provider
// success so the public case exercises the late commit fence.
func synchronousRememberBeforeCommitHook(runtime serverapp.RuntimeContext) func(context.Context, rememberapp.RememberProcessRequest, *repository.SynchronousRememberEmbeddingPlan) error {
	if runtime.PostgresDB == nil || runtime.RLS == nil {
		return nil
	}
	var mu sync.Mutex
	searchGenerationRotated := false
	supersessionRotated := false
	return func(ctx context.Context, input rememberapp.RememberProcessRequest, plan *repository.SynchronousRememberEmbeddingPlan) error {
		if plan == nil {
			return nil
		}
		mu.Lock()
		defer mu.Unlock()
		switch {
		case rememberInputHasFault(input, synchronousSearchGenerationFault):
			if searchGenerationRotated {
				return nil
			}
			if err := rotateSynchronousSearchGeneration(ctx, runtime, plan); err != nil {
				return err
			}
			searchGenerationRotated = true
		case rememberInputHasFault(input, synchronousSupersessionFault):
			if supersessionRotated {
				return nil
			}
			if err := rotateSynchronousSupersessionTarget(ctx, runtime, input); err != nil {
				return err
			}
			supersessionRotated = true
		}
		return nil
	}
}

func rememberInputHasFault(input rememberapp.RememberProcessRequest, fault string) bool {
	marker := "[fixture-fault:" + fault + "]"
	for _, evidence := range input.Evidence {
		if strings.Contains(evidence.Content, marker) {
			return true
		}
	}
	return false
}

func rotateSynchronousSearchGeneration(ctx context.Context, runtime serverapp.RuntimeContext, plan *repository.SynchronousRememberEmbeddingPlan) error {
	err := runtime.RLS.WithSystemTx(ctx, runtime.PostgresDB, func(tx *gorm.DB) error {
		result := tx.WithContext(ctx).Exec(`
			WITH prior AS (
				SELECT search_index_generation_id, generation, embedding_contract_id,
				       embedding_dimensions, ann_strategy, operator_class,
				       indexed_expression, physical_index_name, hnsw_m,
				       hnsw_ef_construction, query_ef_search, exact_max_rows,
				       candidate_limit, allow_exact_fallback, metadata
				FROM search_index_generations
				WHERE search_index_generation_id = ?::uuid
				  AND embedding_contract_id = ?::uuid
				  AND activation_state = 'active'
				FOR UPDATE
			), retired AS (
				UPDATE search_index_generations AS current
				SET activation_state = 'deprecated', activated_at = NULL
				FROM prior
				WHERE current.search_index_generation_id = prior.search_index_generation_id
				RETURNING current.search_index_generation_id
			)
			INSERT INTO search_index_generations (
				search_index_generation_id, generation, embedding_contract_id,
				embedding_dimensions, ann_strategy, operator_class,
				indexed_expression, physical_index_name, hnsw_m,
				hnsw_ef_construction, query_ef_search, exact_max_rows,
				candidate_limit, allow_exact_fallback, activation_state,
				activated_at, metadata
			)
			SELECT gen_random_uuid(), prior.generation + 1, prior.embedding_contract_id,
			       prior.embedding_dimensions, prior.ann_strategy, prior.operator_class,
			       prior.indexed_expression, prior.physical_index_name, prior.hnsw_m,
			       prior.hnsw_ef_construction, prior.query_ef_search, prior.exact_max_rows,
			       prior.candidate_limit, prior.allow_exact_fallback, 'active', now(), prior.metadata
			FROM prior
			JOIN retired ON retired.search_index_generation_id = prior.search_index_generation_id
		`, plan.SearchGenerationID, plan.EmbeddingContractID)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("synchronous E2E search generation was not active")
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("synchronous E2E search generation rotation: %w", err)
	}
	return nil
}

func rotateSynchronousSupersessionTarget(ctx context.Context, runtime serverapp.RuntimeContext, input rememberapp.RememberProcessRequest) error {
	targets := make([]string, 0)
	for _, evidence := range input.Evidence {
		targets = append(targets, evidence.SupersedesEvidenceIDs...)
	}
	if len(targets) == 0 {
		return errors.New("synchronous E2E supersession rotation requires a target")
	}
	rls, ok := runtime.RLS.(*postgres.RLS)
	if !ok || rls == nil {
		return errors.New("synchronous E2E supersession rotation requires PostgreSQL RLS")
	}
	ledger := repository.NewLedgerRepository(runtime.PostgresDB, rls)
	_, err := ledger.CreateIngest(ctx, repository.CreateIngestInput{
		TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID,
		SpaceID: input.SpaceID, SpaceGeneration: input.SpaceGeneration,
		IdempotencyKey: "e2e-late-supersession-" + uuid.NewString(), RequestHash: uuid.NewString(),
		SourceSummary: "synchronous E2E late supersession fence", Status: "quarantined",
		Evidence: []repository.EvidenceInput{{
			Content:    "synchronous E2E competing supersession",
			SourceType: "manual", Authority: "primary",
			IdempotencyKey:        "e2e-late-supersession-evidence-" + uuid.NewString(),
			SupersedesEvidenceIDs: targets,
		}},
	})
	if err != nil {
		return fmt.Errorf("synchronous E2E supersession rotation: %w", err)
	}
	return nil
}
