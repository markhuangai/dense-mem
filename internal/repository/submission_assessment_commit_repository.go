package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

var (
	ErrSubmissionAssessmentNotFound           = errors.New("submission assessment not found")
	ErrSubmissionAssessorAttemptConsumed      = errors.New("submission assessor attempt already consumed")
	ErrSubmissionAssessmentScopeMismatch      = errors.New("submission assessment scope mismatch")
	ErrSubmissionPredicateRegistrationHeld    = errors.New("submission predicate registration requires review")
	ErrSubmissionAssessmentNonPromotable      = errors.New("submission assessment is not promotable")
	ErrSubmissionAssessmentKnownEvidenceStale = errors.New("submission known evidence snapshot is stale")
)

func normalizeRememberCommitScope(scope RememberCommitScope) RememberCommitScope {
	scope.TeamID = strings.TrimSpace(scope.TeamID)
	scope.OwnerProfileID = strings.TrimSpace(scope.OwnerProfileID)
	scope.IngestID = strings.TrimSpace(scope.IngestID)
	return scope
}

func validateRememberCommitScope(scope RememberCommitScope) error {
	for label, value := range map[string]string{
		"team_id": scope.TeamID, "owner_profile_id": scope.OwnerProfileID,
		"ingest_id": scope.IngestID,
	} {
		if _, err := uuid.Parse(value); err != nil {
			return fmt.Errorf("%s is required: %w", label, err)
		}
	}
	return nil
}

func normalizeCommitSubmissionAssessmentInput(input CommitSubmissionAssessmentInput) CommitSubmissionAssessmentInput {
	input.RememberCommitScope = normalizeRememberCommitScope(input.RememberCommitScope)
	input.AssessmentID = strings.TrimSpace(input.AssessmentID)
	for i := range input.Items {
		input.Items[i].FragmentID = strings.TrimSpace(input.Items[i].FragmentID)
		input.Items[i].EvidenceID = strings.TrimSpace(input.Items[i].EvidenceID)
	}
	for i := range input.KnownEvidenceSnapshot {
		item := &input.KnownEvidenceSnapshot[i]
		item.TeamID = strings.TrimSpace(item.TeamID)
		item.EvidenceID = strings.TrimSpace(item.EvidenceID)
		item.FragmentID = strings.TrimSpace(item.FragmentID)
		item.IngestID = strings.TrimSpace(item.IngestID)
		item.OwnerProfileID = strings.TrimSpace(item.OwnerProfileID)
		item.ContentHash = strings.TrimSpace(item.ContentHash)
		item.Authority = strings.TrimSpace(item.Authority)
		item.SourceID = strings.TrimSpace(item.SourceID)
		item.SourceRevisionID = strings.TrimSpace(item.SourceRevisionID)
		item.CurrentSourceRevisionID = strings.TrimSpace(item.CurrentSourceRevisionID)
		item.SpaceID = strings.TrimSpace(item.SpaceID)
	}
	for i := range input.EvidenceConflictResults {
		for j := range input.EvidenceConflictResults[i].Positions {
			input.EvidenceConflictResults[i].Positions[j].EvidenceID = strings.TrimSpace(input.EvidenceConflictResults[i].Positions[j].EvidenceID)
		}
	}
	if input.EvidenceConflictCandidateEvidenceIDs != nil {
		normalizedCandidates := make(map[string][]string, len(input.EvidenceConflictCandidateEvidenceIDs))
		for evidenceID, candidates := range input.EvidenceConflictCandidateEvidenceIDs {
			evidenceID = strings.TrimSpace(evidenceID)
			values := make([]string, 0, len(candidates))
			seen := make(map[string]struct{}, len(candidates))
			for _, candidateID := range candidates {
				candidateID = strings.TrimSpace(candidateID)
				if candidateID == "" {
					continue
				}
				if _, exists := seen[candidateID]; exists {
					continue
				}
				seen[candidateID] = struct{}{}
				values = append(values, candidateID)
			}
			sort.Strings(values)
			normalizedCandidates[evidenceID] = values
		}
		input.EvidenceConflictCandidateEvidenceIDs = normalizedCandidates
	}
	for i := range input.EntityResolutions {
		entry := &input.EntityResolutions[i]
		entry.Resolution = normalizeSubmissionEntityResolution(entry.Resolution)
	}
	for i := range input.RelationshipObservations {
		entry := &input.RelationshipObservations[i]
		entry.RelationshipRef = strings.TrimSpace(entry.RelationshipRef)
		entry.Observation = normalizeSemanticRelationshipDecisionInput(entry.Observation)
	}
	for i := range input.PredicateRegistrations {
		registration := &input.PredicateRegistrations[i]
		registration.RelationshipRef = strings.TrimSpace(registration.RelationshipRef)
		registration.PredicateKey = strings.TrimSpace(registration.PredicateKey)
		registration.SubjectKind = strings.TrimSpace(registration.SubjectKind)
		registration.ObjectKind = strings.TrimSpace(registration.ObjectKind)
		registration.RelationshipKind = strings.TrimSpace(registration.RelationshipKind)
		registration.CurrentCardinality = strings.TrimSpace(registration.CurrentCardinality)
	}
	return input
}

func normalizeSubmissionEntityResolution(input SemanticEntityResolutionInput) SemanticEntityResolutionInput {
	input.MentionRef = strings.TrimSpace(input.MentionRef)
	input.Action = strings.TrimSpace(input.Action)
	input.EntityID = strings.TrimSpace(input.EntityID)
	input.ExactEntityID = strings.TrimSpace(input.ExactEntityID)
	input.EntityKind = strings.TrimSpace(input.EntityKind)
	input.CanonicalName = strings.TrimSpace(input.CanonicalName)
	input.FragmentID = strings.TrimSpace(input.FragmentID)
	input.AssessmentID = strings.TrimSpace(input.AssessmentID)
	return input
}

func validateCommitSubmissionAssessmentInput(input CommitSubmissionAssessmentInput) error {
	if err := validateRememberCommitScope(input.RememberCommitScope); err != nil {
		return err
	}
	if err := validateKnownEvidenceSnapshots(input); err != nil {
		return err
	}
	if _, err := uuid.Parse(input.AssessmentID); err != nil {
		if len(input.Items) > 0 {
			return fmt.Errorf("assessment_id is required: %w", err)
		}
	}
	if len(input.Items) == 0 {
		if len(input.KnownEvidenceSnapshot) != 0 {
			return errors.New("terminal synchronous result cannot carry known evidence")
		}
		if len(input.EntityResolutions) != 0 || len(input.RelationshipObservations) != 0 || len(input.PredicateRegistrations) != 0 {
			return errors.New("terminal synchronous result cannot carry semantic decisions")
		}
		if len(input.EvidenceConflictResults) != 0 {
			return errors.New("terminal synchronous result cannot carry evidence conflicts")
		}
		for _, result := range input.RelationshipResults {
			if result.Disposition != "not_stored" || !submissionRelationshipNotStoredReasonAllowed(strings.TrimSpace(result.Reason)) {
				return fmt.Errorf("terminal synchronous relationship result %q is invalid", result.RelationshipRef)
			}
		}
		return nil
	}
	seenItems := map[string]struct{}{}
	seenEvidenceIDs := map[string]struct{}{}
	for _, item := range input.Items {
		if _, err := uuid.Parse(item.FragmentID); err != nil {
			return fmt.Errorf("fragment_id is required: %w", err)
		}
		if _, exists := seenItems[item.FragmentID]; exists {
			return errors.New("submission assessment item identity is duplicated")
		}
		seenItems[item.FragmentID] = struct{}{}
		if item.EvidenceID != "" {
			if _, exists := seenEvidenceIDs[item.EvidenceID]; exists {
				return errors.New("submission assessment evidence identity is duplicated")
			}
			seenEvidenceIDs[item.EvidenceID] = struct{}{}
		}
	}
	if len(input.EvidenceConflictResults) > EvidenceConflictMaxResults {
		return fmt.Errorf("submission evidence conflicts must contain at most %d results", EvidenceConflictMaxResults)
	}
	for evidenceID, candidates := range input.EvidenceConflictCandidateEvidenceIDs {
		if strings.TrimSpace(evidenceID) == "" {
			return errors.New("submission evidence conflict candidate evidence_id is required")
		}
		for _, candidateID := range candidates {
			if _, err := uuid.Parse(candidateID); err != nil {
				return fmt.Errorf("submission evidence conflict candidate %q is invalid: %w", candidateID, err)
			}
		}
	}
	for conflictIndex, conflict := range input.EvidenceConflictResults {
		if len(conflict.Positions) < 2 || len(conflict.Positions) > EvidenceConflictMaxPositions {
			return fmt.Errorf("submission evidence conflict[%d] must contain between 2 and %d positions", conflictIndex, EvidenceConflictMaxPositions)
		}
		seenPositions := make(map[string]struct{}, len(conflict.Positions))
		for positionIndex, position := range conflict.Positions {
			if strings.TrimSpace(position.EvidenceID) == "" || position.Start < 0 || position.End <= position.Start {
				return fmt.Errorf("submission evidence conflict[%d] position[%d] is invalid", conflictIndex, positionIndex)
			}
			key := fmt.Sprintf("%s:%d:%d", position.EvidenceID, position.Start, position.End)
			if _, exists := seenPositions[key]; exists {
				return fmt.Errorf("submission evidence conflict[%d] position[%d] is duplicated", conflictIndex, positionIndex)
			}
			seenPositions[key] = struct{}{}
		}
	}
	seenEntities := map[string]struct{}{}
	for _, entry := range input.EntityResolutions {
		if _, err := uuid.Parse(entry.Resolution.FragmentID); err != nil {
			return fmt.Errorf("entity resolution fragment_id is required: %w", err)
		}
		if entry.Resolution.Action == string(domain.EntityResolutionAmbiguous) {
			return ErrSubmissionAssessmentNonPromotable
		}
		if err := validateSemanticEntityResolutionInput(entry.Resolution); err != nil {
			return err
		}
		if entry.Resolution.AssessmentID != input.AssessmentID {
			return errors.New("submission entity resolution assessment_id does not match the submission assessment")
		}
		if _, exists := seenEntities[entry.Resolution.MentionRef]; exists {
			return errors.New("submission entity resolution mention_ref is duplicated")
		}
		seenEntities[entry.Resolution.MentionRef] = struct{}{}
	}
	seenRelationships := map[string]struct{}{}
	splitsByRelationship := map[string]map[int]struct{}{}
	registrationByRef := map[string]SubmissionPredicateRegistrationInput{}
	for _, registration := range input.PredicateRegistrations {
		registrationByRef[registration.RelationshipRef] = registration
	}
	for _, entry := range input.RelationshipObservations {
		if _, err := rememberRelationshipFragmentID(entry); err != nil {
			return fmt.Errorf("relationship observation support is required: %w", err)
		}
		if !entry.Observation.AssessorAccepted || entry.Observation.PredicateCandidate != nil || entry.Observation.SuppressSupport {
			return ErrSubmissionAssessmentNonPromotable
		}
		validationObservation := entry.Observation
		if registration, needsRegistration := registrationByRef[validationObservation.Ref]; needsRegistration {
			validationObservation.PredicateKey = registration.PredicateKey
			validationObservation.PredicateVersion = 1
		}
		if err := validateSemanticRelationshipDecisionInput(validationObservation); err != nil {
			return err
		}
		if entry.Observation.AssessmentID != input.AssessmentID {
			return errors.New("submission relationship observation assessment_id does not match the submission assessment")
		}
		if entry.RelationshipRef == "" {
			return errors.New("submission relationship observation relationship_ref is required")
		}
		if entry.SplitIndex < 0 {
			return errors.New("submission relationship observation split_index must be non-negative")
		}
		if _, exists := seenRelationships[entry.Observation.Ref]; exists {
			return errors.New("submission relationship observation ref is duplicated")
		}
		seenRelationships[entry.Observation.Ref] = struct{}{}
		splits := splitsByRelationship[entry.RelationshipRef]
		if splits == nil {
			splits = map[int]struct{}{}
			splitsByRelationship[entry.RelationshipRef] = splits
		}
		if _, exists := splits[entry.SplitIndex]; exists {
			return fmt.Errorf("submission relationship result %q split_index is duplicated", entry.RelationshipRef)
		}
		splits[entry.SplitIndex] = struct{}{}
	}
	seenResults := map[string]struct{}{}
	for index := range input.RelationshipResults {
		result := &input.RelationshipResults[index]
		result.RelationshipRef = strings.TrimSpace(result.RelationshipRef)
		if result.RelationshipRef == "" {
			return errors.New("submission relationship result ref is required")
		}
		if _, exists := seenResults[result.RelationshipRef]; exists {
			return errors.New("submission relationship result ref is duplicated")
		}
		seenResults[result.RelationshipRef] = struct{}{}
		splits := splitsByRelationship[result.RelationshipRef]
		switch result.Disposition {
		case "stored":
			if strings.TrimSpace(result.Reason) != "" || len(result.Splits) != 0 || len(splits) == 0 {
				return fmt.Errorf("submission relationship result %q has invalid stored disposition", result.RelationshipRef)
			}
			for splitIndex := range len(splits) {
				if _, exists := splits[splitIndex]; !exists {
					return fmt.Errorf("submission relationship result %q split_index must be contiguous", result.RelationshipRef)
				}
			}
		case "not_stored":
			if len(splits) != 0 || len(result.Splits) > 0 || strings.TrimSpace(result.Reason) != "not_supported_by_evidence" {
				return fmt.Errorf("submission relationship result %q has invalid not_stored disposition", result.RelationshipRef)
			}
		default:
			return fmt.Errorf("submission relationship result %q has unsupported disposition", result.RelationshipRef)
		}
	}
	for ref := range splitsByRelationship {
		if _, exists := seenResults[ref]; !exists {
			return fmt.Errorf("submission relationship result %q is missing", ref)
		}
	}
	seenRegistrations := map[string]struct{}{}
	for _, registration := range input.PredicateRegistrations {
		if registration.RelationshipRef == "" || registration.PredicateKey == "" {
			return errors.New("submission predicate registration ref and key are required")
		}
		if len([]rune(registration.PredicateKey)) > 128 {
			return errors.New("submission predicate registration key must be at most 128 characters")
		}
		if !contains(domain.EntityKinds(), registration.SubjectKind) || !contains(append(domain.EntityKinds(), domain.ValueTypes()...), registration.ObjectKind) {
			return errors.New("submission predicate registration endpoint kinds are unsupported")
		}
		if !contains(domain.RelationshipKinds(), registration.RelationshipKind) || !contains(domain.CurrentCardinalities(), registration.CurrentCardinality) {
			return errors.New("submission predicate registration policy is unsupported")
		}
		if _, exists := seenRegistrations[registration.RelationshipRef]; exists {
			return errors.New("submission predicate registration relationship_ref is duplicated")
		}
		seenRegistrations[registration.RelationshipRef] = struct{}{}
	}
	return nil
}

func validateKnownEvidenceSnapshots(input CommitSubmissionAssessmentInput) error {
	seen := make(map[string]struct{}, len(input.KnownEvidenceSnapshot))
	for index, item := range input.KnownEvidenceSnapshot {
		if item.TeamID != input.TeamID {
			return fmt.Errorf("known evidence snapshot[%d] team_id does not match the Remember team", index)
		}
		for label, value := range map[string]string{
			"evidence_id": item.EvidenceID, "fragment_id": item.FragmentID,
			"ingest_id": item.IngestID, "owner_profile_id": item.OwnerProfileID,
		} {
			if _, err := uuid.Parse(value); err != nil {
				return fmt.Errorf("known evidence snapshot[%d] %s is required: %w", index, label, err)
			}
		}
		if item.EvidenceID != item.FragmentID {
			return fmt.Errorf("known evidence snapshot[%d] evidence_id and fragment_id must match", index)
		}
		if _, exists := seen[item.EvidenceID]; exists {
			return errors.New("known evidence snapshot identity is duplicated")
		}
		seen[item.EvidenceID] = struct{}{}
		if item.Content == "" || item.ContentHash == "" {
			return fmt.Errorf("known evidence snapshot[%d] content and content_hash are required", index)
		}
		if !domain.Authority(item.Authority).IsValid() {
			return fmt.Errorf("known evidence snapshot[%d] authority is unsupported", index)
		}
		if _, err := uuid.Parse(item.SpaceID); err != nil {
			return fmt.Errorf("known evidence snapshot[%d] space_id is required: %w", index, err)
		}
		if item.SpaceGeneration < 1 {
			return fmt.Errorf("known evidence snapshot[%d] space_generation must be positive", index)
		}
		if (item.SourceID == "") != (item.SourceRevisionID == "") {
			return fmt.Errorf("known evidence snapshot[%d] source and source revision must be provided together", index)
		}
		if item.SourceID != "" {
			if _, err := uuid.Parse(item.SourceID); err != nil {
				return fmt.Errorf("known evidence snapshot[%d] source_id is invalid: %w", index, err)
			}
			if _, err := uuid.Parse(item.SourceRevisionID); err != nil {
				return fmt.Errorf("known evidence snapshot[%d] source_revision_id is invalid: %w", index, err)
			}
			if item.CurrentSourceRevisionID == "" || item.CurrentSourceRevisionID != item.SourceRevisionID {
				return fmt.Errorf("known evidence snapshot[%d] source revision is not current", index)
			}
			if _, err := uuid.Parse(item.CurrentSourceRevisionID); err != nil {
				return fmt.Errorf("known evidence snapshot[%d] current_source_revision_id is invalid: %w", index, err)
			}
		} else if item.CurrentSourceRevisionID != "" {
			return fmt.Errorf("known evidence snapshot[%d] current source revision requires a source", index)
		}
	}
	return nil
}

func cloneSubmissionPayload(input map[string]any) map[string]any {
	output := make(map[string]any, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func resolveSubmissionPredicates(ctx context.Context, tx *gorm.DB, input *CommitSubmissionAssessmentInput) error {
	registrations := make(map[string]SubmissionPredicateRegistrationInput, len(input.PredicateRegistrations))
	for _, registration := range input.PredicateRegistrations {
		registrations[registration.RelationshipRef] = registration
	}
	seen := make(map[string]struct{}, len(input.RelationshipObservations))
	for index := range input.RelationshipObservations {
		observation := &input.RelationshipObservations[index].Observation
		if _, exists := seen[observation.Ref]; exists {
			return errors.New("submission relationship observation ref is duplicated")
		}
		seen[observation.Ref] = struct{}{}
		registration, needsRegistration := registrations[observation.Ref]
		if needsRegistration && observation.ExactPredicateKey != "" {
			return ErrRememberExactReferenceStale
		}
		if !needsRegistration {
			if observation.PredicateKey == "" || observation.PredicateVersion < 1 {
				return ErrSubmissionAssessmentNonPromotable
			}
			if observation.ExactPredicateKey != "" {
				if observation.PredicateKey != observation.ExactPredicateKey {
					return ErrRememberExactReferenceStale
				}
				version, err := loadCurrentExactSubmissionPredicateVersion(ctx, tx, input.TeamID, observation.ExactPredicateKey)
				if err != nil {
					return err
				}
				observation.PredicateVersion = version
			}
			continue
		}
		if observation.PredicateKey != "" {
			return errors.New("submission predicate registration must not include a provider-selected predicate")
		}
		resolved, action, err := resolveSubmissionPredicateRegistration(ctx, tx, input, registration)
		if err != nil {
			return err
		}
		observation.PredicateKey, observation.PredicateVersion = resolved.PredicateKey, resolved.Version
		if err := insertSubmissionPredicateRegistrationEvent(ctx, tx, input, registration.RelationshipRef, action, resolved); err != nil {
			return err
		}
	}
	for ref := range registrations {
		if _, exists := seen[ref]; !exists {
			return errors.New("submission predicate registration has no relationship observation")
		}
	}
	return nil
}

func loadCurrentExactSubmissionPredicateVersion(ctx context.Context, tx *gorm.DB, teamID, predicateKey string) (int, error) {
	var version int
	err := tx.WithContext(ctx).Raw(`
		SELECT version FROM team_predicate_definitions
		WHERE team_id = ?::uuid AND predicate_key = ? AND lifecycle_state = 'active'
		ORDER BY version DESC LIMIT 1
	`, teamID, predicateKey).Row().Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrRememberExactReferenceStale
	}
	if err != nil {
		return 0, err
	}
	return version, nil
}

func resolveSubmissionPredicateRegistration(ctx context.Context, tx *gorm.DB, input *CommitSubmissionAssessmentInput, registration SubmissionPredicateRegistrationInput) (SemanticReviewPredicateCandidate, string, error) {
	requestedKey := strings.TrimSpace(registration.PredicateKey)
	canonicalKey := canonicalGeneratedPredicateKey(requestedKey)
	if err := tx.WithContext(ctx).Exec(`SELECT pg_advisory_xact_lock(hashtext(?))`, input.TeamID+":"+canonicalKey).Error; err != nil {
		return SemanticReviewPredicateCandidate{}, "", err
	}
	loaded, err := loadLatestSubmissionPredicate(ctx, tx, input.TeamID, requestedKey, canonicalKey)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return SemanticReviewPredicateCandidate{}, "", err
	}
	if loaded != nil {
		if loaded.LifecycleState != string(domain.PredicateLifecycleActive) ||
			!semanticPredicateKindAllowed(loaded.AllowedSubjectKinds, registration.SubjectKind) ||
			!semanticPredicateKindAllowed(loaded.AllowedObjectKinds, registration.ObjectKind) ||
			loaded.RelationshipKind != registration.RelationshipKind || loaded.CurrentCardinality != registration.CurrentCardinality {
			return SemanticReviewPredicateCandidate{}, "", ErrSubmissionPredicateRegistrationHeld
		}
		return *loaded, "reused", nil
	}
	metadata, err := marshalJSON(map[string]any{"source": "submission_assessment_registration", "assessment_id": input.AssessmentID, "ingest_id": input.IngestID, "relationship_ref": registration.RelationshipRef, "predicate_policy": domain.PredicatePolicyVersion})
	if err != nil {
		return SemanticReviewPredicateCandidate{}, "", err
	}
	rows, err := tx.WithContext(ctx).Raw(`
		INSERT INTO team_predicate_definitions (
		    team_id, predicate_key, version, aliases, allowed_subject_kinds,
		    allowed_object_kinds, relationship_kind, current_cardinality,
		    lifecycle_state, origin, metadata
		) VALUES (?::uuid, ?, 1, ARRAY[]::text[], ?::text[], ?::text[], ?, ?, 'active', 'submission_registration', ?::jsonb)
		RETURNING predicate_key, version, aliases, allowed_subject_kinds, allowed_object_kinds,
		          relationship_kind, current_cardinality, lifecycle_state
	`, input.TeamID, canonicalKey, pq.Array([]string{registration.SubjectKind}), pq.Array([]string{registration.ObjectKind}), registration.RelationshipKind, registration.CurrentCardinality, string(metadata)).Rows()
	if err != nil {
		return SemanticReviewPredicateCandidate{}, "", err
	}
	defer rows.Close()
	if !rows.Next() {
		return SemanticReviewPredicateCandidate{}, "", rows.Err()
	}
	created, err := scanSubmissionPredicateCandidate(rows)
	if err != nil {
		return SemanticReviewPredicateCandidate{}, "", err
	}
	return created, "created", rows.Err()
}

func loadLatestSubmissionPredicate(ctx context.Context, tx *gorm.DB, teamID, requestedKey, canonicalKey string) (*SemanticReviewPredicateCandidate, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT predicate_key, version, aliases, allowed_subject_kinds, allowed_object_kinds,
		       relationship_kind, current_cardinality, lifecycle_state
		FROM (
			SELECT predicate_key, version, aliases, allowed_subject_kinds, allowed_object_kinds,
			       relationship_kind, current_cardinality, lifecycle_state,
			       row_number() OVER (PARTITION BY predicate_key ORDER BY version DESC) AS version_rank
			FROM team_predicate_definitions WHERE team_id = ?::uuid
		) AS latest
		WHERE version_rank = 1 AND (predicate_key = ? OR predicate_key = ? OR ? = ANY(aliases) OR ? = ANY(aliases))
		ORDER BY CASE WHEN predicate_key = ? THEN 0 WHEN predicate_key = ? THEN 1 ELSE 2 END, predicate_key ASC
		LIMIT 2
	`, teamID, requestedKey, canonicalKey, requestedKey, canonicalKey, requestedKey, canonicalKey).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, sql.ErrNoRows
	}
	candidate, err := scanSubmissionPredicateCandidate(rows)
	if err != nil {
		return nil, err
	}
	if candidate.PredicateKey == canonicalKey || candidate.PredicateKey == requestedKey {
		return &candidate, rows.Err()
	}
	if rows.Next() {
		return nil, ErrSubmissionPredicateRegistrationHeld
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return &candidate, nil
}

func scanSubmissionPredicateCandidate(rows *sql.Rows) (SemanticReviewPredicateCandidate, error) {
	candidate := SemanticReviewPredicateCandidate{}
	var aliases, subjectKinds, objectKinds pq.StringArray
	if err := rows.Scan(&candidate.PredicateKey, &candidate.Version, &aliases, &subjectKinds, &objectKinds, &candidate.RelationshipKind, &candidate.CurrentCardinality, &candidate.LifecycleState); err != nil {
		return SemanticReviewPredicateCandidate{}, err
	}
	candidate.Aliases, candidate.AllowedSubjectKinds, candidate.AllowedObjectKinds = []string(aliases), []string(subjectKinds), []string(objectKinds)
	return candidate, nil
}

func insertSubmissionPredicateRegistrationEvent(ctx context.Context, tx *gorm.DB, input *CommitSubmissionAssessmentInput, relationshipRef, action string, predicate SemanticReviewPredicateCandidate) error {
	metadata, err := marshalJSON(map[string]any{"source": "submission_assessment", "assessment_id": input.AssessmentID})
	if err != nil {
		return err
	}
	return tx.WithContext(ctx).Exec(`
		INSERT INTO predicate_registration_events (
		    team_id, ingest_id, assessment_id, owner_profile_id, relationship_ref,
		    registration_action, predicate_key, predicate_version, metadata
		) VALUES (?::uuid, ?::uuid, ?::uuid, ?::uuid, ?, ?, ?, ?, ?::jsonb)
	`, input.TeamID, input.IngestID, input.AssessmentID, input.OwnerProfileID, relationshipRef, action, predicate.PredicateKey, predicate.Version, string(metadata)).Error
}
