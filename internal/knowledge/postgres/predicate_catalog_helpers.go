package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/lib/pq"
	"gorm.io/gorm"
)

// seedTeamPredicateDefinitions copies the immutable built-in catalog into the
// team catalog inside the caller's existing transaction. Keeping this helper
// in the write owner prevents each semantic writer from maintaining a second
// catalog bootstrap implementation.
func seedTeamPredicateDefinitions(ctx context.Context, tx *gorm.DB, teamID string) error {
	return tx.WithContext(ctx).Exec(`
		INSERT INTO team_predicate_definitions (
		    team_id, predicate_key, version, aliases, allowed_subject_kinds,
		    allowed_object_kinds, relationship_kind, current_cardinality,
		    lifecycle_state, origin, metadata, created_at
		)
		SELECT ?::uuid, predicate_key, version, aliases, allowed_subject_kinds,
		       allowed_object_kinds, relationship_kind, current_cardinality,
		       lifecycle_state, 'built_in',
		       metadata || jsonb_build_object('source', 'predicate_definitions'),
		       created_at
		FROM predicate_definitions
		ON CONFLICT (team_id, predicate_key, version) DO NOTHING
	`, teamID).Error
}

func ensureSemanticPredicateCandidateTx(
	ctx context.Context,
	tx *gorm.DB,
	input EnsureSemanticPredicateCandidateInput,
) (*SemanticReviewPredicateCandidate, error) {
	baseKey := canonicalGeneratedPredicateKey(input.Predicate)
	if err := tx.WithContext(ctx).Exec(`SELECT pg_advisory_xact_lock(hashtext(?))`, input.TeamID+":"+baseKey).Error; err != nil {
		return nil, err
	}
	candidate, err := loadTeamPredicateCandidateByKeyOrAlias(ctx, tx, input.TeamID, baseKey, input.Predicate)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if candidate != nil && candidate.RelationshipKind == input.RelationshipKind {
		return ensureTeamPredicateCandidateKinds(ctx, tx, input, *candidate)
	}
	key := baseKey
	collision := candidate != nil && candidate.RelationshipKind != input.RelationshipKind
	if collision {
		key = collisionGeneratedPredicateKey(baseKey, input.RelationshipKind, input.Predicate)
		if err := tx.WithContext(ctx).Exec(`SELECT pg_advisory_xact_lock(hashtext(?))`, input.TeamID+":"+key).Error; err != nil {
			return nil, err
		}
		candidate, err = loadTeamPredicateCandidateByExactKey(ctx, tx, input.TeamID, key)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if candidate != nil {
			if candidate.RelationshipKind != input.RelationshipKind {
				return nil, fmt.Errorf("predicate key collision %q has incompatible relationship_kind %q", key, candidate.RelationshipKind)
			}
			return ensureTeamPredicateCandidateKinds(ctx, tx, input, *candidate)
		}
	}
	return insertTeamPredicateCandidate(ctx, tx, input, key, collision)
}

func loadTeamPredicateCandidateByKeyOrAlias(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	key string,
	alias string,
) (*SemanticReviewPredicateCandidate, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT predicate_key, version, allowed_subject_kinds, allowed_object_kinds,
		       relationship_kind, current_cardinality, lifecycle_state
		FROM team_predicate_definitions
		WHERE team_id = ?::uuid
		  AND lifecycle_state = 'active'
		  AND (predicate_key = ? OR ? = ANY(aliases))
		ORDER BY CASE WHEN predicate_key = ? THEN 0 ELSE 1 END, version DESC
		LIMIT 1
	`, teamID, key, alias, key).Rows()
	if err != nil {
		return nil, err
	}
	return scanOneTeamPredicateCandidate(rows)
}

func loadTeamPredicateCandidateByExactKey(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	key string,
) (*SemanticReviewPredicateCandidate, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT predicate_key, version, allowed_subject_kinds, allowed_object_kinds,
		       relationship_kind, current_cardinality, lifecycle_state
		FROM team_predicate_definitions
		WHERE team_id = ?::uuid
		  AND predicate_key = ?
		  AND lifecycle_state = 'active'
		ORDER BY version DESC
		LIMIT 1
	`, teamID, key).Rows()
	if err != nil {
		return nil, err
	}
	return scanOneTeamPredicateCandidate(rows)
}

func ensureTeamPredicateCandidateKinds(
	ctx context.Context,
	tx *gorm.DB,
	input EnsureSemanticPredicateCandidateInput,
	candidate SemanticReviewPredicateCandidate,
) (*SemanticReviewPredicateCandidate, error) {
	subjectKinds := unionStringSet(candidate.AllowedSubjectKinds, []string{input.SubjectKind})
	objectKinds := unionStringSet(candidate.AllowedObjectKinds, []string{input.ObjectKind})
	if len(subjectKinds) == len(candidate.AllowedSubjectKinds) && len(objectKinds) == len(candidate.AllowedObjectKinds) {
		return &candidate, nil
	}
	metadata := map[string]any{
		"source":             "generated_predicate_expansion",
		"previous_version":   candidate.Version,
		"original_predicate": input.Predicate,
	}
	for key, value := range input.Metadata {
		metadata[key] = value
	}
	data, err := marshalJSON(metadata)
	if err != nil {
		return nil, err
	}
	rows, err := tx.WithContext(ctx).Raw(`
		INSERT INTO team_predicate_definitions (
		    team_id, predicate_key, version, aliases, allowed_subject_kinds,
		    allowed_object_kinds, relationship_kind, current_cardinality,
		    lifecycle_state, origin, metadata
		) VALUES (
		    ?::uuid, ?, ?, ARRAY[]::text[], ?, ?, ?, ?, 'active', ?, ?::jsonb
		)
		RETURNING predicate_key, version, allowed_subject_kinds, allowed_object_kinds,
		          relationship_kind, current_cardinality, lifecycle_state
	`, input.TeamID, candidate.PredicateKey, candidate.Version+1, pqStringArray(subjectKinds),
		pqStringArray(objectKinds), candidate.RelationshipKind, candidate.CurrentCardinality,
		input.Origin, string(data)).Rows()
	if err != nil {
		return nil, err
	}
	inserted, err := scanOneTeamPredicateCandidate(rows)
	if err != nil {
		return nil, err
	}
	return inserted, nil
}

func insertTeamPredicateCandidate(
	ctx context.Context,
	tx *gorm.DB,
	input EnsureSemanticPredicateCandidateInput,
	key string,
	collision bool,
) (*SemanticReviewPredicateCandidate, error) {
	metadata := map[string]any{
		"source":             "generated_predicate",
		"original_predicate": input.Predicate,
	}
	for name, value := range input.Metadata {
		metadata[name] = value
	}
	data, err := marshalJSON(metadata)
	if err != nil {
		return nil, err
	}
	aliases := []string{}
	if !collision {
		aliases = unionStringSet(aliases, []string{input.Predicate})
	}
	rows, err := tx.WithContext(ctx).Raw(`
		INSERT INTO team_predicate_definitions (
		    team_id, predicate_key, version, aliases, allowed_subject_kinds,
		    allowed_object_kinds, relationship_kind, current_cardinality,
		    lifecycle_state, origin, metadata
		) VALUES (
		    ?::uuid, ?, 1, ?, ?, ?, ?, 'many', 'active', ?, ?::jsonb
		)
		ON CONFLICT (team_id, predicate_key, version) DO NOTHING
		RETURNING predicate_key, version, allowed_subject_kinds, allowed_object_kinds,
		          relationship_kind, current_cardinality, lifecycle_state
	`, input.TeamID, key, pqStringArray(aliases), pqStringArray([]string{input.SubjectKind}),
		pqStringArray([]string{input.ObjectKind}), input.RelationshipKind, input.Origin,
		string(data)).Rows()
	if err != nil {
		return nil, err
	}
	inserted, err := scanOneTeamPredicateCandidate(rows)
	if errors.Is(err, sql.ErrNoRows) {
		return loadTeamPredicateCandidateByExactKey(ctx, tx, input.TeamID, key)
	}
	if err != nil {
		return nil, err
	}
	return inserted, nil
}

func scanOneTeamPredicateCandidate(rows *sql.Rows) (*SemanticReviewPredicateCandidate, error) {
	defer rows.Close()
	candidates, err := scanSemanticReviewPredicateCandidates(rows)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, sql.ErrNoRows
	}
	return &candidates[0], rows.Err()
}

func scanSemanticReviewPredicateCandidates(rows *sql.Rows) ([]SemanticReviewPredicateCandidate, error) {
	out := []SemanticReviewPredicateCandidate{}
	for rows.Next() {
		var candidate SemanticReviewPredicateCandidate
		var subjectKinds pq.StringArray
		var objectKinds pq.StringArray
		if err := rows.Scan(
			&candidate.PredicateKey,
			&candidate.Version,
			&subjectKinds,
			&objectKinds,
			&candidate.RelationshipKind,
			&candidate.CurrentCardinality,
			&candidate.LifecycleState,
		); err != nil {
			return nil, err
		}
		candidate.AllowedSubjectKinds = []string(subjectKinds)
		candidate.AllowedObjectKinds = []string(objectKinds)
		out = append(out, candidate)
	}
	return out, rows.Err()
}

func canonicalGeneratedPredicateKey(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var out []rune
	lastUnderscore := false
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			out = append(out, r)
			lastUnderscore = false
			continue
		}
		if len(out) == 0 || lastUnderscore {
			continue
		}
		out = append(out, '_')
		lastUnderscore = true
	}
	for len(out) > 0 && out[len(out)-1] == '_' {
		out = out[:len(out)-1]
	}
	if len(out) > 64 {
		out = out[:64]
		for len(out) > 0 && out[len(out)-1] == '_' {
			out = out[:len(out)-1]
		}
	}
	if len(out) == 0 {
		return "predicate_" + shortPredicateHash(value, 12)
	}
	return string(out)
}

func collisionGeneratedPredicateKey(base string, relationshipKind string, original string) string {
	suffix := "__" + strings.TrimSpace(relationshipKind) + "_" + shortPredicateHash(original+":"+relationshipKind, 8)
	runes := []rune(base)
	maxBase := 64 - len([]rune(suffix))
	if maxBase < 1 {
		maxBase = 1
	}
	if len(runes) > maxBase {
		runes = runes[:maxBase]
	}
	for len(runes) > 0 && runes[len(runes)-1] == '_' {
		runes = runes[:len(runes)-1]
	}
	if len(runes) == 0 {
		runes = []rune("predicate")
	}
	return string(runes) + suffix
}

func shortPredicateHash(value string, n int) string {
	sum := sha256.Sum256([]byte(value))
	encoded := hex.EncodeToString(sum[:])
	if n <= 0 || n > len(encoded) {
		return encoded
	}
	return encoded[:n]
}

func unionStringSet(left []string, right []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(left)+len(right))
	for _, value := range append(left, right...) {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}
