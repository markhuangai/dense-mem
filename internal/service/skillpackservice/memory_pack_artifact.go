package skillpackservice

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
)

func (s *memoryPackService) loadArtifact(ctx context.Context, artifactJSON, rawURL, expectedHash string) (loadedArtifact, error) {
	sourceURL := strings.TrimSpace(rawURL)
	var data []byte
	var err error
	switch {
	case strings.TrimSpace(artifactJSON) != "":
		data = []byte(artifactJSON)
	case sourceURL != "":
		data, err = fetchArtifact(ctx, s.deps.HTTPClient, sourceURL)
	default:
		err = errors.New("memory pack: artifact_json or url is required")
	}
	if err != nil {
		return loadedArtifact{}, err
	}
	parsed, err := parseLoadedMemoryPackArtifact(data)
	if err != nil {
		return loadedArtifact{}, err
	}
	if err := validateExpectedHash(parsed.hash, expectedHash); err != nil {
		return loadedArtifact{}, err
	}
	if parsed.contentSHA256 != "" && parsed.contentSHA256 != parsed.hash {
		return loadedArtifact{}, fmt.Errorf("%w: content_sha256 mismatch", ErrHashMismatch)
	}
	return loadedArtifact{
		artifact: parsed.artifact,
		hash:     parsed.hash,
		format:   parsed.format,
		source:   sourceURL,
		legacy:   parsed.legacy,
	}, nil
}

func parseMemoryPackArtifactJSON(data []byte) (MemoryPackArtifact, bool, error) {
	parsed, err := parseLoadedMemoryPackArtifact(data)
	if err != nil {
		return MemoryPackArtifact{}, false, err
	}
	return parsed.artifact, parsed.legacy, nil
}

type parsedMemoryPackArtifact struct {
	artifact      MemoryPackArtifact
	hash          string
	format        string
	contentSHA256 string
	legacy        bool
}

func parseLoadedMemoryPackArtifact(data []byte) (parsedMemoryPackArtifact, error) {
	if len(data) == 0 {
		return parsedMemoryPackArtifact{}, fmt.Errorf("%w: empty artifact", ErrInvalidArtifact)
	}
	if len(data) > maxArtifactBytes {
		return parsedMemoryPackArtifact{}, fmt.Errorf("%w: artifact exceeds %d bytes", ErrInvalidArtifact, maxArtifactBytes)
	}
	var probe struct {
		Format        string `json:"format"`
		SchemaVersion string `json:"schema_version"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return parsedMemoryPackArtifact{}, fmt.Errorf("%w: %v", ErrInvalidArtifact, err)
	}
	if probe.Format == MemoryPackFormat {
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		var artifact MemoryPackArtifact
		if err := dec.Decode(&artifact); err != nil {
			return parsedMemoryPackArtifact{}, fmt.Errorf("%w: %v", ErrInvalidArtifact, err)
		}
		if err := ensureSingleJSONValue(dec); err != nil {
			return parsedMemoryPackArtifact{}, err
		}
		if err := validateMemoryPackArtifact(artifact); err != nil {
			return parsedMemoryPackArtifact{}, err
		}
		_, hash, err := canonicalMemoryPackArtifact(artifact)
		if err != nil {
			return parsedMemoryPackArtifact{}, err
		}
		return parsedMemoryPackArtifact{
			artifact:      normalizeMemoryPackArtifact(artifact),
			hash:          hash,
			format:        MemoryPackFormat,
			contentSHA256: artifact.ContentSHA256,
		}, nil
	}
	if probe.Format == memoryPackV23Format {
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		var artifact memoryPackArtifactV23
		if err := dec.Decode(&artifact); err != nil {
			return parsedMemoryPackArtifact{}, fmt.Errorf("%w: %v", ErrInvalidArtifact, err)
		}
		if err := ensureSingleJSONValue(dec); err != nil {
			return parsedMemoryPackArtifact{}, err
		}
		if err := validateMemoryPackArtifactV23(artifact); err != nil {
			return parsedMemoryPackArtifact{}, err
		}
		_, hash, err := canonicalMemoryPackArtifactV23(artifact)
		if err != nil {
			return parsedMemoryPackArtifact{}, err
		}
		return parsedMemoryPackArtifact{
			artifact:      memoryPackArtifactFromV23(artifact),
			hash:          hash,
			format:        memoryPackV23Format,
			contentSHA256: artifact.ContentSHA256,
		}, nil
	}
	if probe.SchemaVersion == SchemaVersion || probe.SchemaVersion == LegacySchemaVersion {
		pack, err := parseArtifactJSON(data)
		if err != nil {
			return parsedMemoryPackArtifact{}, err
		}
		_, hash, err := canonicalLegacyMemoryPackArtifact(pack)
		if err != nil {
			return parsedMemoryPackArtifact{}, err
		}
		return parsedMemoryPackArtifact{
			artifact: MemoryPackFromLegacy(pack),
			hash:     hash,
			format:   pack.SchemaVersion,
			legacy:   true,
		}, nil
	}
	return parsedMemoryPackArtifact{}, fmt.Errorf(
		"%w: format must be %q or %q, or schema_version must be %q or %q",
		ErrInvalidArtifact,
		MemoryPackFormat,
		memoryPackV23Format,
		SchemaVersion,
		LegacySchemaVersion,
	)
}

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

func canonicalMemoryPackArtifactV23(artifact memoryPackArtifactV23) ([]byte, string, error) {
	artifact = normalizeMemoryPackArtifactV23(artifact)
	if err := validateMemoryPackArtifactV23(artifact); err != nil {
		return nil, "", err
	}
	hashable := artifact
	hashable.ContentSHA256 = ""
	data, err := json.Marshal(hashable)
	if err != nil {
		return nil, "", fmt.Errorf("memory pack v2.3 canonicalize: %w", err)
	}
	sum := sha256.Sum256(data)
	return data, hex.EncodeToString(sum[:]), nil
}

func canonicalLegacyMemoryPackArtifact(pack SkillPack) ([]byte, string, error) {
	if err := validatePack(pack); err != nil {
		return nil, "", err
	}
	data, err := json.Marshal(pack)
	if err != nil {
		return nil, "", fmt.Errorf("legacy memory pack canonicalize: %w", err)
	}
	sum := sha256.Sum256(data)
	return data, hex.EncodeToString(sum[:]), nil
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

func validateMemoryPackArtifactV23(artifact memoryPackArtifactV23) error {
	if artifact.Format != memoryPackV23Format {
		return fmt.Errorf("%w: format must be %q", ErrInvalidArtifact, memoryPackV23Format)
	}
	return validateMemoryPackArtifact(memoryPackArtifactFromV23(artifact))
}

func normalizeMemoryPackArtifactV23(artifact memoryPackArtifactV23) memoryPackArtifactV23 {
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
		item.SupportFragmentIDs = uniqueStrings(item.SupportFragmentIDs)
	}
	for i := range artifact.EvidenceFragments {
		fragment := &artifact.EvidenceFragments[i]
		fragment.FragmentID = strings.TrimSpace(fragment.FragmentID)
		fragment.SourceType = strings.TrimSpace(fragment.SourceType)
		fragment.Authority = strings.TrimSpace(fragment.Authority)
		fragment.SourceRef = strings.TrimSpace(fragment.SourceRef)
		fragment.SourceKey = strings.TrimSpace(fragment.SourceKey)
		fragment.SourceRevisionID = strings.TrimSpace(fragment.SourceRevisionID)
	}
	return artifact
}

func memoryPackArtifactFromV23(artifact memoryPackArtifactV23) MemoryPackArtifact {
	artifact = normalizeMemoryPackArtifactV23(artifact)
	converted := MemoryPackArtifact{
		Format:              MemoryPackFormat,
		PackID:              artifact.PackID,
		Name:                artifact.Name,
		Description:         artifact.Description,
		CreatedAt:           artifact.CreatedAt,
		Source:              artifact.Source,
		Extensions:          MemoryPackCopyMap(artifact.Extensions),
		LegacySchemaVersion: artifact.LegacySchemaVersion,
	}
	for _, item := range artifact.Relationships {
		converted.Relationships = append(converted.Relationships, MemoryPackRelationship{
			ItemID:                    item.ItemID,
			SourceRelationshipID:      item.SourceRelationshipID,
			SourceRelationshipVersion: item.SourceRelationshipVersion,
			SourceOwnerProfileID:      item.SourceOwnerProfileID,
			Subject:                   item.Subject,
			PredicateKey:              item.PredicateKey,
			PredicateVersion:          item.PredicateVersion,
			Object:                    item.Object,
			Polarity:                  item.Polarity,
			ScopeKey:                  item.ScopeKey,
			Status:                    item.Status,
			SupportEvidenceIDs:        append([]string(nil), item.SupportFragmentIDs...),
			Metadata:                  MemoryPackCopyMap(item.Metadata),
		})
	}
	for _, fragment := range artifact.EvidenceFragments {
		converted.Evidence = append(converted.Evidence, MemoryPackEvidence{
			EvidenceID:       fragment.FragmentID,
			Content:          fragment.Content,
			ContentHash:      fragment.ContentHash,
			SourceType:       fragment.SourceType,
			Authority:        fragment.Authority,
			SourceRef:        fragment.SourceRef,
			SourceKey:        fragment.SourceKey,
			SourceRevisionID: fragment.SourceRevisionID,
			Labels:           append([]string(nil), fragment.Labels...),
			Metadata:         MemoryPackCopyMap(fragment.Metadata),
		})
	}
	for _, support := range artifact.EvidenceSupports {
		converted.EvidenceSupports = append(converted.EvidenceSupports, MemoryPackEvidenceSupport{
			RelationshipItemID: support.RelationshipItemID,
			EvidenceID:         support.FragmentID,
			Quote:              support.Quote,
			SpanStart:          support.SpanStart,
			SpanEnd:            support.SpanEnd,
			Metadata:           MemoryPackCopyMap(support.Metadata),
		})
	}
	return normalizeMemoryPackArtifact(converted)
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

func inspectMemoryPack(artifact MemoryPackArtifact, hash, format, sourceURL string) *InspectResult {
	result := &InspectResult{
		ArtifactHash: hash,
		Format:       format,
		Name:         artifact.Name,
		Description:  artifact.Description,
		ItemCount:    len(artifact.Relationships),
		SourceURL:    sourceURL,
		Items:        make([]InspectItem, 0, len(artifact.Relationships)),
		SupportSummary: SupportSummary{
			EvidenceCount: len(artifact.Evidence),
			SupportCount:  len(artifact.EvidenceSupports),
		},
	}
	evidence := map[string]struct{}{}
	for _, item := range artifact.Evidence {
		evidence[item.EvidenceID] = struct{}{}
	}
	for _, item := range artifact.Relationships {
		inspected := InspectItem{
			ItemID:               item.ItemID,
			SourceRelationshipID: item.SourceRelationshipID,
			Status:               "ready",
			PredicateKey:         item.PredicateKey,
			Subject:              item.Subject.DisplayName,
			Object:               MemoryPackEndpointText(item.Object),
			SupportEvidenceIDs:   append([]string(nil), item.SupportEvidenceIDs...),
		}
		if !MemoryPackSupportedPredicate(item.PredicateKey) {
			inspected.Status = "needs_review"
			inspected.Severity = "medium"
			inspected.Message = "predicate requires placement review"
			result.DecisionsRequired = append(result.DecisionsRequired, ConflictPrompt{
				ItemID:         item.ItemID,
				Reason:         "predicate is not registered in the destination predicate set",
				AllowedActions: []string{"skip", "import_for_review"},
			})
		}
		for _, evidenceID := range item.SupportEvidenceIDs {
			if _, ok := evidence[evidenceID]; !ok {
				inspected.Status = "needs_review"
				inspected.Severity = "high"
				inspected.Message = "support evidence is not present in artifact"
			}
		}
		result.Items = append(result.Items, inspected)
	}
	return result
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

func MemoryPackFromLegacy(pack SkillPack) MemoryPackArtifact {
	createdAt := time.Now().UTC().Format(time.RFC3339Nano)
	if pack.ExportedAt != nil {
		createdAt = pack.ExportedAt.UTC().Format(time.RFC3339Nano)
	}
	artifact := MemoryPackArtifact{
		Format:              MemoryPackFormat,
		PackID:              "legacy_" + memoryPackShortHash(pack.Name),
		Name:                pack.Name,
		Description:         pack.Description,
		CreatedAt:           createdAt,
		Source:              MemoryPackSource{InstallationID: "legacy"},
		LegacySchemaVersion: pack.SchemaVersion,
	}
	for i, item := range pack.Items {
		itemID := item.SourceID
		if itemID == "" {
			itemID = fmt.Sprintf("legacy-item-%d", i)
		}
		relationship := MemoryPackRelationship{
			ItemID:               itemID,
			SourceRelationshipID: item.SourceID,
			Subject: MemoryPackEndpoint{
				Ref:         fmt.Sprintf("legacy-subject-%d", i),
				Kind:        "entity",
				DisplayName: item.Subject,
			},
			PredicateKey:     item.Predicate,
			PredicateVersion: 1,
			Object: MemoryPackEndpoint{
				Ref:         fmt.Sprintf("legacy-object-%d", i),
				Kind:        "entity",
				DisplayName: item.Object,
			},
			Status:             string(domain.RelationshipStatusNeedsReview),
			SupportEvidenceIDs: append([]string(nil), item.SupportFragmentIDs...),
			Metadata: map[string]any{
				"legacy_source_kind": item.SourceKind,
				"legacy_source_id":   item.SourceID,
			},
		}
		artifact.Relationships = append(artifact.Relationships, relationship)
	}
	if pack.Support != nil {
		for _, fragment := range pack.Support.Fragments {
			artifact.Evidence = append(artifact.Evidence, MemoryPackEvidence{
				EvidenceID: fragment.FragmentID,
				Content:    fragment.Content,
				SourceType: fragment.SourceType,
				Authority:  fragment.Authority,
				SourceRef:  fragment.Source,
				Labels:     append([]string(nil), fragment.Labels...),
			})
		}
	}
	return normalizeMemoryPackArtifact(artifact)
}
