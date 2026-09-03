package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	RememberDuplicateCandidateLimit = 10
	RememberDuplicateMaxEvidence    = 20
)

var ErrRememberDuplicateCandidateStale = errors.New("remember duplicate candidate is stale")

// RememberDuplicateCandidate is a canonical evidence item that the assessor
// may compare with one submitted occurrence. The repository has already
// applied authenticated team, profile, space, lifecycle, and search fences.
type RememberDuplicateCandidate struct {
	FragmentID          string
	OwnerProfileID      string
	Content             string
	ContentHash         string
	Distance            float64
	EmbeddingContractID string
}

// RememberDuplicateCandidateGroup binds the bounded candidate allowlist to a
// submitted evidence index. Empty candidates are meaningful: the assessor must
// return new for that item.
type RememberDuplicateCandidateGroup struct {
	EvidenceIndex int
	EvidenceID    string
	Candidates    []RememberDuplicateCandidate
}

// RememberDuplicateResolution is the server-owned exact result and the
// assessor-owned semantic result carried into the final transaction.
type RememberDuplicateResolution struct {
	EvidenceIndex       int
	EvidenceID          string
	InputFragmentID     string
	Disposition         string
	Exact               bool
	CandidateFragmentID string
	CandidateOwnerID    string
}

// RememberDuplicateEmbeddingPlan is the provider-independent pre-assessment
// render for unique non-exact evidence content.
type RememberDuplicateEmbeddingPlan struct {
	Documents               []SearchDocumentForEmbedding
	EmbeddingContractID     string
	EmbeddingDimensions     int
	EmbeddingModel          string
	SearchIndexGenerationID string
	IndexGeneration         int
}

// RememberDuplicateCandidateInput identifies one authenticated Remember
// request. Evidence content is read-only provider input; no durable row exists
// until the terminal commit.
type RememberDuplicateCandidateInput struct {
	TeamID          string
	OwnerProfileID  string
	SpaceID         string
	SpaceGeneration int64
	Evidence        []EvidenceInput
}

type RememberDuplicateResolutionResult struct {
	Exact      []RememberDuplicateResolution
	Candidates []RememberDuplicateCandidateGroup
}

// PlanRememberDuplicateEmbeddings resolves deterministic exact matches and
// returns one embedding document for every unique non-exact content value.
func (r *LedgerRepositoryImpl) PlanRememberDuplicateEmbeddings(
	ctx context.Context,
	input RememberDuplicateCandidateInput,
) (*RememberDuplicateEmbeddingPlan, error) {
	input = normalizeRememberDuplicateCandidateInput(input)
	if err := validateRememberDuplicateCandidateInput(input); err != nil {
		return nil, err
	}
	plan := &RememberDuplicateEmbeddingPlan{Documents: []SearchDocumentForEmbedding{}}
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		contract, err := loadActiveSearchContractInTx(ctx, tx)
		if err != nil {
			return err
		}
		plan.EmbeddingContractID = contract.EmbeddingContractID
		plan.EmbeddingDimensions = contract.EmbeddingDimensions
		plan.EmbeddingModel = contract.EmbeddingModel
		plan.SearchIndexGenerationID = contract.SearchIndexGenerationID
		plan.IndexGeneration = contract.IndexGeneration
		seenContent := make(map[string]struct{}, len(input.Evidence))
		for _, evidence := range input.Evidence {
			if !rememberEvidenceLifecycleBearing(evidence) {
				_, found, err := resolveRememberExactEvidenceInTx(ctx, tx, input, evidence)
				if err != nil {
					return err
				}
				if found {
					continue
				}
			}
			if !rememberEvidenceRequiresDuplicateAssessment(evidence) {
				continue
			}
			documentHash := searchDocumentHash(evidence.Content)
			if _, exists := seenContent[documentHash]; exists {
				continue
			}
			seenContent[documentHash] = struct{}{}
			renderedContent := strings.TrimSpace(evidence.Content)
			plan.Documents = append(plan.Documents, SearchDocumentForEmbedding{
				SearchDocumentResult: SearchDocumentResult{
					TeamID: input.TeamID, SearchDocumentID: "duplicate:" + evidence.FragmentID,
					OwnerProfileID: input.OwnerProfileID, SourceKind: "evidence", SourceID: evidence.FragmentID,
					SourceVersion: 1, ProjectionFormat: defaultProjectionFormat("evidence"),
					EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
					SpaceID: input.SpaceID, SpaceGeneration: input.SpaceGeneration,
				},
				DocumentText: renderedContent, DocumentHash: documentHash,
			})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("repository: plan Remember duplicate embeddings: %w", err)
	}
	return plan, nil
}

// ResolveRememberDuplicateCandidates performs the vector candidate lookup
// after provider work. It repeats exact matching so a concurrent exact writer
// can win before assessor decisions are used.
func (r *LedgerRepositoryImpl) ResolveRememberDuplicateCandidates(
	ctx context.Context,
	input RememberDuplicateCandidateInput,
	embeddings []InlineEmbeddingResult,
) (*RememberDuplicateResolutionResult, error) {
	input = normalizeRememberDuplicateCandidateInput(input)
	if err := validateRememberDuplicateCandidateInput(input); err != nil {
		return nil, err
	}
	if err := validateRememberEmbeddingContractFence(embeddings); err != nil {
		return nil, err
	}
	result := &RememberDuplicateResolutionResult{
		Exact:      make([]RememberDuplicateResolution, len(input.Evidence)),
		Candidates: []RememberDuplicateCandidateGroup{},
	}
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		contract, err := loadActiveSearchContractInTx(ctx, tx)
		if err != nil {
			return err
		}
		if err := validateRememberEmbeddingContractAgainstActive(embeddings, contract); err != nil {
			return err
		}
		vectors := make(map[string][]float32, len(embeddings))
		for _, embedding := range embeddings {
			hash := strings.TrimSpace(embedding.DocumentHash)
			if hash == "" {
				return fmt.Errorf("%w: duplicate embedding has no document hash", ErrInlineEmbeddingPlanMismatch)
			}
			if _, exists := vectors[hash]; exists {
				return fmt.Errorf("%w: duplicate embedding result has a repeated document hash", ErrInlineEmbeddingPlanMismatch)
			}
			vectors[hash] = append([]float32(nil), embedding.Embedding...)
		}
		for index, evidence := range input.Evidence {
			canonicalID, found := "", false
			if !rememberEvidenceLifecycleBearing(evidence) {
				var err error
				canonicalID, found, err = resolveRememberExactEvidenceInTx(ctx, tx, input, evidence)
				if err != nil {
					return err
				}
			}
			resolution := RememberDuplicateResolution{EvidenceIndex: index, EvidenceID: fmt.Sprintf("evidence:%d", index), InputFragmentID: evidence.FragmentID}
			if found {
				resolution.Disposition = "reuse"
				resolution.Exact = true
				resolution.CandidateFragmentID = canonicalID
				resolution.CandidateOwnerID = input.OwnerProfileID
				result.Exact[index] = resolution
				continue
			}
			if !rememberEvidenceRequiresDuplicateAssessment(evidence) {
				resolution.Disposition = "new"
				result.Exact[index] = resolution
				continue
			}
			vector, ok := vectors[searchDocumentHash(evidence.Content)]
			if !ok {
				return fmt.Errorf("%w: duplicate evidence content was not embedded", ErrInlineEmbeddingPlanMismatch)
			}
			candidates, err := listRememberDuplicateCandidatesInTx(ctx, tx, input, vector, contract)
			if err != nil {
				return err
			}
			result.Candidates = append(result.Candidates, RememberDuplicateCandidateGroup{
				EvidenceIndex: index, EvidenceID: resolution.EvidenceID, Candidates: candidates,
			})
			resolution.Disposition = "new"
			result.Exact[index] = resolution
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("repository: resolve Remember duplicate candidates: %w", err)
	}
	return result, nil
}

func rememberEvidenceRequiresDuplicateAssessment(item EvidenceInput) bool {
	return !item.ForceInsert && !rememberEvidenceLifecycleBearing(item)
}

func normalizeRememberDuplicateCandidateInput(input RememberDuplicateCandidateInput) RememberDuplicateCandidateInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.SpaceID = strings.TrimSpace(input.SpaceID)
	for index := range input.Evidence {
		input.Evidence[index].FragmentID = strings.TrimSpace(input.Evidence[index].FragmentID)
		input.Evidence[index].ContentHash = strings.TrimSpace(input.Evidence[index].ContentHash)
	}
	return input
}

func validateRememberDuplicateCandidateInput(input RememberDuplicateCandidateInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("duplicate team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.OwnerProfileID); err != nil {
		return fmt.Errorf("duplicate owner_profile_id is required: %w", err)
	}
	if input.SpaceID != "" {
		if _, err := uuid.Parse(input.SpaceID); err != nil {
			return fmt.Errorf("duplicate space_id is invalid: %w", err)
		}
		if input.SpaceGeneration < 1 {
			return errors.New("duplicate space_generation is required when space_id is set")
		}
	} else if input.SpaceGeneration != 0 {
		return errors.New("duplicate space_id is required when space_generation is set")
	}
	if len(input.Evidence) == 0 || len(input.Evidence) > RememberDuplicateMaxEvidence {
		return fmt.Errorf("duplicate evidence must contain between 1 and %d items", RememberDuplicateMaxEvidence)
	}
	seen := make(map[string]struct{}, len(input.Evidence))
	for index, evidence := range input.Evidence {
		if _, err := uuid.Parse(evidence.FragmentID); err != nil {
			return fmt.Errorf("duplicate evidence[%d].fragment_id is invalid: %w", index, err)
		}
		if evidence.Content == "" || evidence.ContentHash == "" {
			return fmt.Errorf("duplicate evidence[%d] content and hash are required", index)
		}
		if want := sha256Hex(evidence.Content); evidence.ContentHash != want {
			return fmt.Errorf("duplicate evidence[%d].content_hash does not match content", index)
		}
		if _, exists := seen[evidence.FragmentID]; exists {
			return fmt.Errorf("duplicate evidence[%d].fragment_id is duplicated", index)
		}
		seen[evidence.FragmentID] = struct{}{}
	}
	return nil
}

func resolveRememberExactEvidenceInTx(
	ctx context.Context,
	tx *gorm.DB,
	input RememberDuplicateCandidateInput,
	evidence EvidenceInput,
) (string, bool, error) {
	spaceClause, args := rememberDuplicateSpacePredicate(input.SpaceID, input.SpaceGeneration, "fragment")
	row := tx.WithContext(ctx).Raw(`
		WITH active_contract AS (
			SELECT contract.embedding_contract_id, contract.dimensions
			FROM search_index_generations AS generation
			JOIN embedding_contracts AS contract
			  ON contract.embedding_contract_id = generation.embedding_contract_id
			 AND contract.dimensions = generation.embedding_dimensions
			WHERE generation.activation_state = 'active'
			  AND contract.lifecycle_state = 'active'
			  AND contract.distance_metric = 'cosine'
			ORDER BY contract.version DESC, generation.generation DESC, generation.created_at DESC
			LIMIT 1
		)
		SELECT fragment.fragment_id::text
		FROM evidence_fragments AS fragment
		WHERE fragment.team_id = ?::uuid
		  AND fragment.owner_profile_id = ?::uuid
		  AND fragment.content_hash = ?
		  AND fragment.content = ?
		  AND NOT EXISTS (
		      SELECT 1 FROM evidence_exact_aliases AS alias
		      WHERE alias.team_id = fragment.team_id
		        AND alias.alias_fragment_id = fragment.fragment_id
		  )
		  AND NOT EXISTS (
		      SELECT 1 FROM evidence_lifecycle_events AS lifecycle
		      WHERE lifecycle.team_id = fragment.team_id
		        AND lifecycle.target_fragment_id = fragment.fragment_id
		  )
		  AND NOT EXISTS (
		      SELECT 1 FROM evidence_quarantines AS quarantine
		      WHERE quarantine.team_id = fragment.team_id
		        AND quarantine.fragment_id = fragment.fragment_id
		        AND quarantine.status = 'active'
		  )
		  AND (
		      fragment.source_id IS NULL
		      OR EXISTS (
		          SELECT 1 FROM evidence_sources AS source
		          WHERE source.team_id = fragment.team_id
		            AND source.source_id = fragment.source_id
		            AND source.owner_profile_id = fragment.owner_profile_id
		            AND source.current_revision_id = fragment.source_revision_id
		      )
		  )
		  AND EXISTS (
		      SELECT 1
		      FROM search_documents AS document
		      JOIN active_contract AS contract
		        ON contract.embedding_contract_id = document.embedding_contract_id
		       AND contract.dimensions = document.embedding_dimensions
		      WHERE document.team_id = fragment.team_id
		        AND document.owner_profile_id = fragment.owner_profile_id
		        AND document.source_kind = 'evidence'
		        AND document.source_id = fragment.fragment_id
		        AND document.source_version = 1
		        AND document.projection_format_version = 1
		        AND document.search_state = 'current'
		        AND document.embedding IS NOT NULL
		        AND vector_dims(document.embedding) = document.embedding_dimensions
		        AND document.space_id IS NOT DISTINCT FROM fragment.space_id
		        AND document.space_generation IS NOT DISTINCT FROM fragment.space_generation
		  )
		  `+spaceClause+`
		ORDER BY fragment.created_at ASC, fragment.fragment_id ASC
		LIMIT 1
	`, append([]any{input.TeamID, input.OwnerProfileID, evidence.ContentHash, evidence.Content}, args...)...).Row()
	var fragmentID string
	if err := row.Scan(&fragmentID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}
	return fragmentID, true, nil
}

func listRememberDuplicateCandidatesInTx(
	ctx context.Context,
	tx *gorm.DB,
	input RememberDuplicateCandidateInput,
	vector []float32,
	contract *ActiveSearchContract,
) ([]RememberDuplicateCandidate, error) {
	literal, err := vectorLiteral(vector)
	if err != nil {
		return nil, err
	}
	spaceClause, args := rememberDuplicateSpacePredicate(input.SpaceID, input.SpaceGeneration, "fragment")
	queryArgs := []any{literal, input.TeamID, contract.EmbeddingContractID, contract.EmbeddingDimensions, input.OwnerProfileID}
	queryArgs = append(queryArgs, args...)
	queryArgs = append(queryArgs, literal, RememberDuplicateCandidateLimit)
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT fragment.fragment_id::text, fragment.owner_profile_id::text,
		       fragment.content, fragment.content_hash,
		       (document.embedding <=> ?::vector)::double precision AS distance,
		       document.embedding_contract_id::text
		FROM search_documents AS document
		JOIN evidence_fragments AS fragment
		  ON fragment.team_id = document.team_id
		 AND fragment.fragment_id = document.source_id
		JOIN memory_spaces AS space
		  ON space.team_id = fragment.team_id
		 AND space.id = fragment.space_id
		WHERE document.team_id = ?::uuid
		  AND document.source_kind = 'evidence'
		  AND document.embedding_contract_id = ?::uuid
		  AND document.embedding_dimensions = ?
		  AND document.search_state = 'current'
		  AND document.embedding IS NOT NULL
		  AND space.lifecycle_state = 'active'
		  AND fragment.space_generation = dense_mem_active_space_generation(fragment.team_id, fragment.space_id)
		  AND NOT EXISTS (
		      SELECT 1 FROM evidence_exact_aliases AS alias
		      WHERE alias.team_id = fragment.team_id
		        AND alias.alias_fragment_id = fragment.fragment_id
		  )
		  AND NOT EXISTS (
		      SELECT 1 FROM evidence_lifecycle_events AS lifecycle
		      WHERE lifecycle.team_id = fragment.team_id
		        AND lifecycle.target_fragment_id = fragment.fragment_id
		  )
		  AND NOT EXISTS (
		      SELECT 1 FROM evidence_quarantines AS quarantine
		      WHERE quarantine.team_id = fragment.team_id
		        AND quarantine.fragment_id = fragment.fragment_id
		        AND quarantine.status = 'active'
		  )
		  AND (
		      fragment.source_id IS NULL
		      OR EXISTS (
		          SELECT 1 FROM evidence_sources AS source
		          WHERE source.team_id = fragment.team_id
		            AND source.source_id = fragment.source_id
		            AND source.owner_profile_id = fragment.owner_profile_id
		            AND source.current_revision_id = fragment.source_revision_id
		      )
		  )
		  AND (space.kind = 'team_shared' OR fragment.owner_profile_id = ?::uuid)
		  `+spaceClause+`
		ORDER BY document.embedding <=> ?::vector ASC, fragment.fragment_id ASC
		LIMIT ?
	`, queryArgs...).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	candidates := make([]RememberDuplicateCandidate, 0, RememberDuplicateCandidateLimit)
	for rows.Next() {
		var candidate RememberDuplicateCandidate
		if err := rows.Scan(&candidate.FragmentID, &candidate.OwnerProfileID, &candidate.Content, &candidate.ContentHash, &candidate.Distance, &candidate.EmbeddingContractID); err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Distance != candidates[j].Distance {
			return candidates[i].Distance < candidates[j].Distance
		}
		return candidates[i].FragmentID < candidates[j].FragmentID
	})
	return candidates, nil
}

func rememberDuplicateSpacePredicate(spaceID string, generation int64, tableAlias string) (string, []any) {
	if strings.TrimSpace(spaceID) == "" {
		return fmt.Sprintf(
			"AND %s.space_id = dense_mem_team_shared_space(%s.team_id) AND %s.space_generation = dense_mem_active_space_generation(%s.team_id, %s.space_id)",
			tableAlias, tableAlias, tableAlias, tableAlias, tableAlias,
		), nil
	}
	return fmt.Sprintf(
		"AND %s.space_id = ?::uuid AND %s.space_generation = ? AND EXISTS (SELECT 1 FROM memory_spaces AS duplicate_space WHERE duplicate_space.team_id = %s.team_id AND duplicate_space.id = %s.space_id AND duplicate_space.lifecycle_state = 'active' AND duplicate_space.generation = %s.space_generation)",
		tableAlias, tableAlias, tableAlias, tableAlias, tableAlias,
	), []any{spaceID, generation}
}

type rememberDuplicateSourceFence struct {
	SourceID       string
	OwnerProfileID string
}

// lockRememberDuplicateEligibility keeps the space, source revision, and
// lifecycle predicates stable until the final candidate recheck and occurrence
// insert. The lock order matches the known-evidence fence: space, sources,
// then lifecycle targets.
func lockRememberDuplicateEligibility(
	ctx context.Context,
	tx *gorm.DB,
	input SynchronousRememberCommitInput,
	fragmentIDs []string,
) error {
	if len(fragmentIDs) == 0 {
		return nil
	}
	spaceID := strings.TrimSpace(input.SpaceID)
	if spaceID == "" {
		fence, err := loadTeamSharedSpaceFence(ctx, tx, input.TeamID)
		if err != nil {
			return fmt.Errorf("%w: duplicate space fence is unavailable: %v", ErrRememberDuplicateCandidateStale, err)
		}
		spaceID = fence.ID
	}
	if err := lockKnownEvidenceSpace(ctx, tx, input.TeamID, spaceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) || isPostgresLockNotAvailable(err) {
			return ErrRememberDuplicateCandidateStale
		}
		return err
	}

	orderedFragments := append([]string(nil), fragmentIDs...)
	sort.Strings(orderedFragments)
	sources := make(map[string]rememberDuplicateSourceFence, len(orderedFragments))
	for _, fragmentID := range orderedFragments {
		var sourceID, ownerProfileID string
		err := tx.WithContext(ctx).Raw(`
			SELECT COALESCE(source_id::text, ''), owner_profile_id::text
			FROM evidence_fragments
			WHERE team_id = ?::uuid AND fragment_id = ?::uuid
			LIMIT 1
		`, input.TeamID, fragmentID).Row().Scan(&sourceID, &ownerProfileID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrRememberDuplicateCandidateStale
		}
		if err != nil {
			return err
		}
		if strings.TrimSpace(sourceID) == "" {
			continue
		}
		key := sourceID + "\x00" + ownerProfileID
		sources[key] = rememberDuplicateSourceFence{SourceID: sourceID, OwnerProfileID: ownerProfileID}
	}
	orderedSources := make([]string, 0, len(sources))
	for key := range sources {
		orderedSources = append(orderedSources, key)
	}
	sort.Strings(orderedSources)
	for _, key := range orderedSources {
		source := sources[key]
		if err := lockKnownEvidenceSource(ctx, tx, input.TeamID, source.SourceID, source.OwnerProfileID); err != nil {
			if errors.Is(err, sql.ErrNoRows) || isPostgresLockNotAvailable(err) {
				return ErrRememberDuplicateCandidateStale
			}
			return err
		}
	}
	if err := lockEvidenceLifecycleTargetIDs(ctx, tx, input.TeamID, orderedFragments); err != nil {
		return err
	}
	return nil
}

func rememberDuplicateResolutionByIndex(input SynchronousRememberCommitInput) map[int]RememberDuplicateResolution {
	resolved := make(map[int]RememberDuplicateResolution, len(input.DuplicateResolutions))
	for _, resolution := range input.DuplicateResolutions {
		resolved[resolution.EvidenceIndex] = resolution
	}
	return resolved
}

func rememberEvidenceLifecycleBearing(item EvidenceInput) bool {
	return len(item.SupersedesEvidenceIDs) > 0 ||
		strings.TrimSpace(item.SourceKey) != "" ||
		strings.TrimSpace(item.SourceRevisionToken) != "" ||
		strings.TrimSpace(item.ExpectedPreviousRevisionToken) != ""
}

func rememberDuplicateCandidateByIDInTx(
	ctx context.Context,
	tx *gorm.DB,
	input SynchronousRememberCommitInput,
	resolution RememberDuplicateResolution,
	contract *ActiveSearchContract,
) (RememberDuplicateCandidate, error) {
	if resolution.CandidateFragmentID == "" || resolution.CandidateOwnerID == "" {
		return RememberDuplicateCandidate{}, ErrRememberDuplicateCandidateStale
	}
	spaceClause, args := rememberDuplicateSpacePredicate(input.SpaceID, input.SpaceGeneration, "fragment")
	queryArgs := []any{input.TeamID, resolution.CandidateFragmentID, resolution.CandidateOwnerID, contract.EmbeddingContractID, contract.EmbeddingDimensions}
	queryArgs = append(queryArgs, args...)
	queryArgs = append(queryArgs, input.OwnerProfileID)
	row := tx.WithContext(ctx).Raw(`
		SELECT fragment.fragment_id::text, fragment.owner_profile_id::text,
		       fragment.content, fragment.content_hash, document.embedding_contract_id::text
		FROM evidence_fragments AS fragment
		JOIN search_documents AS document
		  ON document.team_id = fragment.team_id
		 AND document.source_kind = 'evidence'
		 AND document.source_id = fragment.fragment_id
		JOIN memory_spaces AS space
		  ON space.team_id = fragment.team_id
		 AND space.id = fragment.space_id
		WHERE fragment.team_id = ?::uuid
		  AND fragment.fragment_id = ?::uuid
		  AND fragment.owner_profile_id = ?::uuid
		  AND document.embedding_contract_id = ?::uuid
		  AND document.embedding_dimensions = ?
		  AND document.search_state = 'current'
		  AND document.embedding IS NOT NULL
		  AND space.lifecycle_state = 'active'
		  AND fragment.space_generation = dense_mem_active_space_generation(fragment.team_id, fragment.space_id)
		  AND NOT EXISTS (
		      SELECT 1 FROM evidence_exact_aliases AS alias
		      WHERE alias.team_id = fragment.team_id
		        AND alias.alias_fragment_id = fragment.fragment_id
		  )
		  AND NOT EXISTS (
		      SELECT 1 FROM evidence_lifecycle_events AS lifecycle
		      WHERE lifecycle.team_id = fragment.team_id
		        AND lifecycle.target_fragment_id = fragment.fragment_id
		  )
		  AND NOT EXISTS (
		      SELECT 1 FROM evidence_quarantines AS quarantine
		      WHERE quarantine.team_id = fragment.team_id
		        AND quarantine.fragment_id = fragment.fragment_id
		        AND quarantine.status = 'active'
		  )
		  AND (
		      fragment.source_id IS NULL
		      OR EXISTS (
		          SELECT 1 FROM evidence_sources AS source
		          WHERE source.team_id = fragment.team_id
		            AND source.source_id = fragment.source_id
		            AND source.owner_profile_id = fragment.owner_profile_id
		            AND source.current_revision_id = fragment.source_revision_id
		      )
		  )
		  `+spaceClause+`
		  AND (fragment.owner_profile_id = ?::uuid OR EXISTS (
		      SELECT 1 FROM memory_spaces AS space
		      WHERE space.team_id = fragment.team_id
		        AND space.id = fragment.space_id
		        AND space.kind = 'team_shared'
		  ))
		LIMIT 1
	`, queryArgs...).Row()
	var candidate RememberDuplicateCandidate
	if err := row.Scan(&candidate.FragmentID, &candidate.OwnerProfileID, &candidate.Content, &candidate.ContentHash, &candidate.EmbeddingContractID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return RememberDuplicateCandidate{}, ErrRememberDuplicateCandidateStale
		}
		return RememberDuplicateCandidate{}, err
	}
	return candidate, nil
}

func lockRememberDuplicateKeysInTx(ctx context.Context, tx *gorm.DB, input SynchronousRememberCommitInput) error {
	keys := make(map[string]struct{}, len(input.Evidence)+len(input.DuplicateResolutions))
	for _, evidence := range input.Evidence {
		keys["content:"+input.TeamID+":"+input.OwnerProfileID+":"+input.SpaceID+":"+fmt.Sprint(input.SpaceGeneration)+":"+evidence.ContentHash] = struct{}{}
	}
	for _, resolution := range input.DuplicateResolutions {
		if resolution.CandidateFragmentID != "" {
			keys["candidate:"+input.TeamID+":"+resolution.CandidateFragmentID] = struct{}{}
		}
	}
	ordered := make([]string, 0, len(keys))
	for key := range keys {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)
	for _, key := range ordered {
		if err := tx.WithContext(ctx).Exec("SELECT pg_advisory_xact_lock(hashtextextended(?::text, 0))", key).Error; err != nil {
			return err
		}
	}
	return nil
}

func insertRememberEvidenceOccurrence(
	ctx context.Context,
	tx *gorm.DB,
	input CreateIngestInput,
	item EvidenceInput,
	index int,
	occurrenceID string,
	canonicalFragmentID string,
	canonicalOwnerID string,
	source *SourceRevisionResult,
) (EvidenceFragment, error) {
	metadata, err := marshalJSON(item.Metadata)
	if err != nil {
		return EvidenceFragment{}, err
	}
	sourceID, sourceRevisionID := "", ""
	if source != nil {
		sourceID, sourceRevisionID = source.SourceID, source.SourceRevisionID
	}
	if strings.TrimSpace(occurrenceID) == "" {
		occurrenceID = uuid.NewString()
	}
	if strings.TrimSpace(canonicalFragmentID) == "" {
		return EvidenceFragment{}, errors.New("remember occurrence canonical fragment is required")
	}
	if strings.TrimSpace(canonicalOwnerID) == "" {
		canonicalOwnerID = input.OwnerProfileID
	}
	rows, err := tx.WithContext(ctx).Raw(`
		INSERT INTO evidence_occurrences (
		    team_id, occurrence_id, canonical_fragment_id, canonical_owner_profile_id,
		    ingest_id, owner_profile_id, space_id, space_generation, evidence_index,
		    content, content_hash, source_type, authority, source_ref, source_id,
		    source_revision_id, labels, metadata, force_insert
		) VALUES (
		    ?::uuid, ?::uuid, ?::uuid, ?::uuid, ?::uuid, ?::uuid,
		    NULLIF(?, '')::uuid, NULLIF(?::bigint, 0), ?, ?, ?, ?, ?, ?,
		    NULLIF(?, '')::uuid, NULLIF(?, '')::uuid, ?, ?::jsonb, ?
		)
		ON CONFLICT (team_id, occurrence_id) DO NOTHING
		RETURNING occurrence_id::text
		`, input.TeamID, occurrenceID, canonicalFragmentID, canonicalOwnerID,
		input.IngestID, input.OwnerProfileID, input.SpaceID, input.SpaceGeneration,
		index, item.Content, item.ContentHash, item.SourceType, item.Authority, item.SourceRef,
		sourceID, sourceRevisionID, pqStringArray(item.Labels), string(metadata), item.ForceInsert).Rows()
	if err != nil {
		return EvidenceFragment{}, err
	}
	defer rows.Close()
	if rows.Next() {
		if err := rows.Scan(&occurrenceID); err != nil {
			return EvidenceFragment{}, err
		}
	} else {
		if err := rows.Err(); err != nil {
			return EvidenceFragment{}, err
		}
		if err := tx.WithContext(ctx).Raw(`
			SELECT occurrence_id::text
			FROM evidence_occurrences
			WHERE team_id = ?::uuid AND occurrence_id = ?::uuid
		`, input.TeamID, occurrenceID).Row().Scan(&occurrenceID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return EvidenceFragment{}, fmt.Errorf("remember occurrence insert returned no row: %w", err)
			}
			return EvidenceFragment{}, err
		}
	}
	return EvidenceFragment{
		FragmentID: canonicalFragmentID, SubmittedFragmentID: item.FragmentID,
		OccurrenceID: occurrenceID, CanonicalOwnerID: canonicalOwnerID, OccurrenceOwnerID: input.OwnerProfileID,
		EvidenceIndex: index, Content: item.Content, ContentHash: item.ContentHash,
		Authority: item.Authority, SourceID: sourceID, SourceRevisionID: sourceRevisionID,
	}, rows.Err()
}

func resolveRememberEvidenceInTx(
	ctx context.Context,
	tx *gorm.DB,
	input SynchronousRememberCommitInput,
	createInput CreateIngestInput,
	contract *ActiveSearchContract,
	sources map[int]*SourceRevisionResult,
) ([]EvidenceFragment, error) {
	resolutions := rememberDuplicateResolutionByIndex(input)
	// Resolve the candidate IDs before writing any occurrence, then acquire the
	// same lifecycle-target locks used by retract, supersede, and quarantine.
	// The second lookup below is the authoritative eligibility fence.
	initialExact := make(map[int]string, len(createInput.Evidence))
	lifecycleTargets := make([]string, 0, len(createInput.Evidence))
	seenLifecycleTargets := make(map[string]struct{}, len(createInput.Evidence))
	for index, item := range createInput.Evidence {
		canonicalID, exact := "", false
		lifecycleBearing := rememberEvidenceLifecycleBearing(item)
		if !lifecycleBearing {
			var err error
			canonicalID, exact, err = resolveRememberExactEvidenceInTx(ctx, tx, RememberDuplicateCandidateInput{
				TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID,
				SpaceID: input.SpaceID, SpaceGeneration: input.SpaceGeneration,
				Evidence: []EvidenceInput{item},
			}, item)
			if err != nil {
				return nil, err
			}
		}
		if exact {
			initialExact[index] = canonicalID
			if _, exists := seenLifecycleTargets[canonicalID]; !exists {
				seenLifecycleTargets[canonicalID] = struct{}{}
				lifecycleTargets = append(lifecycleTargets, canonicalID)
			}
		}
		planned := resolutions[index]
		if !exact && planned.Disposition == "reuse" && planned.CandidateFragmentID != "" {
			if _, exists := seenLifecycleTargets[planned.CandidateFragmentID]; !exists {
				seenLifecycleTargets[planned.CandidateFragmentID] = struct{}{}
				lifecycleTargets = append(lifecycleTargets, planned.CandidateFragmentID)
			}
		}
	}
	if err := lockRememberDuplicateEligibility(ctx, tx, input, lifecycleTargets); err != nil {
		return nil, err
	}
	evidence := make([]EvidenceFragment, 0, len(createInput.Evidence))
	for index, item := range createInput.Evidence {
		planned := resolutions[index]
		canonicalID, exact := "", false
		lifecycleBearing := rememberEvidenceLifecycleBearing(item)
		if !lifecycleBearing {
			var err error
			canonicalID, exact, err = resolveRememberExactEvidenceInTx(ctx, tx, RememberDuplicateCandidateInput{
				TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID,
				SpaceID: input.SpaceID, SpaceGeneration: input.SpaceGeneration,
				Evidence: []EvidenceInput{item},
			}, item)
			if err != nil {
				return nil, err
			}
		}
		if expectedCanonicalID, wasInitiallyExact := initialExact[index]; wasInitiallyExact &&
			(!exact || canonicalID != expectedCanonicalID) {
			return nil, ErrRememberDuplicateCandidateStale
		}
		canonicalOwnerID := input.OwnerProfileID
		disposition := planned.Disposition
		if exact && !lifecycleBearing {
			disposition = "reuse"
			planned.CandidateFragmentID = canonicalID
			planned.CandidateOwnerID = input.OwnerProfileID
		}
		if lifecycleBearing {
			disposition = "new"
		}
		if disposition == "reuse" && !exact {
			candidate, err := rememberDuplicateCandidateByIDInTx(ctx, tx, input, planned, contract)
			if err != nil {
				return nil, err
			}
			canonicalID = candidate.FragmentID
			canonicalOwnerID = candidate.OwnerProfileID
		}
		if disposition != "reuse" {
			fragment, err := insertEvidenceFragment(ctx, tx, createInput, input.IngestID, index, item, sources[index])
			if err != nil {
				return nil, err
			}
			canonicalID = fragment.FragmentID
			canonicalOwnerID = input.OwnerProfileID
		}
		occurrence := EvidenceFragment{
			FragmentID: canonicalID, SubmittedFragmentID: item.FragmentID,
			OccurrenceID: canonicalID, CanonicalOwnerID: canonicalOwnerID,
			OccurrenceOwnerID: input.OwnerProfileID, EvidenceIndex: index,
			Content: item.Content, ContentHash: item.ContentHash,
			Authority: item.Authority,
		}
		if source := sources[index]; source != nil {
			occurrence.SourceID = source.SourceID
			occurrence.SourceRevisionID = source.SourceRevisionID
		}
		if disposition == "reuse" {
			occurrence.OccurrenceID = uuid.NewString()
			var err error
			occurrence, err = insertRememberEvidenceOccurrence(ctx, tx, createInput, item, index, occurrence.OccurrenceID, canonicalID, canonicalOwnerID, sources[index])
			if err != nil {
				return nil, err
			}
			occurrence.SubmittedFragmentID = item.FragmentID
			occurrence.FragmentID = canonicalID
		}
		evidence = append(evidence, occurrence)
	}
	return evidence, nil
}
