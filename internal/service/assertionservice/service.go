package assertionservice

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
)

const ProjectionVersion = "semantic-edge-v2"

var relationshipTypePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)

var reservedRelationshipTypes = map[string]struct{}{
	"CONTRADICTS":     {},
	"DECOMPOSED_INTO": {},
	"DERIVED_FROM":    {},
	"MENTIONS":        {},
	"PROMOTES_TO":     {},
	"SUPPORTED_BY":    {},
	"SUPERSEDED_BY":   {},
}

type Bundle struct {
	Entities   []domain.Entity    `json:"entities"`
	Assertions []domain.Assertion `json:"assertions"`
}

type SupersededAssertion struct {
	AssertionID string
	Tier        domain.AssertionTier
	Status      domain.AssertionStatus
}

type WriteResult struct {
	Superseded []SupersededAssertion
}

type Store interface {
	WriteBundle(ctx context.Context, profileID string, bundle Bundle) (WriteResult, error)
}

type Reader interface {
	GetAssertion(ctx context.Context, profileID, assertionID string) (*domain.Assertion, error)
}

type StateStore interface {
	UpdateAssertionState(ctx context.Context, profileID, assertionID string, tier domain.AssertionTier, status domain.AssertionStatus, at time.Time) (*domain.Assertion, WriteResult, error)
}

type StateUpdate struct {
	AssertionID string
	Tier        domain.AssertionTier
	Status      domain.AssertionStatus
}

type BatchStateStore interface {
	UpdateAssertionStates(ctx context.Context, profileID string, updates []StateUpdate, at time.Time) ([]domain.Assertion, WriteResult, error)
}

type LegacyLinker interface {
	LinkLegacyDecomposition(ctx context.Context, profileID string, refs []domain.LegacyMemoryRef, assertionIDs []string, at time.Time) error
}

type LegacyMigrationStore interface {
	FinalizeLegacyMigration(ctx context.Context, profileID string, updates []StateUpdate, refs []domain.LegacyMemoryRef, at time.Time) ([]domain.Assertion, WriteResult, error)
}

type LegacyRefChecker interface {
	MissingLegacyRefs(ctx context.Context, profileID string, refs []domain.LegacyMemoryRef) ([]string, error)
}

type Service struct {
	store Store
}

func New(store Store) *Service {
	return &Service{store: store}
}

func (s *Service) WriteBundle(ctx context.Context, profileID string, bundle Bundle) (WriteResult, error) {
	if s == nil || s.store == nil {
		return WriteResult{}, errors.New("assertion service: store is required")
	}
	if err := ValidateBundle(profileID, bundle); err != nil {
		return WriteResult{}, err
	}
	return s.store.WriteBundle(ctx, profileID, bundle)
}

func (s *Service) GetAssertion(ctx context.Context, profileID, assertionID string) (*domain.Assertion, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("assertion service: store is required")
	}
	reader, ok := s.store.(Reader)
	if !ok {
		return nil, errors.New("assertion service: store does not support reads")
	}
	if strings.TrimSpace(profileID) == "" || strings.TrimSpace(assertionID) == "" {
		return nil, errors.New("assertion service: team_id and assertion_id are required")
	}
	return reader.GetAssertion(ctx, strings.TrimSpace(profileID), strings.TrimSpace(assertionID))
}

func (s *Service) UpdateState(ctx context.Context, profileID, assertionID string, tier domain.AssertionTier, status domain.AssertionStatus, at time.Time) (*domain.Assertion, WriteResult, error) {
	if s == nil || s.store == nil {
		return nil, WriteResult{}, errors.New("assertion service: store is required")
	}
	stateStore, ok := s.store.(StateStore)
	if !ok {
		return nil, WriteResult{}, errors.New("assertion service: store does not support state updates")
	}
	if strings.TrimSpace(profileID) == "" || strings.TrimSpace(assertionID) == "" {
		return nil, WriteResult{}, errors.New("assertion service: team_id and assertion_id are required")
	}
	if !tier.IsValid() || !status.IsValid() {
		return nil, WriteResult{}, errors.New("assertion service: tier or status is invalid")
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	return stateStore.UpdateAssertionState(ctx, strings.TrimSpace(profileID), strings.TrimSpace(assertionID), tier, status, at.UTC())
}

func (s *Service) UpdateStates(ctx context.Context, profileID string, updates []StateUpdate, at time.Time) ([]domain.Assertion, WriteResult, error) {
	if s == nil || s.store == nil {
		return nil, WriteResult{}, errors.New("assertion service: store is required")
	}
	batch, ok := s.store.(BatchStateStore)
	if !ok {
		return nil, WriteResult{}, errors.New("assertion service: store does not support batch state updates")
	}
	profileID = strings.TrimSpace(profileID)
	if profileID == "" || len(updates) == 0 {
		return nil, WriteResult{}, errors.New("assertion service: team_id and updates are required")
	}
	if err := validateStateUpdates(updates); err != nil {
		return nil, WriteResult{}, err
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	return batch.UpdateAssertionStates(ctx, profileID, updates, at.UTC())
}

func (s *Service) LinkLegacyDecomposition(ctx context.Context, profileID string, refs []domain.LegacyMemoryRef, assertionIDs []string, at time.Time) error {
	if s == nil || s.store == nil {
		return errors.New("assertion service: store is required")
	}
	linker, ok := s.store.(LegacyLinker)
	if !ok {
		return errors.New("assertion service: store does not support legacy links")
	}
	if strings.TrimSpace(profileID) == "" || len(refs) == 0 || len(assertionIDs) == 0 {
		return errors.New("assertion service: team_id, legacy refs, and assertion_ids are required")
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	return linker.LinkLegacyDecomposition(ctx, strings.TrimSpace(profileID), refs, assertionIDs, at.UTC())
}

func (s *Service) FinalizeLegacyMigration(ctx context.Context, profileID string, updates []StateUpdate, refs []domain.LegacyMemoryRef, at time.Time) ([]domain.Assertion, WriteResult, error) {
	if s == nil || s.store == nil {
		return nil, WriteResult{}, errors.New("assertion service: store is required")
	}
	migrationStore, ok := s.store.(LegacyMigrationStore)
	if !ok {
		return nil, WriteResult{}, errors.New("assertion service: store does not support legacy migration")
	}
	profileID = strings.TrimSpace(profileID)
	if profileID == "" || len(updates) == 0 || len(refs) == 0 {
		return nil, WriteResult{}, errors.New("assertion service: team_id, updates, and legacy refs are required")
	}
	if err := validateStateUpdates(updates); err != nil {
		return nil, WriteResult{}, err
	}
	if err := validateLegacyRefs(refs); err != nil {
		return nil, WriteResult{}, err
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	return migrationStore.FinalizeLegacyMigration(ctx, profileID, updates, refs, at.UTC())
}

func (s *Service) CheckLegacyRefs(ctx context.Context, profileID string, refs []domain.LegacyMemoryRef) error {
	if s == nil || s.store == nil {
		return errors.New("assertion service: store is required")
	}
	checker, ok := s.store.(LegacyRefChecker)
	if !ok {
		return errors.New("assertion service: store does not support legacy reference checks")
	}
	profileID = strings.TrimSpace(profileID)
	if profileID == "" || len(refs) == 0 {
		return errors.New("assertion service: team_id and legacy refs are required")
	}
	if err := validateLegacyRefs(refs); err != nil {
		return err
	}
	missing, err := checker.MissingLegacyRefs(ctx, profileID, refs)
	if err != nil {
		return err
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("assertion service: legacy refs not found: %s", strings.Join(missing, ", "))
	}
	return nil
}

func validateStateUpdates(updates []StateUpdate) error {
	seen := map[string]struct{}{}
	for i := range updates {
		updates[i].AssertionID = strings.TrimSpace(updates[i].AssertionID)
		if updates[i].AssertionID == "" || !updates[i].Tier.IsValid() || !updates[i].Status.IsValid() {
			return fmt.Errorf("assertion service: update[%d] is invalid", i)
		}
		if _, exists := seen[updates[i].AssertionID]; exists {
			return fmt.Errorf("assertion service: duplicate assertion_id %q", updates[i].AssertionID)
		}
		seen[updates[i].AssertionID] = struct{}{}
	}
	return nil
}

func validateLegacyRefs(refs []domain.LegacyMemoryRef) error {
	seen := map[string]struct{}{}
	for i, ref := range refs {
		kind := strings.ToLower(strings.TrimSpace(ref.Type))
		switch kind {
		case "fragment", "claim", "fact", "dream":
		default:
			return fmt.Errorf("assertion service: legacy ref[%d] type is invalid", i)
		}
		id := strings.TrimSpace(ref.ID)
		if id == "" {
			return fmt.Errorf("assertion service: legacy ref[%d] id is required", i)
		}
		key := kind + ":" + id
		if _, exists := seen[key]; exists {
			return fmt.Errorf("assertion service: duplicate legacy ref %q", key)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func ValidateBundle(profileID string, bundle Bundle) error {
	profileID = strings.TrimSpace(profileID)
	if profileID == "" {
		return errors.New("assertion bundle: team_id is required")
	}
	if len(bundle.Entities) == 0 {
		return errors.New("assertion bundle: entities are required")
	}
	if len(bundle.Assertions) == 0 {
		return errors.New("assertion bundle: assertions are required")
	}

	entities := make(map[string]struct{}, len(bundle.Entities))
	for i, entity := range bundle.Entities {
		if err := entity.Validate(); err != nil {
			return fmt.Errorf("assertion bundle: entity[%d]: %w", i, err)
		}
		if entity.ProfileID != profileID {
			return fmt.Errorf("assertion bundle: entity[%d] belongs to another team", i)
		}
		if _, exists := entities[entity.EntityID]; exists {
			return fmt.Errorf("assertion bundle: duplicate entity_id %q", entity.EntityID)
		}
		entities[entity.EntityID] = struct{}{}
	}

	assertions := make(map[string]struct{}, len(bundle.Assertions))
	for i, assertion := range bundle.Assertions {
		if err := assertion.Validate(); err != nil {
			return fmt.Errorf("assertion bundle: assertion[%d]: %w", i, err)
		}
		if assertion.ProfileID != profileID {
			return fmt.Errorf("assertion bundle: assertion[%d] belongs to another team", i)
		}
		if _, exists := assertions[assertion.AssertionID]; exists {
			return fmt.Errorf("assertion bundle: duplicate assertion_id %q", assertion.AssertionID)
		}
		assertions[assertion.AssertionID] = struct{}{}
		if _, exists := entities[assertion.SubjectEntityID]; !exists {
			return fmt.Errorf("assertion bundle: assertion[%d] subject is missing", i)
		}
		if assertion.ObjectEntityID != "" {
			if _, exists := entities[assertion.ObjectEntityID]; !exists {
				return fmt.Errorf("assertion bundle: assertion[%d] object is missing", i)
			}
		}
		if !relationshipTypePattern.MatchString(assertion.RelationshipType) {
			return fmt.Errorf("assertion bundle: assertion[%d] relationship type is unsafe", i)
		}
		if _, reserved := reservedRelationshipTypes[assertion.RelationshipType]; reserved {
			return fmt.Errorf("assertion bundle: assertion[%d] relationship type is reserved", i)
		}
		if want := RelationshipType(assertion.PredicateKey); assertion.RelationshipType != want {
			return fmt.Errorf("assertion bundle: assertion[%d] relationship type must be %q", i, want)
		}
	}
	return nil
}

func NormalizeName(value string) string {
	var builder strings.Builder
	separator := false
	for _, r := range strings.ToLower(strings.TrimSpace(value)) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if separator && builder.Len() > 0 {
				builder.WriteByte(' ')
			}
			builder.WriteRune(r)
			separator = false
		case builder.Len() > 0:
			separator = true
		}
	}
	return strings.TrimSpace(builder.String())
}

func NormalizeEntityType(value string) string {
	return normalizeToken(value, 48)
}

func NormalizePredicate(value string) string {
	return normalizeToken(value, 64)
}

func RelationshipType(predicate string) string {
	return strings.ToUpper(NormalizePredicate(predicate))
}

func NewEntity(profileID, name, entityType string, aliases []string) (domain.Entity, error) {
	normalizedName := NormalizeName(name)
	normalizedType := NormalizeEntityType(entityType)
	if normalizedName == "" || normalizedType == "" {
		return domain.Entity{}, errors.New("entity name and type are required")
	}
	normalizedAliases := normalizeAliases(aliases, normalizedName)
	key := strings.Join([]string{profileID, normalizedType, normalizedName}, ":")
	return domain.Entity{
		EntityID:         uuid.NewSHA1(uuid.NameSpaceOID, []byte("dense-mem-entity:"+key)).String(),
		ProfileID:        strings.TrimSpace(profileID),
		CanonicalName:    strings.TrimSpace(name),
		NormalizedName:   normalizedName,
		EntityType:       normalizedType,
		Aliases:          normalizedAliases,
		ResolutionStatus: domain.EntityResolutionCanonical,
		ResolutionConf:   1,
	}, nil
}

func NewValue(profileID string, valueType domain.ValueType, value, display, unit string) (domain.TypedValue, error) {
	normalized := strings.TrimSpace(value)
	if !valueType.IsValid() || normalized == "" {
		return domain.TypedValue{}, errors.New("value type and value are required")
	}
	key := strings.Join([]string{profileID, string(valueType), normalized, strings.TrimSpace(unit)}, ":")
	return domain.TypedValue{
		ValueID:   uuid.NewSHA1(uuid.NameSpaceOID, []byte("dense-mem-value:"+key)).String(),
		ValueType: valueType,
		Value:     normalized,
		Display:   strings.TrimSpace(display),
		Unit:      strings.TrimSpace(unit),
	}, nil
}

func AssertionID(profileID, subjectEntityID, predicateKey, objectKey string, polarity domain.ClaimPolarity, validFrom, validTo *time.Time) string {
	parts := []string{
		strings.TrimSpace(profileID),
		strings.TrimSpace(subjectEntityID),
		NormalizePredicate(predicateKey),
		strings.TrimSpace(objectKey),
		string(polarity),
		formatTimeKey(validFrom),
		formatTimeKey(validTo),
	}
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("dense-mem-assertion:"+strings.Join(parts, ":"))).String()
}

func formatTimeKey(value *time.Time) string {
	if value == nil || value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func normalizeAliases(values []string, canonical string) []string {
	seen := map[string]struct{}{canonical: {}}
	out := make([]string, 0, len(values))
	for _, value := range values {
		normalized := NormalizeName(value)
		if normalized == "" {
			continue
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, strings.TrimSpace(value))
	}
	sort.Slice(out, func(i, j int) bool {
		return NormalizeName(out[i]) < NormalizeName(out[j])
	})
	return out
}

func normalizeToken(value string, maxLength int) string {
	var builder strings.Builder
	underscore := false
	for _, r := range strings.ToLower(strings.TrimSpace(value)) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			builder.WriteRune(r)
			underscore = false
		case builder.Len() > 0 && !underscore:
			builder.WriteByte('_')
			underscore = true
		}
		if builder.Len() >= maxLength {
			break
		}
	}
	return strings.Trim(builder.String(), "_")
}
