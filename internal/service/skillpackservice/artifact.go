package skillpackservice

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/markhuangai/dense-mem/internal/domain"
)

const (
	maxArtifactBytes = 1 << 20
)

var (
	ErrInvalidArtifact = errors.New("invalid skill pack artifact")
	ErrHashMismatch    = errors.New("skill pack hash mismatch")
)

func canonicalArtifact(pack SkillPack) ([]byte, string, error) {
	if err := validatePack(pack); err != nil {
		return nil, "", err
	}
	data, err := json.Marshal(pack)
	if err != nil {
		return nil, "", fmt.Errorf("skill pack canonicalize: %w", err)
	}
	sum := sha256.Sum256(data)
	return data, hex.EncodeToString(sum[:]), nil
}

func parseArtifactJSON(data []byte) (SkillPack, error) {
	if len(data) == 0 {
		return SkillPack{}, fmt.Errorf("%w: empty artifact", ErrInvalidArtifact)
	}
	if len(data) > maxArtifactBytes {
		return SkillPack{}, fmt.Errorf("%w: artifact exceeds %d bytes", ErrInvalidArtifact, maxArtifactBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var pack SkillPack
	if err := dec.Decode(&pack); err != nil {
		return SkillPack{}, fmt.Errorf("%w: %v", ErrInvalidArtifact, err)
	}
	if err := ensureSingleJSONValue(dec); err != nil {
		return SkillPack{}, err
	}
	if err := validatePack(pack); err != nil {
		return SkillPack{}, err
	}
	return pack, nil
}

func ensureSingleJSONValue(dec *json.Decoder) error {
	var extra any
	if err := dec.Decode(&extra); err == io.EOF {
		return nil
	} else if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidArtifact, err)
	}
	return fmt.Errorf("%w: multiple JSON values", ErrInvalidArtifact)
}

func validatePack(pack SkillPack) error {
	if pack.SchemaVersion != SchemaVersion {
		return fmt.Errorf("%w: schema_version must be %q", ErrInvalidArtifact, SchemaVersion)
	}
	if strings.TrimSpace(pack.Name) == "" {
		return fmt.Errorf("%w: name is required", ErrInvalidArtifact)
	}
	if len(pack.Name) > 256 {
		return fmt.Errorf("%w: name exceeds 256 characters", ErrInvalidArtifact)
	}
	if len(pack.Description) > 1024 {
		return fmt.Errorf("%w: description exceeds 1024 characters", ErrInvalidArtifact)
	}
	if len(pack.Items) == 0 {
		return fmt.Errorf("%w: items is required", ErrInvalidArtifact)
	}
	for i, item := range pack.Items {
		if err := validateItem(item); err != nil {
			return fmt.Errorf("%w: items[%d]: %v", ErrInvalidArtifact, i, err)
		}
	}
	claimsByID, fragmentsByID, err := validateSupport(pack.Support)
	if err != nil {
		return err
	}
	if err := validateItemSupportReferences(pack.Items, claimsByID, fragmentsByID); err != nil {
		return err
	}
	return nil
}

func validateItem(item SkillPackItem) error {
	if strings.TrimSpace(item.Subject) == "" {
		return errors.New("subject is required")
	}
	if len(item.Subject) > 256 {
		return errors.New("subject exceeds 256 characters")
	}
	if strings.TrimSpace(item.Predicate) == "" {
		return errors.New("predicate is required")
	}
	if !allowedPredicate(item.Predicate) {
		return fmt.Errorf("predicate %q is not allowed", item.Predicate)
	}
	if strings.TrimSpace(item.Object) == "" {
		return errors.New("object is required")
	}
	if len(item.Object) > 1024 {
		return errors.New("object exceeds 1024 characters")
	}
	if !allowedSourceKind(item.SourceKind) {
		return fmt.Errorf("source_kind %q is not allowed", item.SourceKind)
	}
	if len(item.SourceID) > 128 {
		return errors.New("source_id exceeds 128 characters")
	}
	if err := validateIDList("support_claim_ids", item.SupportClaimIDs); err != nil {
		return err
	}
	if err := validateIDList("support_fragment_ids", item.SupportFragmentIDs); err != nil {
		return err
	}
	return nil
}

func validateSupport(support *SkillPackSupport) (map[string]SkillPackSupportClaim, map[string]struct{}, error) {
	claimsByID := map[string]SkillPackSupportClaim{}
	fragmentsByID := map[string]struct{}{}
	if support == nil {
		return claimsByID, fragmentsByID, nil
	}
	for i, fragment := range support.Fragments {
		if err := validateSupportFragment(fragment); err != nil {
			return nil, nil, fmt.Errorf("%w: support.fragments[%d]: %v", ErrInvalidArtifact, i, err)
		}
		if _, exists := fragmentsByID[fragment.FragmentID]; exists {
			return nil, nil, fmt.Errorf("%w: support.fragments[%d]: duplicate fragment_id %q", ErrInvalidArtifact, i, fragment.FragmentID)
		}
		fragmentsByID[fragment.FragmentID] = struct{}{}
	}
	for i, claim := range support.Claims {
		if err := validateSupportClaim(claim); err != nil {
			return nil, nil, fmt.Errorf("%w: support.claims[%d]: %v", ErrInvalidArtifact, i, err)
		}
		if _, exists := claimsByID[claim.ClaimID]; exists {
			return nil, nil, fmt.Errorf("%w: support.claims[%d]: duplicate claim_id %q", ErrInvalidArtifact, i, claim.ClaimID)
		}
		for _, fragmentID := range claim.SupportedBy {
			if _, exists := fragmentsByID[fragmentID]; !exists {
				return nil, nil, fmt.Errorf("%w: support.claims[%d]: supported_by references missing fragment %q", ErrInvalidArtifact, i, fragmentID)
			}
		}
		claimsByID[claim.ClaimID] = claim
	}
	return claimsByID, fragmentsByID, nil
}

func validateSupportClaim(claim SkillPackSupportClaim) error {
	if strings.TrimSpace(claim.ClaimID) == "" {
		return errors.New("claim_id is required")
	}
	if len(claim.ClaimID) > 128 {
		return errors.New("claim_id exceeds 128 characters")
	}
	if err := validateTripleFields(claim.Subject, claim.Predicate, claim.Object); err != nil {
		return err
	}
	return validateIDList("supported_by", claim.SupportedBy)
}

func validateSupportFragment(fragment SkillPackSupportFragment) error {
	if strings.TrimSpace(fragment.FragmentID) == "" {
		return errors.New("fragment_id is required")
	}
	if len(fragment.FragmentID) > 128 {
		return errors.New("fragment_id exceeds 128 characters")
	}
	if strings.TrimSpace(fragment.Content) == "" {
		return errors.New("content is required")
	}
	if len(fragment.Content) > 8192 {
		return errors.New("content exceeds 8192 bytes")
	}
	if len(fragment.Source) > 256 {
		return errors.New("source exceeds 256 characters")
	}
	if fragment.SourceType != "" && !domain.SourceType(fragment.SourceType).IsValid() {
		return fmt.Errorf("source_type %q is not allowed", fragment.SourceType)
	}
	if fragment.Authority != "" && !domain.Authority(fragment.Authority).IsValid() {
		return fmt.Errorf("authority %q is not allowed", fragment.Authority)
	}
	if len(fragment.Labels) > 20 {
		return errors.New("labels exceeds 20 items")
	}
	for i, label := range fragment.Labels {
		if strings.TrimSpace(label) == "" {
			return fmt.Errorf("labels[%d] is required", i)
		}
		if len(label) > 64 {
			return fmt.Errorf("labels[%d] exceeds 64 characters", i)
		}
	}
	if !validConfidence(fragment.SourceQuality) {
		return errors.New("source_quality must be in [0,1]")
	}
	return nil
}

func validateTripleFields(subject, predicate, object string) error {
	if strings.TrimSpace(subject) == "" {
		return errors.New("subject is required")
	}
	if len(subject) > 256 {
		return errors.New("subject exceeds 256 characters")
	}
	if strings.TrimSpace(predicate) == "" {
		return errors.New("predicate is required")
	}
	if !allowedPredicate(predicate) {
		return fmt.Errorf("predicate %q is not allowed", predicate)
	}
	if strings.TrimSpace(object) == "" {
		return errors.New("object is required")
	}
	if len(object) > 1024 {
		return errors.New("object exceeds 1024 characters")
	}
	return nil
}

func validateIDList(field string, values []string) error {
	seen := map[string]struct{}{}
	for i, value := range values {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s[%d] is required", field, i)
		}
		if len(value) > 128 {
			return fmt.Errorf("%s[%d] exceeds 128 characters", field, i)
		}
		if _, exists := seen[value]; exists {
			return fmt.Errorf("%s[%d] duplicates %q", field, i, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validateItemSupportReferences(items []SkillPackItem, claimsByID map[string]SkillPackSupportClaim, fragmentsByID map[string]struct{}) error {
	for i, item := range items {
		for _, claimID := range item.SupportClaimIDs {
			claim, exists := claimsByID[claimID]
			if !exists {
				return fmt.Errorf("%w: items[%d]: support_claim_ids references missing claim %q", ErrInvalidArtifact, i, claimID)
			}
			if claim.Subject != item.Subject || claim.Predicate != item.Predicate || claim.Object != item.Object {
				return fmt.Errorf("%w: items[%d]: support claim %q does not match item triple", ErrInvalidArtifact, i, claimID)
			}
		}
		for _, fragmentID := range item.SupportFragmentIDs {
			if _, exists := fragmentsByID[fragmentID]; !exists {
				return fmt.Errorf("%w: items[%d]: support_fragment_ids references missing fragment %q", ErrInvalidArtifact, i, fragmentID)
			}
		}
	}
	return nil
}

func validConfidence(value float64) bool {
	return value >= 0 && value <= 1
}

func allowedPredicate(predicate string) bool {
	switch predicate {
	case "has_skill", "knows", "uses":
		return true
	default:
		return false
	}
}

func allowedSourceKind(kind string) bool {
	switch kind {
	case SourceKindFact, SourceKindValidatedClaim, SourceKindManual:
		return true
	default:
		return false
	}
}

func normalizePack(pack SkillPack) SkillPack {
	pack.SchemaVersion = strings.TrimSpace(pack.SchemaVersion)
	pack.Name = strings.TrimSpace(pack.Name)
	pack.Description = strings.TrimSpace(pack.Description)
	if pack.ExportedAt != nil {
		exportedAt := pack.ExportedAt.UTC()
		if exportedAt.IsZero() {
			pack.ExportedAt = nil
		} else {
			pack.ExportedAt = &exportedAt
		}
	}
	for i := range pack.Items {
		pack.Items[i].Subject = strings.TrimSpace(pack.Items[i].Subject)
		pack.Items[i].Predicate = strings.TrimSpace(pack.Items[i].Predicate)
		pack.Items[i].Object = strings.TrimSpace(pack.Items[i].Object)
		pack.Items[i].SourceKind = strings.TrimSpace(pack.Items[i].SourceKind)
		pack.Items[i].SourceID = strings.TrimSpace(pack.Items[i].SourceID)
		normalizeIDList(pack.Items[i].SupportClaimIDs)
		normalizeIDList(pack.Items[i].SupportFragmentIDs)
	}
	if pack.Support != nil {
		for i := range pack.Support.Claims {
			normalizeSupportClaim(&pack.Support.Claims[i])
		}
		for i := range pack.Support.Fragments {
			normalizeSupportFragment(&pack.Support.Fragments[i])
		}
		sort.Slice(pack.Support.Claims, func(i, j int) bool {
			return pack.Support.Claims[i].ClaimID < pack.Support.Claims[j].ClaimID
		})
		sort.Slice(pack.Support.Fragments, func(i, j int) bool {
			return pack.Support.Fragments[i].FragmentID < pack.Support.Fragments[j].FragmentID
		})
		if len(pack.Support.Claims) == 0 && len(pack.Support.Fragments) == 0 {
			pack.Support = nil
		}
	}
	return pack
}

func normalizeIDList(values []string) {
	for i := range values {
		values[i] = strings.TrimSpace(values[i])
	}
	sort.Strings(values)
}

func normalizeSupportClaim(claim *SkillPackSupportClaim) {
	claim.ClaimID = strings.TrimSpace(claim.ClaimID)
	claim.Subject = strings.TrimSpace(claim.Subject)
	claim.Predicate = strings.TrimSpace(claim.Predicate)
	claim.Object = strings.TrimSpace(claim.Object)
	normalizeIDList(claim.SupportedBy)
}

func normalizeSupportFragment(fragment *SkillPackSupportFragment) {
	fragment.FragmentID = strings.TrimSpace(fragment.FragmentID)
	fragment.Content = strings.TrimSpace(fragment.Content)
	fragment.Source = strings.TrimSpace(fragment.Source)
	fragment.SourceType = strings.TrimSpace(fragment.SourceType)
	fragment.Authority = strings.TrimSpace(fragment.Authority)
	for i := range fragment.Labels {
		fragment.Labels[i] = strings.TrimSpace(fragment.Labels[i])
	}
	sort.Strings(fragment.Labels)
}

func validateExpectedHash(actual, expected string) error {
	expected = strings.ToLower(strings.TrimSpace(expected))
	if expected == "" {
		return nil
	}
	if len(expected) != 64 {
		return fmt.Errorf("%w: expected_sha256 must be 64 lowercase hex characters", ErrInvalidArtifact)
	}
	if actual != expected {
		return fmt.Errorf("%w: expected %s got %s", ErrHashMismatch, expected, actual)
	}
	return nil
}
