package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

const submissionPredicateLookupBatchSize = 256

// ValidateSubmissionPredicateRegistrations checks the current team catalog
// without reserving or writing definitions. The final commit still rechecks
// the catalog under its predicate advisory lock.
func (r *Store) ValidateSubmissionPredicateRegistrations(
	ctx context.Context,
	input SubmissionPredicateRegistrationValidationInput,
) ([]SubmissionPredicateRegistrationIssue, error) {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return nil, fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.OwnerProfileID); err != nil {
		return nil, fmt.Errorf("owner_profile_id is required: %w", err)
	}
	if len(input.Registrations) == 0 {
		return nil, nil
	}
	registrations := make([]SubmissionPredicateRegistrationInput, len(input.Registrations))
	copy(registrations, input.Registrations)
	issues := make([]SubmissionPredicateRegistrationIssue, 0)
	for index := range registrations {
		registration := &registrations[index]
		registration.PredicateKey = strings.TrimSpace(registration.PredicateKey)
		registration.SubjectKind = strings.TrimSpace(registration.SubjectKind)
		registration.ObjectKind = strings.TrimSpace(registration.ObjectKind)
		registration.RelationshipKind = strings.TrimSpace(registration.RelationshipKind)
		registration.CurrentCardinality = strings.TrimSpace(registration.CurrentCardinality)
		issues = append(issues, validateSubmissionPredicateRegistrationFields(index, *registration)...)
	}
	if len(issues) > 0 {
		return issues, nil
	}
	if r == nil || r.db == nil {
		return nil, errors.New("knowledge: database is required")
	}
	if r.rls == nil {
		return nil, errors.New("knowledge: rls helper is required")
	}
	err := r.rls.WithTeamProfileReadOnlyRepeatableTx(ctx, r.db, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		var active bool
		if err := tx.WithContext(ctx).Raw(`
			SELECT EXISTS (SELECT 1 FROM teams
			WHERE id = ?::uuid AND status = 'active' AND deleted_at IS NULL)
		`, input.TeamID).Row().Scan(&active); err != nil {
			return err
		}
		if !active {
			return ErrTeamInactive
		}
		requestedKeys := make([]string, len(registrations))
		for index, registration := range registrations {
			requestedKeys[index] = registration.PredicateKey
		}
		matches, err := loadLatestSubmissionPredicateCandidates(ctx, tx, input.TeamID, requestedKeys)
		if err != nil {
			return err
		}
		virtualDefinitions := make(map[string]SemanticReviewPredicateCandidate)
		for index, registration := range registrations {
			canonicalKey := canonicalGeneratedPredicateKey(registration.PredicateKey)
			persisted, err := selectSubmissionPredicateCandidate(matches[index], registration.PredicateKey, canonicalKey)
			if errors.Is(err, ErrSubmissionPredicateRegistrationHeld) {
				issues = append(issues, SubmissionPredicateRegistrationIssue{index, "predicate_key", "matches ambiguous predicate aliases"})
				continue
			}
			if err != nil {
				return err
			}
			resolved := persisted
			if virtual, exists := virtualDefinitions[canonicalKey]; exists {
				candidates := append(append([]SemanticReviewPredicateCandidate(nil), matches[index]...), virtual)
				resolved, err = selectSubmissionPredicateCandidate(candidates, registration.PredicateKey, canonicalKey)
				if errors.Is(err, ErrSubmissionPredicateRegistrationHeld) {
					issues = append(issues, SubmissionPredicateRegistrationIssue{index, "predicate_key", "matches ambiguous predicate aliases"})
					continue
				}
				if err != nil {
					return err
				}
				// A virtual key must not change how preview resolves an existing alias.
				if persisted != nil && resolved != nil && persisted.PredicateKey != resolved.PredicateKey {
					issues = append(issues, SubmissionPredicateRegistrationIssue{index, "predicate_key", "collides with an existing predicate alias"})
					continue
				}
			}
			if resolved != nil {
				if field, message := submissionPredicateRegistrationCompatibility(*resolved, registration); field != "" {
					issues = append(issues, SubmissionPredicateRegistrationIssue{index, field, message})
				}
			} else {
				virtualDefinitions[canonicalKey] = SemanticReviewPredicateCandidate{
					PredicateKey: canonicalKey, Version: 1,
					AllowedSubjectKinds: []string{registration.SubjectKind},
					AllowedObjectKinds:  []string{registration.ObjectKind},
					RelationshipKind:    registration.RelationshipKind,
					CurrentCardinality:  registration.CurrentCardinality,
					LifecycleState:      string(domain.PredicateLifecycleActive),
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("knowledge: validate submission predicate registrations: %w", err)
	}
	return issues, nil
}

func validateSubmissionPredicateRegistrationFields(index int, registration SubmissionPredicateRegistrationInput) []SubmissionPredicateRegistrationIssue {
	issues := make([]SubmissionPredicateRegistrationIssue, 0, 5)
	add := func(field, message string) {
		issues = append(issues, SubmissionPredicateRegistrationIssue{index, field, message})
	}
	if registration.PredicateKey == "" || len([]rune(registration.PredicateKey)) > 128 {
		add("predicate_key", "is required and must be bounded")
	}
	if !contains(domain.EntityKinds(), registration.SubjectKind) {
		add("subject_kind", "is unsupported")
	}
	if !contains(append(domain.EntityKinds(), domain.ValueTypes()...), registration.ObjectKind) {
		add("object_kind", "is unsupported")
	}
	if !contains(domain.RelationshipKinds(), registration.RelationshipKind) {
		add("relationship_kind", "is unsupported")
	}
	if !contains(domain.CurrentCardinalities(), registration.CurrentCardinality) {
		add("current_cardinality", "is unsupported")
	}
	return issues
}

func submissionPredicateRegistrationCompatibility(
	loaded SemanticReviewPredicateCandidate,
	registration SubmissionPredicateRegistrationInput,
) (string, string) {
	if loaded.LifecycleState != string(domain.PredicateLifecycleActive) {
		return "predicate_key", "resolves to a predicate that is not active"
	}
	if !semanticPredicateKindAllowed(loaded.AllowedSubjectKinds, registration.SubjectKind) {
		return "subject_kind", "is incompatible with the existing predicate"
	}
	if !semanticPredicateKindAllowed(loaded.AllowedObjectKinds, registration.ObjectKind) {
		return "object_kind", "is incompatible with the existing predicate"
	}
	if loaded.RelationshipKind != registration.RelationshipKind {
		return "relationship_kind", "is incompatible with the existing predicate"
	}
	if loaded.CurrentCardinality != registration.CurrentCardinality {
		return "current_cardinality", "is incompatible with the existing predicate"
	}
	return "", ""
}

func selectSubmissionPredicateCandidate(
	candidates []SemanticReviewPredicateCandidate,
	requestedKey, canonicalKey string,
) (*SemanticReviewPredicateCandidate, error) {
	for _, candidate := range candidates {
		if candidate.PredicateKey == requestedKey {
			return &candidate, nil
		}
	}
	for _, candidate := range candidates {
		if candidate.PredicateKey == canonicalKey {
			return &candidate, nil
		}
	}
	var alias *SemanticReviewPredicateCandidate
	for _, candidate := range candidates {
		if contains(candidate.Aliases, requestedKey) || contains(candidate.Aliases, canonicalKey) {
			if alias != nil {
				return nil, ErrSubmissionPredicateRegistrationHeld
			}
			matched := candidate
			alias = &matched
		}
	}
	return alias, nil
}

func loadLatestSubmissionPredicateCandidates(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	requestedKeys []string,
) ([][]SemanticReviewPredicateCandidate, error) {
	uniqueKeys := make([]string, 0, len(requestedKeys))
	uniqueIndexes := make([]int, len(requestedKeys))
	indexByKey := make(map[string]int, len(requestedKeys))
	for index, key := range requestedKeys {
		uniqueIndex, exists := indexByKey[key]
		if !exists {
			uniqueIndex = len(uniqueKeys)
			indexByKey[key] = uniqueIndex
			uniqueKeys = append(uniqueKeys, key)
		}
		uniqueIndexes[index] = uniqueIndex
	}
	uniqueMatches := make([][]SemanticReviewPredicateCandidate, len(uniqueKeys))
	for start := 0; start < len(uniqueKeys); start += submissionPredicateLookupBatchSize {
		end := min(start+submissionPredicateLookupBatchSize, len(uniqueKeys))
		requested := uniqueKeys[start:end]
		canonical := make([]string, len(requested))
		for index, key := range requested {
			canonical[index] = canonicalGeneratedPredicateKey(key)
		}
		rows, err := tx.WithContext(ctx).Raw(`
			WITH requested AS (
			    SELECT input.requested_key, input.canonical_key, input.ordinality AS request_index
			    FROM unnest(?::text[], ?::text[]) WITH ORDINALITY
			         AS input(requested_key, canonical_key, ordinality)
			), potential_keys AS (
			    SELECT DISTINCT definition.predicate_key
			    FROM team_predicate_definitions AS definition
			    JOIN requested ON definition.predicate_key = requested.requested_key
			       OR definition.predicate_key = requested.canonical_key
			       OR requested.requested_key = ANY(definition.aliases)
			       OR requested.canonical_key = ANY(definition.aliases)
			    WHERE definition.team_id = ?::uuid
			), latest AS (
			    SELECT DISTINCT ON (definition.predicate_key)
			           definition.predicate_key, definition.version, definition.aliases,
			           definition.allowed_subject_kinds, definition.allowed_object_kinds,
			           definition.relationship_kind, definition.current_cardinality,
			           definition.lifecycle_state
			    FROM team_predicate_definitions AS definition
			    JOIN potential_keys USING (predicate_key)
			    WHERE definition.team_id = ?::uuid
			    ORDER BY definition.predicate_key, definition.version DESC
			), ranked AS (
			    SELECT requested.request_index, latest.*,
			           row_number() OVER (
			               PARTITION BY requested.request_index
			               ORDER BY CASE WHEN latest.predicate_key = requested.requested_key THEN 0
			                             WHEN latest.predicate_key = requested.canonical_key THEN 1 ELSE 2 END,
			                        latest.predicate_key
			           ) AS match_rank
			    FROM requested
			    JOIN latest ON latest.predicate_key = requested.requested_key
			       OR latest.predicate_key = requested.canonical_key
			       OR requested.requested_key = ANY(latest.aliases)
			       OR requested.canonical_key = ANY(latest.aliases)
			)
			SELECT request_index, predicate_key, version, aliases, allowed_subject_kinds,
			       allowed_object_kinds, relationship_kind, current_cardinality, lifecycle_state
			FROM ranked WHERE match_rank <= 2
			ORDER BY request_index, match_rank
		`, pq.Array(requested), pq.Array(canonical), teamID, teamID).Rows()
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var requestIndex int
			var candidate SemanticReviewPredicateCandidate
			var aliases, subjectKinds, objectKinds pq.StringArray
			if err := rows.Scan(&requestIndex, &candidate.PredicateKey, &candidate.Version,
				&aliases, &subjectKinds, &objectKinds, &candidate.RelationshipKind,
				&candidate.CurrentCardinality, &candidate.LifecycleState); err != nil {
				rows.Close()
				return nil, err
			}
			candidate.Aliases = []string(aliases)
			candidate.AllowedSubjectKinds = []string(subjectKinds)
			candidate.AllowedObjectKinds = []string(objectKinds)
			uniqueMatches[start+requestIndex-1] = append(uniqueMatches[start+requestIndex-1], candidate)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	matches := make([][]SemanticReviewPredicateCandidate, len(requestedKeys))
	for index, uniqueIndex := range uniqueIndexes {
		matches[index] = uniqueMatches[uniqueIndex]
	}
	return matches, nil
}

func loadLatestSubmissionPredicate(ctx context.Context, tx *gorm.DB, teamID, requestedKey, canonicalKey string) (*SemanticReviewPredicateCandidate, error) {
	matches, err := loadLatestSubmissionPredicateCandidates(ctx, tx, teamID, []string{requestedKey})
	if err != nil {
		return nil, err
	}
	resolved, err := selectSubmissionPredicateCandidate(matches[0], requestedKey, canonicalKey)
	if err != nil {
		return nil, err
	}
	if resolved == nil {
		return nil, sql.ErrNoRows
	}
	return resolved, nil
}
