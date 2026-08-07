package skillpackservice

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
)

var ErrInvalidArtifact = errors.New("invalid memory pack artifact")

func canonicalMemoryPackArtifact(artifact MemoryPackArtifact) ([]byte, string, error) {
	artifact = normalizeMemoryPackArtifact(artifact)
	if err := validateMemoryPackArtifact(artifact); err != nil {
		return nil, "", err
	}
	hashable := artifact
	hashable.ContentSHA256 = ""
	data, err := json.Marshal(hashable)
	if err != nil {
		return nil, "", fmt.Errorf("memory pack canonicalize: %w", err)
	}
	sum := sha256.Sum256(data)
	return data, hex.EncodeToString(sum[:]), nil
}

func marshalMemoryPackArtifact(artifact MemoryPackArtifact) ([]byte, error) {
	artifact = normalizeMemoryPackArtifact(artifact)
	if err := validateMemoryPackArtifact(artifact); err != nil {
		return nil, err
	}
	data, err := json.Marshal(artifact)
	if err != nil {
		return nil, fmt.Errorf("memory pack marshal: %w", err)
	}
	return data, nil
}

func validateMemoryPackArtifact(artifact MemoryPackArtifact) error {
	if artifact.Format != MemoryPackFormat {
		return fmt.Errorf("%w: format must be %q", ErrInvalidArtifact, MemoryPackFormat)
	}
	if strings.TrimSpace(artifact.Name) == "" {
		return fmt.Errorf("%w: name is required", ErrInvalidArtifact)
	}
	if len(artifact.Name) > 256 {
		return fmt.Errorf("%w: name exceeds 256 characters", ErrInvalidArtifact)
	}
	if len(artifact.Description) > 1024 {
		return fmt.Errorf("%w: description exceeds 1024 characters", ErrInvalidArtifact)
	}
	if len(artifact.Relationships) == 0 {
		return fmt.Errorf("%w: relationships is required", ErrInvalidArtifact)
	}
	seen := map[string]struct{}{}
	for i, item := range artifact.Relationships {
		if err := validateMemoryPackRelationship(item); err != nil {
			return fmt.Errorf("%w: relationships[%d]: %v", ErrInvalidArtifact, i, err)
		}
		if _, exists := seen[item.ItemID]; exists {
			return fmt.Errorf("%w: relationships[%d]: duplicate item_id %q", ErrInvalidArtifact, i, item.ItemID)
		}
		seen[item.ItemID] = struct{}{}
	}
	evidence := map[string]struct{}{}
	for i, item := range artifact.Evidence {
		id := strings.TrimSpace(item.EvidenceID)
		if id == "" {
			return fmt.Errorf("%w: evidence[%d]: evidence_id is required", ErrInvalidArtifact, i)
		}
		if strings.TrimSpace(item.Content) == "" {
			return fmt.Errorf("%w: evidence[%d]: content is required", ErrInvalidArtifact, i)
		}
		if _, exists := evidence[id]; exists {
			return fmt.Errorf("%w: evidence[%d]: duplicate evidence_id %q", ErrInvalidArtifact, i, id)
		}
		evidence[id] = struct{}{}
	}
	for i, support := range artifact.EvidenceSupports {
		if _, exists := seen[support.RelationshipItemID]; !exists {
			return fmt.Errorf("%w: evidence_supports[%d]: relationship item %q is missing", ErrInvalidArtifact, i, support.RelationshipItemID)
		}
		if _, exists := evidence[support.EvidenceID]; !exists {
			return fmt.Errorf("%w: evidence_supports[%d]: evidence %q is missing", ErrInvalidArtifact, i, support.EvidenceID)
		}
	}
	return nil
}

func validateMemoryPackRelationship(item MemoryPackRelationship) error {
	if strings.TrimSpace(item.ItemID) == "" {
		return errors.New("item_id is required")
	}
	if strings.TrimSpace(item.PredicateKey) == "" {
		return errors.New("predicate_key is required")
	}
	if utf8.RuneCountInString(item.PredicateKey) > 128 {
		return errors.New("predicate_key exceeds 128 characters")
	}
	if item.PredicateVersion < 1 {
		return errors.New("predicate_version must be greater than zero")
	}
	if strings.TrimSpace(item.Subject.Ref) == "" || strings.TrimSpace(item.Subject.DisplayName) == "" {
		return errors.New("subject ref and display_name are required")
	}
	if strings.TrimSpace(item.Object.Ref) == "" {
		return errors.New("object ref is required")
	}
	if item.Object.Kind == "value" {
		if strings.TrimSpace(item.Object.Value) == "" || strings.TrimSpace(item.Object.ValueType) == "" {
			return errors.New("object value and value_type are required")
		}
		if _, err := memoryPackCanonicalValue(item.Object); err != nil {
			return err
		}
	} else if strings.TrimSpace(item.Object.DisplayName) == "" {
		return errors.New("object display_name is required")
	}
	return nil
}

func normalizeMemoryPackArtifact(artifact MemoryPackArtifact) MemoryPackArtifact {
	artifact.Format = strings.TrimSpace(artifact.Format)
	artifact.PackID = strings.TrimSpace(artifact.PackID)
	artifact.Name = strings.TrimSpace(artifact.Name)
	artifact.Description = strings.TrimSpace(artifact.Description)
	artifact.Source.TeamID = strings.TrimSpace(artifact.Source.TeamID)
	artifact.Source.ExportedBy = strings.TrimSpace(artifact.Source.ExportedBy)
	for i := range artifact.Relationships {
		item := &artifact.Relationships[i]
		item.ItemID = strings.TrimSpace(item.ItemID)
		item.SourceRelationshipID = strings.TrimSpace(item.SourceRelationshipID)
		item.SourceOwnerProfileID = strings.TrimSpace(item.SourceOwnerProfileID)
		item.PredicateKey = strings.TrimSpace(item.PredicateKey)
		item.Subject = normalizeMemoryPackEndpoint(item.Subject)
		item.Object = normalizeMemoryPackEndpoint(item.Object)
		item.SupportEvidenceIDs = uniqueStrings(item.SupportEvidenceIDs)
	}
	for i := range artifact.Evidence {
		evidence := &artifact.Evidence[i]
		evidence.EvidenceID = strings.TrimSpace(evidence.EvidenceID)
		evidence.SourceType = strings.TrimSpace(evidence.SourceType)
		evidence.Authority = strings.TrimSpace(evidence.Authority)
		evidence.SourceRef = strings.TrimSpace(evidence.SourceRef)
		evidence.SourceKey = strings.TrimSpace(evidence.SourceKey)
		evidence.SourceRevisionID = strings.TrimSpace(evidence.SourceRevisionID)
	}
	return artifact
}

func normalizeMemoryPackEndpoint(endpoint MemoryPackEndpoint) MemoryPackEndpoint {
	endpoint.Ref = strings.TrimSpace(endpoint.Ref)
	endpoint.Kind = strings.TrimSpace(endpoint.Kind)
	if endpoint.Kind == "" {
		endpoint.Kind = "entity"
	}
	endpoint.SourceID = strings.TrimSpace(endpoint.SourceID)
	endpoint.DisplayName = strings.TrimSpace(endpoint.DisplayName)
	endpoint.ValueType = strings.TrimSpace(endpoint.ValueType)
	endpoint.Value = strings.TrimSpace(endpoint.Value)
	return endpoint
}

func memoryPackRelationshipFromTrace(record *repository.RelationshipTraceRecord) MemoryPackRelationship {
	itemID := record.RelationshipID
	if itemID == "" {
		itemID = "rel_" + memoryPackShortHash(record.SemanticGroupKey)
	}
	object := MemoryPackEndpoint{
		Ref:         "object",
		Kind:        "entity",
		SourceID:    record.ObjectEntityID,
		DisplayName: record.ObjectEntityName,
	}
	if record.ObjectValueID != "" {
		object = MemoryPackEndpoint{
			Ref:       "object",
			Kind:      "value",
			SourceID:  record.ObjectValueID,
			ValueType: record.ObjectValueType,
			Value:     record.ObjectValue,
		}
	}
	return MemoryPackRelationship{
		ItemID:                    itemID,
		SourceRelationshipID:      record.RelationshipID,
		SourceRelationshipVersion: record.Version,
		SourceOwnerProfileID:      record.OwnerProfileID,
		Subject: MemoryPackEndpoint{
			Ref:         "subject",
			Kind:        "entity",
			SourceID:    record.SubjectEntityID,
			DisplayName: record.SubjectName,
		},
		PredicateKey:     record.PredicateKey,
		PredicateVersion: record.PredicateVersion,
		Object:           object,
		Polarity:         record.Polarity,
		ScopeKey:         record.ScopeKey,
		Status:           record.Status,
	}
}

func memoryPackCanonicalValue(endpoint MemoryPackEndpoint) (any, error) {
	switch endpoint.ValueType {
	case string(domain.ValueTypeString), string(domain.ValueTypeDate), string(domain.ValueTypeDateTime):
		return endpoint.Value, nil
	case string(domain.ValueTypeNumber):
		value, err := strconv.ParseFloat(endpoint.Value, 64)
		if err != nil {
			return nil, fmt.Errorf("memory pack value %q must be a number", endpoint.Value)
		}
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return nil, fmt.Errorf("memory pack value %q must be finite", endpoint.Value)
		}
		return value, nil
	case string(domain.ValueTypeBoolean):
		value, err := strconv.ParseBool(endpoint.Value)
		if err != nil {
			return nil, fmt.Errorf("memory pack value %q must be a boolean", endpoint.Value)
		}
		return value, nil
	default:
		return nil, fmt.Errorf("memory pack value type %q is unsupported", endpoint.ValueType)
	}
}
