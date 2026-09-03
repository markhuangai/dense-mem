package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

type relationshipCorrectionInsert struct {
	State        string
	SpaceID      string
	Token        string
	ExpiresAt    *time.Time
	Candidates   []RelationshipCorrectionCandidate
	Selection    RelationshipCorrectionSelection
	ErrorCode    string
	ErrorMessage string
}

type effectiveRelationshipCorrectionSupport struct {
	RelationshipCorrectionSupport
	SupportID                string
	OccurrenceID             string
	OccurrenceOwnerProfileID string
	EvidenceOwnerProfileID   string
	SourceGroupKey           string
	SourceID                 string
	SourceRevisionID         string
	Quote                    string
	Authority                string
	Metadata                 map[string]any
	IngestID                 string
	SpaceID                  string
}

type relationshipCorrectionResolution struct {
	SubjectEntityID  string
	ObjectEntityID   string
	ObjectValueID    string
	Predicate        *predicateDefinition
	SubjectCreate    *RelationshipCorrectionEntityPatch
	ObjectCreate     *RelationshipCorrectionEntityPatch
	Candidates       []RelationshipCorrectionCandidate
	Selection        RelationshipCorrectionSelection
	RejectionCode    string
	RejectionMessage string
}

type correctionEntityResolution struct {
	EntityID   string
	EntityKind string
	Create     *RelationshipCorrectionEntityPatch
	Candidates []RelationshipCorrectionCandidate
}

func insertRelationshipCorrectionSubmission(
	ctx context.Context,
	tx *gorm.DB,
	input CorrectRelationshipInput,
	requestHash string,
	insert relationshipCorrectionInsert,
) (*relationshipCorrectionSubmissionRow, bool, error) {
	patchJSON, err := json.Marshal(input.Patch)
	if err != nil {
		return nil, false, err
	}
	supportsJSON, err := json.Marshal(input.Supports)
	if err != nil {
		return nil, false, err
	}
	candidates := insert.Candidates
	if candidates == nil {
		candidates = []RelationshipCorrectionCandidate{}
	}
	candidatesJSON, err := json.Marshal(candidates)
	if err != nil {
		return nil, false, err
	}
	selectionJSON, err := json.Marshal(insert.Selection)
	if err != nil {
		return nil, false, err
	}
	submissionID := uuid.NewString()
	var insertedID string
	err = tx.WithContext(ctx).Raw(`
		INSERT INTO relationship_correction_submissions (
		    team_id, submission_id, owner_profile_id, relationship_id,
		    expected_version, request_hash, patch, supports, reason,
		    idempotency_key, processing_state, confirmation_token,
		    confirmation_expires_at, candidates, selection,
		    error_code, error_message, space_id, completed_at
		) VALUES (
		    ?::uuid, ?::uuid, ?::uuid, ?::uuid,
		    ?, ?, ?::jsonb, ?::jsonb, ?,
		    ?, ?, ?, ?, ?::jsonb, ?::jsonb,
		    ?, ?, ?::uuid, CASE WHEN ? IN ('completed', 'rejected', 'failed') THEN now() ELSE NULL END
		)
		ON CONFLICT (team_id, owner_profile_id, idempotency_key) DO NOTHING
		RETURNING submission_id::text
	`, input.TeamID, submissionID, input.OwnerProfileID, input.RelationshipID,
		input.ExpectedVersion, requestHash, string(patchJSON), string(supportsJSON), input.Reason,
		input.IdempotencyKey, insert.State, insert.Token, timeArg(insert.ExpiresAt),
		string(candidatesJSON), string(selectionJSON), insert.ErrorCode, insert.ErrorMessage,
		insert.SpaceID, insert.State).Row().Scan(&insertedID)
	created := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, false, err
	}
	row, err := loadRelationshipCorrectionByIdempotency(ctx, tx, input.TeamID, input.OwnerProfileID, input.IdempotencyKey)
	if err != nil {
		return nil, false, err
	}
	if row.RequestHash != requestHash {
		return nil, false, ErrSemanticIdempotencyConflict
	}
	return row, created, nil
}

func loadEffectiveRelationshipCorrectionSupports(
	ctx context.Context,
	tx *gorm.DB,
	teamID, relationshipID string,
) ([]effectiveRelationshipCorrectionSupport, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		WITH latest AS (
		    SELECT DISTINCT ON (support_id) support_id, decision
		    FROM relationship_support_decision_events
		    WHERE team_id = ?::uuid
		      AND relationship_id = ?::uuid
		    ORDER BY support_id, created_at DESC, support_decision_id DESC
		)
		SELECT support.support_id::text, support.fragment_id::text,
		       COALESCE(support.occurrence_id::text, ''),
		       COALESCE(support.occurrence_owner_profile_id::text, ''),
		       support.evidence_owner_profile_id::text,
		       support.span_start, support.span_end, support.source_group_key,
		       COALESCE(support.source_id::text, ''),
		       COALESCE(support.source_revision_id::text, ''),
		       support.quote, support.authority, support.metadata,
	       fragment.ingest_id::text,
	       support.space_id::text
		FROM relationship_evidence_supports AS support
		JOIN latest ON latest.support_id = support.support_id
		JOIN evidence_fragments AS fragment
		  ON fragment.team_id = support.team_id
		 AND fragment.fragment_id = support.fragment_id
		LEFT JOIN evidence_quarantines AS quarantine
		  ON quarantine.team_id = support.team_id
		 AND quarantine.fragment_id = support.fragment_id
		 AND quarantine.status = 'active'
		LEFT JOIN evidence_sources AS source
		  ON source.team_id = support.team_id
		 AND source.source_id = support.source_id
		LEFT JOIN evidence_lifecycle_events AS lifecycle
		  ON lifecycle.team_id = support.team_id
		 AND lifecycle.target_fragment_id = support.fragment_id
		WHERE support.team_id = ?::uuid
		  AND support.relationship_id = ?::uuid
		  AND latest.decision IN ('grant', 'reinstate')
		  AND quarantine.quarantine_id IS NULL
		  AND lifecycle.lifecycle_event_id IS NULL
		  AND (support.source_id IS NULL OR source.current_revision_id = support.source_revision_id)
		ORDER BY support.fragment_id, support.span_start, support.span_end
	`, teamID, relationshipID, teamID, relationshipID).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []effectiveRelationshipCorrectionSupport
	for rows.Next() {
		var support effectiveRelationshipCorrectionSupport
		var metadataJSON []byte
		if err := rows.Scan(
			&support.SupportID, &support.EvidenceID, &support.OccurrenceID,
			&support.OccurrenceOwnerProfileID, &support.EvidenceOwnerProfileID,
			&support.Start, &support.End,
			&support.SourceGroupKey, &support.SourceID, &support.SourceRevisionID,
			&support.Quote, &support.Authority, &metadataJSON,
			&support.IngestID, &support.SpaceID,
		); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(metadataJSON, &support.Metadata); err != nil {
			return nil, err
		}
		result = append(result, support)
	}
	return result, rows.Err()
}

func loadRelationshipCorrectionIngestID(
	ctx context.Context,
	tx *gorm.DB,
	teamID, relationshipID, ownerProfileID string,
) (string, error) {
	var ingestID string
	err := tx.WithContext(ctx).Raw(`
		SELECT observation.ingest_id::text
		FROM relationship_observations AS observation
		JOIN knowledge_ingests AS ingest
		  ON ingest.team_id = observation.team_id
		 AND ingest.ingest_id = observation.ingest_id
		 AND ingest.owner_profile_id = observation.owner_profile_id
		WHERE observation.team_id = ?::uuid
		  AND observation.relationship_id = ?::uuid
		  AND observation.owner_profile_id = ?::uuid
		ORDER BY observation.created_at ASC, observation.observation_id ASC
		LIMIT 1
	`, teamID, relationshipID, ownerProfileID).Row().Scan(&ingestID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errors.New("relationship correction requires an owner-owned source ingest")
	}
	return ingestID, err
}

func resolveRelationshipCorrectionPatch(
	ctx context.Context,
	tx *gorm.DB,
	input CorrectRelationshipInput,
	source *RelationshipRecord,
) (*relationshipCorrectionResolution, error) {
	resolution := &relationshipCorrectionResolution{
		SubjectEntityID: source.SubjectEntityID,
		ObjectEntityID:  source.ObjectEntityID,
		ObjectValueID:   source.ObjectValueID,
	}
	var subjectKind, objectKind string
	var err error
	if input.Patch.SubjectEntity != nil {
		resolved, err := resolveCorrectionEntity(ctx, tx, input.TeamID, "subject_entity", input.Patch.SubjectEntity)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			resolution.RejectionCode = "entity_not_found"
			resolution.RejectionMessage = "subject Entity is not active and available to the team"
			return resolution, nil
		}
		if err != nil {
			return nil, err
		}
		if len(resolved.Candidates) > relationshipCorrectionCandidateLimit {
			resolution.RejectionCode = "too_many_entity_candidates"
			resolution.RejectionMessage = "subject Entity name has too many exact candidates"
			return resolution, nil
		}
		resolution.Candidates = append(resolution.Candidates, resolved.Candidates...)
		resolution.SubjectCreate = resolved.Create
		subjectKind = resolved.EntityKind
		if resolved.EntityID != "" {
			resolution.SubjectEntityID = resolved.EntityID
			resolution.Selection.SubjectEntityID = resolved.EntityID
		}
	} else {
		subjectKind, err = loadEntityKind(ctx, tx, input.TeamID, source.SubjectEntityID)
		if err != nil {
			return nil, err
		}
	}

	if input.Patch.ObjectEntity != nil {
		resolved, err := resolveCorrectionEntity(ctx, tx, input.TeamID, "object_entity", input.Patch.ObjectEntity)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			resolution.RejectionCode = "entity_not_found"
			resolution.RejectionMessage = "object Entity is not active and available to the team"
			return resolution, nil
		}
		if err != nil {
			return nil, err
		}
		if len(resolved.Candidates) > relationshipCorrectionCandidateLimit {
			resolution.RejectionCode = "too_many_entity_candidates"
			resolution.RejectionMessage = "object Entity name has too many exact candidates"
			return resolution, nil
		}
		resolution.Candidates = append(resolution.Candidates, resolved.Candidates...)
		resolution.ObjectCreate = resolved.Create
		objectKind = resolved.EntityKind
		if resolved.EntityID != "" {
			resolution.ObjectEntityID = resolved.EntityID
			resolution.ObjectValueID = ""
			resolution.Selection.ObjectEntityID = resolved.EntityID
		}
	} else if source.ObjectEntityID != "" {
		objectKind, err = loadEntityKind(ctx, tx, input.TeamID, source.ObjectEntityID)
		if err != nil {
			return nil, err
		}
	} else {
		objectKind, err = loadValueType(ctx, tx, input.TeamID, source.ObjectValueID)
		if err != nil {
			return nil, err
		}
	}

	if input.Patch.Predicate != nil {
		resolution.Predicate, err = loadLatestActivePredicateDefinition(ctx, tx, input.TeamID, input.Patch.Predicate.Key)
	} else {
		resolution.Predicate, err = loadPredicateDefinition(ctx, tx, input.TeamID, source.PredicateKey, source.PredicateVersion)
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		resolution.RejectionCode = "predicate_not_found"
		resolution.RejectionMessage = "predicate is not registered and active for the team"
		return resolution, nil
	}
	if err != nil {
		return nil, err
	}
	if len(resolution.Predicate.AllowedSubjectKinds) > 0 && !contains(resolution.Predicate.AllowedSubjectKinds, subjectKind) {
		resolution.RejectionCode = "predicate_subject_kind_mismatch"
		resolution.RejectionMessage = fmt.Sprintf("predicate %q does not allow subject kind %q", resolution.Predicate.Key, subjectKind)
		return resolution, nil
	}
	if len(resolution.Predicate.AllowedObjectKinds) > 0 && !contains(resolution.Predicate.AllowedObjectKinds, objectKind) {
		resolution.RejectionCode = "predicate_object_kind_mismatch"
		resolution.RejectionMessage = fmt.Sprintf("predicate %q does not allow object kind %q", resolution.Predicate.Key, objectKind)
		return resolution, nil
	}
	if len(resolution.Candidates) == 0 && resolution.SubjectCreate == nil && resolution.ObjectCreate == nil &&
		resolution.SubjectEntityID == source.SubjectEntityID &&
		resolution.ObjectEntityID == source.ObjectEntityID &&
		resolution.ObjectValueID == source.ObjectValueID &&
		resolution.Predicate.Key == source.PredicateKey &&
		resolution.Predicate.Version == source.PredicateVersion {
		resolution.RejectionCode = "no_change"
		resolution.RejectionMessage = "correction patch does not change the Relationship"
	}
	return resolution, nil
}

func resolveCorrectionEntity(
	ctx context.Context,
	tx *gorm.DB,
	teamID, endpoint string,
	patch *RelationshipCorrectionEntityPatch,
) (*correctionEntityResolution, error) {
	if patch.EntityID != "" {
		candidate, err := loadActiveCorrectionEntity(ctx, tx, teamID, patch.EntityID)
		if err != nil {
			return nil, err
		}
		return &correctionEntityResolution{EntityID: candidate.EntityID, EntityKind: candidate.EntityKind}, nil
	}
	candidates, err := listExactCorrectionEntityCandidates(ctx, tx, teamID, endpoint, patch.Name, patch.EntityKind)
	if err != nil {
		return nil, err
	}
	resolution := &correctionEntityResolution{EntityKind: patch.EntityKind}
	switch len(candidates) {
	case 0:
		copy := *patch
		resolution.Create = &copy
	case 1:
		resolution.EntityID = candidates[0].EntityID
	default:
		resolution.Candidates = candidates
	}
	return resolution, nil
}

func loadActiveCorrectionEntity(
	ctx context.Context,
	tx *gorm.DB,
	teamID, entityID string,
) (*RelationshipCorrectionCandidate, error) {
	var candidate RelationshipCorrectionCandidate
	err := tx.WithContext(ctx).Raw(`
		SELECT entity.entity_id::text, entity.entity_kind,
		       COALESCE(name.display_name, entity.entity_id::text)
		FROM entity_records AS entity
		LEFT JOIN entity_names AS name
		  ON name.team_id = entity.team_id
		 AND name.entity_id = entity.entity_id
		 AND name.name_kind = 'canonical'
		 AND name.valid_to IS NULL
		WHERE entity.team_id = ?::uuid
		  AND entity.entity_id = ?::uuid
		  AND entity.status = 'active'
		LIMIT 1
	`, teamID, entityID).Row().Scan(&candidate.EntityID, &candidate.EntityKind, &candidate.CanonicalName)
	if errors.Is(err, sql.ErrNoRows) {
		err = gorm.ErrRecordNotFound
	}
	return &candidate, err
}

func listExactCorrectionEntityCandidates(
	ctx context.Context,
	tx *gorm.DB,
	teamID, endpoint, name, entityKind string,
) ([]RelationshipCorrectionCandidate, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT entity.entity_id::text, entity.entity_kind, canonical.display_name
		FROM entity_records AS entity
		JOIN entity_names AS canonical
		  ON canonical.team_id = entity.team_id
		 AND canonical.entity_id = entity.entity_id
		 AND canonical.name_kind = 'canonical'
		 AND canonical.valid_to IS NULL
		WHERE entity.team_id = ?::uuid
		  AND entity.entity_kind = ?
		  AND entity.status = 'active'
		  AND canonical.normalized_name = ?
		ORDER BY entity.entity_id
		LIMIT ?
	`, teamID, entityKind, normalizeName(name), relationshipCorrectionCandidateLimit+1).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var candidates []RelationshipCorrectionCandidate
	for rows.Next() {
		candidate := RelationshipCorrectionCandidate{Endpoint: endpoint}
		if err := rows.Scan(&candidate.EntityID, &candidate.EntityKind, &candidate.CanonicalName); err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate)
	}
	return candidates, rows.Err()
}

func loadLatestActivePredicateDefinition(
	ctx context.Context,
	tx *gorm.DB,
	teamID, predicateKey string,
) (*predicateDefinition, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT predicate_key, version, allowed_subject_kinds, allowed_object_kinds,
		       relationship_kind, current_cardinality
		FROM team_predicate_definitions
		WHERE team_id = ?::uuid
		  AND predicate_key = ?
		  AND lifecycle_state = 'active'
		ORDER BY version DESC
		LIMIT 1
	`, teamID, predicateKey).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, gorm.ErrRecordNotFound
	}
	var loaded predicateDefinition
	var subjectKinds, objectKinds pq.StringArray
	if err := rows.Scan(&loaded.Key, &loaded.Version, &subjectKinds, &objectKinds, &loaded.RelationshipKind, &loaded.CurrentCardinality); err != nil {
		return nil, err
	}
	loaded.AllowedSubjectKinds = []string(subjectKinds)
	loaded.AllowedObjectKinds = []string(objectKinds)
	return &loaded, rows.Err()
}

func validateRelationshipCorrectionSelection(
	row *relationshipCorrectionSubmissionRow,
	selection RelationshipCorrectionSelection,
) (RelationshipCorrectionSelection, error) {
	allowed := map[string]map[string]struct{}{}
	for _, candidate := range row.Candidates {
		if allowed[candidate.Endpoint] == nil {
			allowed[candidate.Endpoint] = map[string]struct{}{}
		}
		allowed[candidate.Endpoint][candidate.EntityID] = struct{}{}
	}
	selected := map[string]string{
		"subject_entity": selection.SubjectEntityID,
		"object_entity":  selection.ObjectEntityID,
	}
	for endpoint, candidates := range allowed {
		entityID := selected[endpoint]
		if entityID == "" {
			return RelationshipCorrectionSelection{}, fmt.Errorf("%w: %s selection is required", ErrRelationshipCorrectionConfirmation, endpoint)
		}
		if _, ok := candidates[entityID]; !ok {
			return RelationshipCorrectionSelection{}, fmt.Errorf("%w: %s selection is not a candidate", ErrRelationshipCorrectionConfirmation, endpoint)
		}
	}
	for endpoint, entityID := range selected {
		if entityID != "" && allowed[endpoint] == nil {
			return RelationshipCorrectionSelection{}, fmt.Errorf("%w: %s was not ambiguous", ErrRelationshipCorrectionConfirmation, endpoint)
		}
	}
	return selection, nil
}

func validateSelectedCorrectionEntities(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	candidates []RelationshipCorrectionCandidate,
	selection RelationshipCorrectionSelection,
) error {
	selected := map[string]string{"subject_entity": selection.SubjectEntityID, "object_entity": selection.ObjectEntityID}
	for endpoint := range groupCorrectionCandidates(candidates) {
		candidate, err := loadActiveCorrectionEntity(ctx, tx, teamID, selected[endpoint])
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errRelationshipCorrectionSelectionUnavailable
		}
		if err != nil {
			return err
		}
		if candidate.EntityID != selected[endpoint] {
			return errRelationshipCorrectionSelectionUnavailable
		}
	}
	return nil
}

func groupCorrectionCandidates(candidates []RelationshipCorrectionCandidate) map[string]struct{} {
	groups := map[string]struct{}{}
	for _, candidate := range candidates {
		groups[candidate.Endpoint] = struct{}{}
	}
	return groups
}

func markRelationshipCorrectionConfirmation(
	ctx context.Context,
	tx *gorm.DB,
	row *relationshipCorrectionSubmissionRow,
	idempotencyKey, requestHash string,
	selection RelationshipCorrectionSelection,
) error {
	selectionJSON, err := json.Marshal(selection)
	if err != nil {
		return err
	}
	result := tx.WithContext(ctx).Exec(`
		UPDATE relationship_correction_submissions
		SET processing_state = 'processing', confirmation_round = 1,
		    confirmation_idempotency_key = ?, confirmation_request_hash = ?,
		    selection = ?::jsonb, updated_at = now()
		WHERE team_id = ?::uuid
		  AND submission_id = ?::uuid
		  AND owner_profile_id = ?::uuid
		  AND processing_state = 'awaiting_confirmation'
		  AND confirmation_round = 0
	`, idempotencyKey, requestHash, string(selectionJSON), row.TeamID, row.SubmissionID, row.OwnerProfileID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrRelationshipCorrectionConfirmation
	}
	return nil
}

func rejectRelationshipCorrectionSubmission(
	ctx context.Context,
	tx *gorm.DB,
	row *relationshipCorrectionSubmissionRow,
	code, message, confirmationIdempotency, confirmationHash string,
) error {
	result := tx.WithContext(ctx).Exec(`
		UPDATE relationship_correction_submissions
		SET processing_state = 'rejected', error_code = ?, error_message = ?,
		    confirmation_round = CASE WHEN ? = '' THEN confirmation_round ELSE 1 END,
		    confirmation_idempotency_key = CASE WHEN ? = '' THEN confirmation_idempotency_key ELSE ? END,
		    confirmation_request_hash = CASE WHEN ? = '' THEN confirmation_request_hash ELSE ? END,
		    completed_at = now(), updated_at = now()
		WHERE team_id = ?::uuid
		  AND submission_id = ?::uuid
		  AND owner_profile_id = ?::uuid
		  AND processing_state IN ('processing', 'awaiting_confirmation')
	`, code, message, confirmationIdempotency,
		confirmationIdempotency, confirmationIdempotency,
		confirmationIdempotency, confirmationHash,
		row.TeamID, row.SubmissionID, row.OwnerProfileID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrRelationshipCorrectionStateConflict
	}
	return nil
}

func lockRelationshipCorrectionSource(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	relationshipID string,
	ownerProfileID string,
) (*RelationshipRecord, error) {
	source, err := loadRelationshipRecord(ctx, tx, teamID, relationshipID)
	if err != nil {
		return nil, err
	}
	if source.OwnerProfileID != ownerProfileID {
		return nil, ErrSemanticOwnerMismatch
	}
	if err := lockRelationshipConflictSnapshotScopeForRecord(ctx, tx, source); err != nil {
		return nil, err
	}
	source, err = loadRelationshipRecordForUpdate(ctx, tx, teamID, relationshipID)
	if err != nil {
		return nil, err
	}
	if source.OwnerProfileID != ownerProfileID {
		return nil, ErrSemanticOwnerMismatch
	}
	return source, nil
}

func (r *SemanticRepositoryImpl) applyRelationshipCorrection(
	ctx context.Context,
	tx *gorm.DB,
	row *relationshipCorrectionSubmissionRow,
	source *RelationshipRecord,
	resolution *relationshipCorrectionResolution,
	supports []effectiveRelationshipCorrectionSupport,
) (*CorrectRelationshipResult, error) {
	for _, support := range supports {
		if err := requireSemanticSpaceMatch(source.SpaceID, support.SpaceID); err != nil {
			return nil, err
		}
	}
	subjectEntityID := resolution.SubjectEntityID
	objectEntityID := resolution.ObjectEntityID
	objectValueID := resolution.ObjectValueID
	if resolution.Selection.SubjectEntityID != "" {
		subjectEntityID = resolution.Selection.SubjectEntityID
	}
	if resolution.Selection.ObjectEntityID != "" {
		objectEntityID = resolution.Selection.ObjectEntityID
		objectValueID = ""
	}
	if resolution.SubjectCreate != nil {
		createdID, err := createRelationshipCorrectionEntity(ctx, tx, row, resolution.SubjectCreate)
		if err != nil {
			return nil, err
		}
		subjectEntityID = createdID
		resolution.Selection.SubjectEntityID = createdID
	}
	if resolution.ObjectCreate != nil {
		createdID, err := createRelationshipCorrectionEntity(ctx, tx, row, resolution.ObjectCreate)
		if err != nil {
			return nil, err
		}
		objectEntityID = createdID
		objectValueID = ""
		resolution.Selection.ObjectEntityID = createdID
	}
	if subjectEntityID == source.SubjectEntityID && objectEntityID == source.ObjectEntityID &&
		objectValueID == source.ObjectValueID && resolution.Predicate.Key == source.PredicateKey &&
		resolution.Predicate.Version == source.PredicateVersion {
		if err := rejectRelationshipCorrectionSubmission(ctx, tx, row, "no_change", "correction selection does not change the Relationship", row.ConfirmationIdempotency, row.ConfirmationRequestHash); err != nil {
			return nil, err
		}
		updated, err := loadRelationshipCorrectionSubmission(ctx, tx, row.TeamID, row.OwnerProfileID, row.SubmissionID, false)
		if err != nil {
			return nil, err
		}
		return relationshipCorrectionResultFromRow(updated), nil
	}

	decision := ApplyRelationshipDecisionInput{
		TeamID:           row.TeamID,
		OwnerProfileID:   row.OwnerProfileID,
		SubjectEntityID:  subjectEntityID,
		PredicateKey:     resolution.Predicate.Key,
		PredicateVersion: resolution.Predicate.Version,
		ObjectEntityID:   objectEntityID,
		ObjectValueID:    objectValueID,
		Polarity:         source.Polarity,
		ScopeKey:         source.ScopeKey,
		ValidFrom:        source.ValidFrom,
		ValidTo:          source.ValidTo,
		EvidenceVerdict:  string(domain.VerificationEntailed),
	}
	existing, err := selectRelationshipByIdentity(ctx, tx, decision)
	reused := false
	if err == nil {
		if existing.RelationshipID == source.RelationshipID {
			return r.rejectAppliedRelationshipCorrection(ctx, tx, row, "no_change", "correction resolves to the existing Relationship")
		}
		if existing.OwnerProfileID != row.OwnerProfileID || existing.IdentityAliasOfID != "" ||
			existing.Status != string(domain.RelationshipStatusActive) || existing.SupportCount == 0 {
			return r.rejectAppliedRelationshipCorrection(ctx, tx, row, "inactive_relationship_collision", "corrected Relationship collides with inactive or unsupported history")
		}
		reused = true
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	update := tx.WithContext(ctx).Exec(`
		UPDATE relationship_records
		SET status = 'superseded', recorded_to = now(), version = version + 1, updated_at = now()
		WHERE team_id = ?::uuid
		  AND relationship_id = ?::uuid
		  AND owner_profile_id = ?::uuid
		  AND version = ?
		  AND status = 'active'
		  AND support_count > 0
		  AND identity_alias_of_relationship_id IS NULL
	`, row.TeamID, source.RelationshipID, row.OwnerProfileID, row.ExpectedVersion)
	if update.Error != nil {
		return nil, update.Error
	}
	if update.RowsAffected != 1 {
		return nil, ErrSemanticOwnerMismatch
	}
	if _, err := insertRelationshipTransition(ctx, tx, transitionInput{
		TeamID: row.TeamID, OwnerProfileID: row.OwnerProfileID,
		RelationshipID: source.RelationshipID, FromStatus: source.Status,
		ToStatus: string(domain.RelationshipStatusSuperseded), Reason: "relationship_correction",
		IdempotencyKey: "relationship_correction:" + row.SubmissionID + ":original",
	}); err != nil {
		return nil, err
	}
	correctionIngestID, err := loadRelationshipCorrectionIngestID(ctx, tx, row.TeamID, source.RelationshipID, row.OwnerProfileID)
	if err != nil {
		return nil, err
	}

	var successor *RelationshipRecord
	var verificationEventID string
	for index, support := range supports {
		supportInput := EvidenceSupportInput{
			FragmentID: support.EvidenceID, EvidenceOwnerProfileID: support.EvidenceOwnerProfileID, SourceGroupKey: support.SourceGroupKey,
			OccurrenceID: support.OccurrenceID, OccurrenceOwnerProfileID: support.OccurrenceOwnerProfileID,
			SourceID: support.SourceID, SourceRevisionID: support.SourceRevisionID,
			SpanStart: support.Start, SpanEnd: support.End, Quote: support.Quote,
			Authority: support.Authority, Metadata: support.Metadata,
		}
		copyDecision := decision
		copyDecision.IngestID = correctionIngestID
		copyDecision.SubjectRef = subjectEntityID
		copyDecision.OriginalPredicate = resolution.Predicate.Key
		copyDecision.ObjectRef = objectEntityID
		if copyDecision.ObjectRef == "" {
			copyDecision.ObjectRef = objectValueID
		}
		copyDecision.Rationale = "accepted relationship correction"
		copyDecision.Model = "server_policy"
		copyDecision.ResponseHash = row.RequestHash
		copyDecision.Support = &supportInput
		copyDecision.ObservationMetadata = map[string]any{
			"relationship_correction_submission_id": row.SubmissionID,
			"copied_from_support_id":                support.SupportID,
		}
		copyDecision.RelationshipMetadata = map[string]any{
			"relationship_correction_submission_id": row.SubmissionID,
			"corrects_relationship_id":              source.RelationshipID,
		}
		copyDecision = normalizeApplyRelationshipDecisionInput(copyDecision)
		if err := validateApplyRelationshipDecisionInput(copyDecision); err != nil {
			return nil, err
		}
		applied, err := applyRelationshipDecisionInTx(ctx, tx, copyDecision)
		if err != nil {
			return nil, err
		}
		if applied.Relationship == nil {
			return nil, errors.New("relationship correction did not produce a successor")
		}
		if index == 0 {
			verificationEventID = applied.VerificationEventID
		}
		successor = applied.Relationship
	}
	if successor == nil || verificationEventID == "" {
		return nil, errors.New("relationship correction requires effective support")
	}

	original, err := loadRelationshipRecord(ctx, tx, row.TeamID, source.RelationshipID)
	if err != nil {
		return nil, err
	}
	successor, err = loadRelationshipRecord(ctx, tx, row.TeamID, successor.RelationshipID)
	if err != nil {
		return nil, err
	}
	sourceSpaceID, err := loadRelationshipSpaceID(ctx, tx, row.TeamID, successor.RelationshipID, successor.Version)
	if err != nil {
		return nil, err
	}
	if err := requireSemanticSpaceMatch(source.SpaceID, sourceSpaceID); err != nil {
		return nil, err
	}
	metadataJSON, err := json.Marshal(map[string]any{"submission_id": row.SubmissionID})
	if err != nil {
		return nil, err
	}
	if err := tx.WithContext(ctx).Exec(`
		INSERT INTO relationship_cross_references (
		    team_id, author_profile_id, source_relationship_id,
		    source_relationship_version, target_relationship_id,
		    target_relationship_version, kind, verification_event_id, metadata, space_id
		) VALUES (
		    ?::uuid, ?::uuid, ?::uuid, ?, ?::uuid, ?, 'corrects', ?::uuid, ?::jsonb, ?::uuid
		)
	`, row.TeamID, row.OwnerProfileID, successor.RelationshipID, successor.Version,
		original.RelationshipID, original.Version, verificationEventID, string(metadataJSON), sourceSpaceID).Error; err != nil {
		return nil, err
	}
	patchJSON, err := json.Marshal(row.Patch)
	if err != nil {
		return nil, err
	}
	supportsJSON, err := json.Marshal(row.Supports)
	if err != nil {
		return nil, err
	}
	if err := tx.WithContext(ctx).Exec(`
		INSERT INTO relationship_correction_events (
		    team_id, submission_id, owner_profile_id,
		    original_relationship_id, original_relationship_version,
		    successor_relationship_id, successor_relationship_version,
		    reused_successor, patch, supports, reason, space_id
		) VALUES (
		    ?::uuid, ?::uuid, ?::uuid, ?::uuid, ?, ?::uuid, ?, ?, ?::jsonb, ?::jsonb, ?, ?::uuid
		)
	`, row.TeamID, row.SubmissionID, row.OwnerProfileID,
		original.RelationshipID, original.Version, successor.RelationshipID, successor.Version,
		reused, string(patchJSON), string(supportsJSON), row.Reason, sourceSpaceID).Error; err != nil {
		return nil, err
	}
	if err := applyRelationshipCorrectionSearchDocuments(ctx, tx, row, original, successor); err != nil {
		return nil, err
	}
	selectionJSON, err := json.Marshal(resolution.Selection)
	if err != nil {
		return nil, err
	}
	completed := tx.WithContext(ctx).Exec(`
		UPDATE relationship_correction_submissions
		SET processing_state = 'completed', successor_relationship_id = ?::uuid,
		    reused_successor = ?, selection = ?::jsonb,
		    error_code = '', error_message = '', completed_at = now(), updated_at = now()
		WHERE team_id = ?::uuid
		  AND submission_id = ?::uuid
		  AND owner_profile_id = ?::uuid
		  AND processing_state = 'processing'
	`, successor.RelationshipID, reused, string(selectionJSON), row.TeamID, row.SubmissionID, row.OwnerProfileID)
	if completed.Error != nil {
		return nil, completed.Error
	}
	if completed.RowsAffected != 1 {
		return nil, ErrRelationshipCorrectionConfirmation
	}
	updated, err := loadRelationshipCorrectionSubmission(ctx, tx, row.TeamID, row.OwnerProfileID, row.SubmissionID, false)
	if err != nil {
		return nil, err
	}
	return relationshipCorrectionResultFromRow(updated), nil
}

func (r *SemanticRepositoryImpl) rejectAppliedRelationshipCorrection(
	ctx context.Context,
	tx *gorm.DB,
	row *relationshipCorrectionSubmissionRow,
	code, message string,
) (*CorrectRelationshipResult, error) {
	if err := rejectRelationshipCorrectionSubmission(ctx, tx, row, code, message, row.ConfirmationIdempotency, row.ConfirmationRequestHash); err != nil {
		return nil, err
	}
	updated, err := loadRelationshipCorrectionSubmission(ctx, tx, row.TeamID, row.OwnerProfileID, row.SubmissionID, false)
	if err != nil {
		return nil, err
	}
	return relationshipCorrectionResultFromRow(updated), nil
}

func createRelationshipCorrectionEntity(
	ctx context.Context,
	tx *gorm.DB,
	row *relationshipCorrectionSubmissionRow,
	patch *RelationshipCorrectionEntityPatch,
) (string, error) {
	identityContext, err := json.Marshal(map[string]any{"relationship_correction_submission_id": row.SubmissionID})
	if err != nil {
		return "", err
	}
	metadata, err := json.Marshal(map[string]any{"created_by": "correct_relationship"})
	if err != nil {
		return "", err
	}
	var entityID string
	if err := tx.WithContext(ctx).Raw(`
		INSERT INTO entity_records (team_id, entity_kind, identity_context, metadata, space_id, space_generation)
		VALUES (?::uuid, ?, ?::jsonb, ?::jsonb,
		        ?::uuid,
		        (SELECT generation FROM memory_spaces WHERE id = ?::uuid AND team_id = ?::uuid))
		RETURNING entity_id::text
	`, row.TeamID, patch.EntityKind, string(identityContext), string(metadata), row.SpaceID, row.SpaceID, row.TeamID).Row().Scan(&entityID); err != nil {
		return "", err
	}
	if _, err := insertEntityName(ctx, tx, AddEntityNameInput{
		TeamID: row.TeamID, OwnerProfileID: row.OwnerProfileID,
		EntityID: entityID, DisplayName: patch.Name, NameKind: "canonical",
		Metadata: map[string]any{"relationship_correction_submission_id": row.SubmissionID},
	}); err != nil {
		return "", err
	}
	return entityID, nil
}
