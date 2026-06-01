package skillpackservice

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	maxArtifactBytes = 1 << 20
	maxPackItems     = 100
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
	if len(pack.Items) > maxPackItems {
		return fmt.Errorf("%w: items exceeds maximum of %d", ErrInvalidArtifact, maxPackItems)
	}
	for i, item := range pack.Items {
		if err := validateItem(item); err != nil {
			return fmt.Errorf("%w: items[%d]: %v", ErrInvalidArtifact, i, err)
		}
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
	return nil
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
	for i := range pack.Items {
		pack.Items[i].Subject = strings.TrimSpace(pack.Items[i].Subject)
		pack.Items[i].Predicate = strings.TrimSpace(pack.Items[i].Predicate)
		pack.Items[i].Object = strings.TrimSpace(pack.Items[i].Object)
		pack.Items[i].SourceKind = strings.TrimSpace(pack.Items[i].SourceKind)
	}
	return pack
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
