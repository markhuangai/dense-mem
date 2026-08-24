package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const maxRememberPreflightIssues = 20

type RememberPreflightIssue struct {
	Path    string
	Code    string
	Message string
}

type RememberPreflightError struct {
	Issues          []RememberPreflightIssue
	IssuesTruncated bool
}

func (e *RememberPreflightError) Error() string {
	if e == nil || len(e.Issues) == 0 {
		return "remember preflight validation failed"
	}
	return e.Issues[0].Message
}

type rememberPreflightCollector struct {
	issues    []RememberPreflightIssue
	truncated bool
}

func (c *rememberPreflightCollector) add(path, code, message string) {
	path = strings.TrimSpace(path)
	code = strings.TrimSpace(code)
	message = strings.TrimSpace(message)
	if path == "" || code == "" || message == "" {
		return
	}
	for _, issue := range c.issues {
		if issue.Path == path && issue.Code == code && issue.Message == message {
			return
		}
	}
	if len(c.issues) >= maxRememberPreflightIssues {
		c.truncated = true
		return
	}
	c.issues = append(c.issues, RememberPreflightIssue{Path: path, Code: code, Message: message})
}

func (c *rememberPreflightCollector) err() error {
	if len(c.issues) == 0 {
		return nil
	}
	sort.SliceStable(c.issues, func(i, j int) bool {
		if c.issues[i].Path != c.issues[j].Path {
			return c.issues[i].Path < c.issues[j].Path
		}
		if c.issues[i].Code != c.issues[j].Code {
			return c.issues[i].Code < c.issues[j].Code
		}
		return c.issues[i].Message < c.issues[j].Message
	})
	return &RememberPreflightError{Issues: c.issues, IssuesTruncated: c.truncated}
}

type rememberPreflightProposal struct {
	RelationshipHints []rememberPreflightRelationship `json:"relationship_hints"`
}

type rememberPreflightRelationship struct {
	Subject          rememberPreflightEntity            `json:"subject"`
	Predicate        rememberPreflightPredicate         `json:"predicate"`
	Object           rememberPreflightObject            `json:"object"`
	CorrectionTarget *rememberPreflightCorrectionTarget `json:"correction_target"`
	ConflictContext  *rememberPreflightConflictContext  `json:"conflict_context"`
}

type rememberPreflightEntity struct {
	KnownEntityID string `json:"known_entity_id"`
	EntityKind    string `json:"entity_kind"`
}

type rememberPreflightEntityState struct {
	Status     string
	EntityKind string
}

type rememberPreflightPredicate struct {
	KnownPredicateKey string `json:"known_predicate_key"`
}

type rememberPreflightObject struct {
	Entity *rememberPreflightEntity `json:"entity"`
}

type rememberPreflightCorrectionTarget struct {
	RelationshipID  string `json:"relationship_id"`
	ExpectedVersion int    `json:"expected_version"`
}

type rememberPreflightConflictContext struct {
	ConflictID      string `json:"conflict_id"`
	ExpectedVersion int    `json:"expected_version"`
}

func validateRememberSubmissionPreflight(ctx context.Context, tx *gorm.DB, input CreateIngestInput, ingestID string) error {
	proposalJSON, err := json.Marshal(input.Proposal)
	if err != nil {
		return err
	}
	var proposal rememberPreflightProposal
	if err := json.Unmarshal(proposalJSON, &proposal); err != nil {
		return err
	}
	collector := &rememberPreflightCollector{}
	entityStates := map[string]rememberPreflightEntityState{}
	predicateStates := map[string]bool{}
	for index, relationship := range proposal.RelationshipHints {
		if err := validateRememberExactEntity(ctx, tx, input.TeamID, input.OwnerProfileID, ingestID, relationship.Subject,
			fmt.Sprintf("/relationships/%d/subject", index), entityStates, collector); err != nil {
			return err
		}
		if relationship.Object.Entity != nil {
			if err := validateRememberExactEntity(ctx, tx, input.TeamID, input.OwnerProfileID, ingestID, *relationship.Object.Entity,
				fmt.Sprintf("/relationships/%d/object/entity", index), entityStates, collector); err != nil {
				return err
			}
		}
		if err := validateRememberExactPredicate(ctx, tx, input.TeamID, relationship.Predicate.KnownPredicateKey,
			fmt.Sprintf("/relationships/%d/predicate/known_predicate_key", index), predicateStates, collector); err != nil {
			return err
		}
		if err := validateRememberCorrectionTarget(ctx, tx, input, index, relationship.CorrectionTarget, collector); err != nil {
			return err
		}
		if err := validateRememberConflictContext(ctx, tx, input, index, relationship.ConflictContext, collector); err != nil {
			return err
		}
	}
	if err := validateRememberSourcePreflight(ctx, tx, input, collector); err != nil {
		return err
	}
	if err := validateRememberSupersessionPreflight(ctx, tx, input, collector); err != nil {
		return err
	}
	return collector.err()
}

func validateRememberExactEntity(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	ownerProfileID string,
	ingestID string,
	entity rememberPreflightEntity,
	basePath string,
	states map[string]rememberPreflightEntityState,
	collector *rememberPreflightCollector,
) error {
	entityID := strings.TrimSpace(entity.KnownEntityID)
	if entityID == "" {
		return nil
	}
	if _, err := uuid.Parse(entityID); err != nil {
		collector.add(basePath+"/known_entity_id", "invalid", "known_entity_id must be a UUID")
		return nil
	}
	state, loaded := states[entityID]
	if !loaded {
		err := withSystemModeInTx(ctx, tx, teamID, ownerProfileID, func(systemTx *gorm.DB) error {
			return systemTx.WithContext(ctx).Raw(`
				SELECT entity.status, entity.entity_kind
				FROM entity_records AS entity
				JOIN knowledge_ingests AS ingest
				  ON ingest.team_id = entity.team_id
				 AND ingest.space_id = entity.space_id
				 AND ingest.space_generation = entity.space_generation
				WHERE ingest.team_id = ?::uuid
				  AND ingest.owner_profile_id = ?::uuid
				  AND ingest.ingest_id = ?::uuid
				  AND entity.entity_id = ?::uuid
				LIMIT 1
				FOR SHARE OF entity
			`, teamID, ownerProfileID, ingestID, entityID).Row().Scan(&state.Status, &state.EntityKind)
		})
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		states[entityID] = state
	}
	if state.Status != "active" {
		collector.add(basePath+"/known_entity_id", "unavailable", "known_entity_id is unavailable")
		return nil
	}
	if entityKind := strings.TrimSpace(entity.EntityKind); entityKind != "" && entityKind != state.EntityKind {
		collector.add(basePath+"/entity_kind", "conflict", "entity_kind does not match known_entity_id")
	}
	return nil
}

func validateRememberExactPredicate(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	predicateKey string,
	path string,
	states map[string]bool,
	collector *rememberPreflightCollector,
) error {
	predicateKey = strings.TrimSpace(predicateKey)
	if predicateKey == "" {
		return nil
	}
	active, loaded := states[predicateKey]
	if !loaded {
		var version int
		err := tx.WithContext(ctx).Raw(`
			SELECT version
			FROM team_predicate_definitions
			WHERE team_id = ?::uuid AND predicate_key = ? AND lifecycle_state = 'active'
			ORDER BY version DESC
			LIMIT 1
		`, teamID, predicateKey).Row().Scan(&version)
		active = err == nil && version > 0
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		states[predicateKey] = active
	}
	if !active {
		collector.add(path, "unavailable", "known_predicate_key is unavailable")
	}
	return nil
}

func validateRememberCorrectionTarget(
	ctx context.Context,
	tx *gorm.DB,
	input CreateIngestInput,
	index int,
	target *rememberPreflightCorrectionTarget,
	collector *rememberPreflightCollector,
) error {
	if target == nil {
		return nil
	}
	base := fmt.Sprintf("/relationships/%d/correction_target", index)
	if _, err := uuid.Parse(strings.TrimSpace(target.RelationshipID)); err != nil {
		collector.add(base+"/relationship_id", "invalid", "correction target relationship_id must be a UUID")
		return nil
	}
	var version int
	err := tx.WithContext(ctx).Raw(`
		SELECT version
		FROM relationship_records
		WHERE team_id = ?::uuid
		  AND relationship_id = ?::uuid
		  AND owner_profile_id = ?::uuid
		  AND space_id IS NOT DISTINCT FROM NULLIF(?, '')::uuid
		  AND space_generation IS NOT DISTINCT FROM NULLIF(?, 0)::bigint
		  AND status = 'active'
		LIMIT 1
		FOR SHARE
	`, input.TeamID, target.RelationshipID, input.OwnerProfileID, input.SpaceID, input.SpaceGeneration).Row().Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		collector.add(base+"/relationship_id", "unavailable", "correction target is unavailable")
		return nil
	}
	if err != nil {
		return err
	}
	if version != target.ExpectedVersion {
		collector.add(base+"/expected_version", "stale", "correction target version is stale")
	}
	return nil
}

func validateRememberConflictContext(
	ctx context.Context,
	tx *gorm.DB,
	input CreateIngestInput,
	index int,
	conflict *rememberPreflightConflictContext,
	collector *rememberPreflightCollector,
) error {
	if conflict == nil {
		return nil
	}
	base := fmt.Sprintf("/relationships/%d/conflict_context", index)
	if _, err := uuid.Parse(strings.TrimSpace(conflict.ConflictID)); err != nil {
		collector.add(base+"/conflict_id", "invalid", "conflict_id must be a UUID")
		return nil
	}
	var version int
	err := tx.WithContext(ctx).Raw(`
		SELECT version
		FROM relationship_conflict_cases
		WHERE team_id = ?::uuid
		  AND conflict_id = ?::uuid
		  AND space_id IS NOT DISTINCT FROM NULLIF(?, '')::uuid
		  AND space_generation IS NOT DISTINCT FROM NULLIF(?, 0)::bigint
		  AND status IN ('open', 'overdue')
		LIMIT 1
		FOR SHARE
	`, input.TeamID, conflict.ConflictID, input.SpaceID, input.SpaceGeneration).Row().Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		collector.add(base+"/conflict_id", "unavailable", "conflict context is unavailable")
		return nil
	}
	if err != nil {
		return err
	}
	if version != conflict.ExpectedVersion {
		collector.add(base+"/expected_version", "stale", "conflict context version is stale")
	}
	return nil
}

type rememberSourcePreflightState struct {
	found                    bool
	currentToken             string
	currentHash              string
	currentProvenanceMatches bool
	spaceID                  string
	spaceGeneration          int64
}

func validateRememberSourcePreflight(
	ctx context.Context,
	tx *gorm.DB,
	input CreateIngestInput,
	collector *rememberPreflightCollector,
) error {
	states := map[string]rememberSourcePreflightState{}
	for index, item := range input.Evidence {
		if item.SourceKey == "" {
			continue
		}
		state, loaded := states[item.SourceKey]
		if !loaded {
			envelope, err := marshalJSON(item.SourceRevisionEnvelope)
			if err != nil {
				return err
			}
			err = tx.WithContext(ctx).Raw(`
				SELECT source.current_revision_token,
				       COALESCE(revision.content_hash, ''),
				       COALESCE(
				           source.source_kind = ?
				           AND source.authority = ?
				           AND revision.expected_previous_revision_token = ?
				           AND revision.envelope = ?::jsonb,
				           false
				       ),
				       COALESCE(source.space_id::text, ''),
				       COALESCE(source.space_generation, 0)
				FROM evidence_sources AS source
				LEFT JOIN evidence_source_revisions AS revision
				  ON revision.team_id = source.team_id
				 AND revision.source_revision_id = source.current_revision_id
				WHERE source.team_id = ?::uuid
				  AND source.owner_profile_id = ?::uuid
				  AND source.source_key = ?
				LIMIT 1
				FOR SHARE OF source
			`, sourceKindForEvidence(item.SourceType), item.Authority,
				item.ExpectedPreviousRevisionToken, string(envelope),
				input.TeamID, input.OwnerProfileID, item.SourceKey).Row().Scan(
				&state.currentToken, &state.currentHash, &state.currentProvenanceMatches,
				&state.spaceID, &state.spaceGeneration,
			)
			if err == nil {
				state.found = true
			} else if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			states[item.SourceKey] = state
		}
		base := fmt.Sprintf("/evidence/%d", index)
		if !state.found {
			if item.ExpectedPreviousRevisionToken != "" {
				collector.add(base+"/previous_source_revision", "stale", "source revision is stale")
			}
			continue
		}
		if input.SpaceID != "" && (state.spaceID != input.SpaceID || state.spaceGeneration != input.SpaceGeneration) {
			collector.add(base+"/source_key", "unavailable", "source_key is unavailable in this memory space")
			continue
		}
		if state.currentToken == item.SourceRevisionToken {
			if state.currentHash != item.SourceRevisionContentHash {
				collector.add(base+"/source_revision", "conflict", "source_revision already exists with different evidence")
			} else if !state.currentProvenanceMatches {
				collector.add(base+"/source_revision", "conflict", "source_revision already exists with different provenance")
			}
			continue
		}
		if state.currentToken != item.ExpectedPreviousRevisionToken {
			collector.add(base+"/previous_source_revision", "stale", "source revision is stale")
		}
	}
	return nil
}

func validateRememberSupersessionPreflight(
	ctx context.Context,
	tx *gorm.DB,
	input CreateIngestInput,
	collector *rememberPreflightCollector,
) error {
	return withSystemModeInTx(ctx, tx, input.TeamID, input.OwnerProfileID, func(systemTx *gorm.DB) error {
		return validateRememberSupersessionPreflightInSystem(ctx, systemTx, input, collector)
	})
}

func validateRememberSupersessionPreflightInSystem(
	ctx context.Context,
	tx *gorm.DB,
	input CreateIngestInput,
	collector *rememberPreflightCollector,
) error {
	allTargets := make([]string, 0)
	for _, item := range input.Evidence {
		allTargets = append(allTargets, item.SupersedesEvidenceIDs...)
	}
	if len(allTargets) == 0 {
		return nil
	}
	if err := lockEvidenceLifecycleTargetIDs(ctx, tx, input.TeamID, allTargets); err != nil {
		return err
	}
	for evidenceIndex, item := range input.Evidence {
		targetSpaceID := ""
		for targetIndex, targetID := range item.SupersedesEvidenceIDs {
			path := fmt.Sprintf("/evidence/%d/supersedes_evidence_ids/%d", evidenceIndex, targetIndex)
			spaceID, err := validateEvidenceLifecycleTargets(
				ctx, tx, input.TeamID, input.OwnerProfileID, []string{targetID},
			)
			if errors.Is(err, ErrEvidenceLifecycleNotFound) {
				collector.add(path, "unavailable", "supersession target is unavailable")
				continue
			}
			if errors.Is(err, ErrEvidenceLifecycleConflict) {
				collector.add(path, "stale", "supersession target is stale")
				continue
			}
			if err != nil {
				return err
			}
			stale := false
			if input.SpaceID != "" && spaceID != input.SpaceID {
				stale = true
			}
			if targetSpaceID == "" {
				targetSpaceID = spaceID
			} else if targetSpaceID != spaceID {
				stale = true
			}
			if stale {
				collector.add(path, "stale", "supersession target is stale")
			}
		}
	}
	return nil
}

// rememberSourceRevisionIntent is immutable intake data. It is deliberately
// separate from evidence_source_revisions because staging must not advance the
// active source pointer before semantic acceptance.
type rememberSourceRevisionIntent struct {
	FragmentID                    string
	SourceID                      string
	SourceRevisionID              string
	SourceKey                     string
	SourceKind                    string
	Authority                     string
	RevisionToken                 string
	ExpectedPreviousRevisionToken string
	ContentHash                   string
	Envelope                      map[string]any
}

type rememberSupersessionIntent struct {
	FragmentID       string
	TargetFragmentID string
}

func insertRememberSubmissionIntents(
	ctx context.Context,
	tx *gorm.DB,
	input CreateIngestInput,
	ingestID string,
	fragmentID string,
	item EvidenceInput,
) error {
	if strings.TrimSpace(item.SourceKey) != "" {
		envelope, err := marshalJSON(item.SourceRevisionEnvelope)
		if err != nil {
			return err
		}
		if err := tx.WithContext(ctx).Exec(`
			INSERT INTO remember_source_revision_intents (
			    team_id, ingest_id, owner_profile_id, fragment_id,
			    source_key, source_kind, authority, revision_token,
			    expected_previous_revision_token, content_hash, envelope,
			    space_id, space_generation
			) VALUES (
			    ?::uuid, ?::uuid, ?::uuid, ?::uuid,
			    ?, ?, ?, ?, ?, ?, ?::jsonb,
			    NULLIF(?, '')::uuid, NULLIF(?::bigint, 0)
			)
		`, input.TeamID, ingestID, input.OwnerProfileID, fragmentID,
			strings.TrimSpace(item.SourceKey), sourceKindForEvidence(item.SourceType),
			strings.TrimSpace(item.Authority), strings.TrimSpace(item.SourceRevisionToken),
			strings.TrimSpace(item.ExpectedPreviousRevisionToken), strings.TrimSpace(item.SourceRevisionContentHash),
			string(envelope), input.SpaceID, input.SpaceGeneration).Error; err != nil {
			return err
		}
	}
	for _, targetID := range item.SupersedesEvidenceIDs {
		inserted := tx.WithContext(ctx).Exec(`
			INSERT INTO remember_supersession_intents (
			    team_id, ingest_id, owner_profile_id, fragment_id, target_fragment_id,
			    space_id, space_generation
			)
			SELECT ingest.team_id, ingest.ingest_id, ingest.owner_profile_id, ?::uuid, ?::uuid,
			       ingest.space_id, ingest.space_generation
			FROM knowledge_ingests AS ingest
			WHERE ingest.team_id = ?::uuid
			  AND ingest.ingest_id = ?::uuid
			  AND ingest.owner_profile_id = ?::uuid
		`, fragmentID, targetID, input.TeamID, ingestID, input.OwnerProfileID)
		if inserted.Error != nil {
			return inserted.Error
		}
		if inserted.RowsAffected != 1 {
			return errors.New("remember supersession intent does not match its staged ingest")
		}
	}
	return nil
}

// validateRememberSubmissionSupersessionTargets performs the exact-reference
// preflight without changing lifecycle state. The same targets are locked and
// revalidated in the accepted semantic transaction.
func validateRememberSubmissionSupersessionTargets(
	ctx context.Context,
	tx *gorm.DB,
	input CreateIngestInput,
	ingestID string,
) error {
	for _, item := range input.Evidence {
		if len(item.SupersedesEvidenceIDs) == 0 {
			continue
		}
		if _, err := planEvidenceLifecycle(ctx, tx, evidenceLifecycleOperationInput{
			TeamID:              input.TeamID,
			OwnerProfileID:      input.OwnerProfileID,
			EvidenceIDs:         item.SupersedesEvidenceIDs,
			ReplacementIngestID: ingestID,
		}); err != nil {
			return fmt.Errorf("remember supersession target preflight: %w", err)
		}
	}
	return nil
}

func loadRememberSourceRevisionIntents(
	ctx context.Context,
	tx *gorm.DB,
	scope SubmissionAssessmentRunScope,
) ([]rememberSourceRevisionIntent, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT fragment_id::text, COALESCE(source_id::text, ''), COALESCE(source_revision_id::text, ''),
		       source_key, source_kind, authority, revision_token,
		       expected_previous_revision_token, content_hash, envelope
		FROM remember_source_revision_intents
		WHERE team_id = ?::uuid AND ingest_id = ?::uuid AND owner_profile_id = ?::uuid
		ORDER BY fragment_id ASC
	`, scope.TeamID, scope.IngestID, scope.OwnerProfileID).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	intents := make([]rememberSourceRevisionIntent, 0)
	for rows.Next() {
		var intent rememberSourceRevisionIntent
		var envelope []byte
		if err := rows.Scan(&intent.FragmentID, &intent.SourceID, &intent.SourceRevisionID, &intent.SourceKey, &intent.SourceKind, &intent.Authority,
			&intent.RevisionToken, &intent.ExpectedPreviousRevisionToken, &intent.ContentHash, &envelope); err != nil {
			return nil, err
		}
		if err := jsonUnmarshalObject(envelope, &intent.Envelope); err != nil {
			return nil, err
		}
		intents = append(intents, intent)
	}
	return intents, rows.Err()
}

func loadRememberSupersessionIntents(
	ctx context.Context,
	tx *gorm.DB,
	scope SubmissionAssessmentRunScope,
) ([]rememberSupersessionIntent, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT fragment_id::text, target_fragment_id::text
		FROM remember_supersession_intents
		WHERE team_id = ?::uuid AND ingest_id = ?::uuid AND owner_profile_id = ?::uuid
		ORDER BY target_fragment_id ASC, fragment_id ASC
	`, scope.TeamID, scope.IngestID, scope.OwnerProfileID).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	intents := make([]rememberSupersessionIntent, 0)
	for rows.Next() {
		var intent rememberSupersessionIntent
		if err := rows.Scan(&intent.FragmentID, &intent.TargetFragmentID); err != nil {
			return nil, err
		}
		intents = append(intents, intent)
	}
	return intents, rows.Err()
}

func applyRememberSubmissionIntents(
	ctx context.Context,
	tx *gorm.DB,
	scope SubmissionAssessmentRunScope,
) error {
	sourceIntents, err := loadRememberSourceRevisionIntents(ctx, tx, scope)
	if err != nil {
		return err
	}
	sources := make(map[string]SourceRevisionResult, len(sourceIntents))
	for _, intent := range sourceIntents {
		advanced, err := advanceSourceRevisionInTx(ctx, tx, AdvanceSourceRevisionInput{
			TeamID:                        scope.TeamID,
			OwnerProfileID:                scope.OwnerProfileID,
			IngestID:                      scope.IngestID,
			SourceKey:                     intent.SourceKey,
			SourceKind:                    intent.SourceKind,
			Authority:                     intent.Authority,
			RevisionToken:                 intent.RevisionToken,
			ExpectedPreviousRevisionToken: intent.ExpectedPreviousRevisionToken,
			ContentHash:                   intent.ContentHash,
			Envelope:                      intent.Envelope,
		}, sources)
		if err != nil {
			return err
		}
		updated := tx.WithContext(ctx).Exec(`
			UPDATE remember_source_revision_intents
			SET source_id = ?::uuid, source_revision_id = ?::uuid
			WHERE team_id = ?::uuid AND owner_profile_id = ?::uuid AND fragment_id = ?::uuid
			  AND ingest_id = ?::uuid AND source_revision_id IS NULL
		`, advanced.SourceID, advanced.SourceRevisionID, scope.TeamID, scope.OwnerProfileID,
			intent.FragmentID, scope.IngestID)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return errors.New("remember source revision intent no longer matches staged evidence")
		}
		bound := tx.WithContext(ctx).Exec(`
			UPDATE evidence_fragments
			SET source_id = ?::uuid, source_revision_id = ?::uuid
			WHERE team_id = ?::uuid AND owner_profile_id = ?::uuid AND fragment_id = ?::uuid
			  AND ingest_id = ?::uuid AND source_id IS NULL AND source_revision_id IS NULL
		`, advanced.SourceID, advanced.SourceRevisionID, scope.TeamID, scope.OwnerProfileID,
			intent.FragmentID, scope.IngestID)
		if bound.Error != nil {
			return bound.Error
		}
		if bound.RowsAffected != 1 {
			return errors.New("remember source revision no longer matches staged evidence")
		}
	}

	supersessions, err := loadRememberSupersessionIntents(ctx, tx, scope)
	if err != nil {
		return err
	}
	if len(supersessions) == 0 {
		return nil
	}
	requestHash, idempotencyKey, err := loadRememberSubmissionIdentity(ctx, tx, scope)
	if err != nil {
		return err
	}
	lifecycleKey := rememberSupersessionLifecycleKey(scope.IngestID)
	if err := lockEvidenceLifecycleIdempotencyKeys(ctx, tx, scope.TeamID, scope.OwnerProfileID, []string{lifecycleKey}); err != nil {
		return err
	}
	if existing, err := loadEvidenceLifecycleOperation(ctx, tx, scope.TeamID, scope.OwnerProfileID, lifecycleKey); err != nil {
		return err
	} else if existing != nil {
		if existing.Action != "supersede" || existing.RequestHash != requestHash {
			return fmt.Errorf("%w: remember supersession key reused with a different request", ErrIdempotencyConflict)
		}
		return nil
	}
	// Remember supersessions used the submission key before lifecycle keys were
	// namespaced. Replay that exact legacy operation without reapplying events.
	if legacy, err := loadEvidenceLifecycleOperation(ctx, tx, scope.TeamID, scope.OwnerProfileID, idempotencyKey); err != nil {
		return err
	} else if legacy != nil && legacy.Action == "supersede" && legacy.RequestHash == requestHash && legacy.ReplacementIngestID == scope.IngestID {
		return nil
	}
	targetIDs := make([]string, 0, len(supersessions))
	for _, intent := range supersessions {
		targetIDs = append(targetIDs, intent.TargetFragmentID)
	}
	if err := lockEvidenceLifecycleTargetIDs(ctx, tx, scope.TeamID, targetIDs); err != nil {
		return err
	}
	operation := evidenceLifecycleOperationInput{
		TeamID:              scope.TeamID,
		OwnerProfileID:      scope.OwnerProfileID,
		Action:              "supersede",
		EvidenceIDs:         targetIDs,
		IdempotencyKey:      lifecycleKey,
		RequestHash:         requestHash,
		ReplacementIngestID: scope.IngestID,
	}
	planned, err := planEvidenceLifecycle(ctx, tx, operation)
	if err != nil {
		return err
	}
	operationID, err := insertEvidenceLifecycleOperation(ctx, tx, operation, planned)
	if err != nil {
		return err
	}
	if err := insertRememberSupersessionEvents(ctx, tx, operation, operationID, planned.SpaceID, supersessions); err != nil {
		return err
	}
	return applyEvidenceLifecycleEffects(ctx, tx, operation, operationID, planned)
}

func rememberSupersessionLifecycleKey(ingestID string) string {
	return "remember:supersession:" + strings.TrimSpace(ingestID)
}

func loadRememberSubmissionIdentity(ctx context.Context, tx *gorm.DB, scope SubmissionAssessmentRunScope) (string, string, error) {
	row := tx.WithContext(ctx).Raw(`
		SELECT request_hash, idempotency_key
		FROM knowledge_ingests
		WHERE team_id = ?::uuid AND ingest_id = ?::uuid AND owner_profile_id = ?::uuid
	`, scope.TeamID, scope.IngestID, scope.OwnerProfileID).Row()
	var requestHash, idempotencyKey string
	if err := row.Scan(&requestHash, &idempotencyKey); err != nil {
		return "", "", err
	}
	return requestHash, idempotencyKey, nil
}

func insertRememberSupersessionEvents(
	ctx context.Context,
	tx *gorm.DB,
	input evidenceLifecycleOperationInput,
	operationID string,
	spaceID string,
	intents []rememberSupersessionIntent,
) error {
	for _, intent := range intents {
		result := tx.WithContext(ctx).Exec(`
			INSERT INTO evidence_lifecycle_events (
			    team_id, lifecycle_operation_id, target_fragment_id,
			    replacement_fragment_id, owner_profile_id, action, space_id
			) VALUES (?::uuid, ?::uuid, ?::uuid, ?::uuid, ?::uuid, 'supersede', ?::uuid)
		`, input.TeamID, operationID, intent.TargetFragmentID, intent.FragmentID, input.OwnerProfileID, spaceID)
		if result.Error != nil {
			if isPostgresUniqueConstraint(result.Error, "evidence_lifecycle_events_terminal_target_unique") {
				return fmt.Errorf("%w: evidence target is already terminal", ErrEvidenceLifecycleConflict)
			}
			return result.Error
		}
	}
	return nil
}

func jsonUnmarshalObject(raw []byte, target *map[string]any) error {
	if len(raw) == 0 {
		*target = map[string]any{}
		return nil
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	if value == nil {
		value = map[string]any{}
	}
	*target = value
	return nil
}
