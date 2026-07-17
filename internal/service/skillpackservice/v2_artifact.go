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

func (s *v2Service) loadV2Artifact(ctx context.Context, artifactJSON, rawURL, expectedHash string) (v2LoadedArtifact, error) {
	sourceURL := strings.TrimSpace(rawURL)
	var data []byte
	var err error
	switch {
	case strings.TrimSpace(artifactJSON) != "":
		data = []byte(artifactJSON)
	case sourceURL != "":
		data, err = fetchArtifact(ctx, s.deps.HTTPClient, sourceURL)
	default:
		err = errors.New("v2 memory pack: artifact_json or url is required")
	}
	if err != nil {
		return v2LoadedArtifact{}, err
	}
	artifact, legacy, err := parseV2MemoryPackArtifactJSON(data)
	if err != nil {
		return v2LoadedArtifact{}, err
	}
	_, hash, err := canonicalV2MemoryPackArtifact(artifact)
	if err != nil {
		return v2LoadedArtifact{}, err
	}
	if err := validateExpectedHash(hash, expectedHash); err != nil {
		return v2LoadedArtifact{}, err
	}
	if artifact.ContentSHA256 != "" && artifact.ContentSHA256 != hash {
		return v2LoadedArtifact{}, fmt.Errorf("%w: content_sha256 mismatch", ErrHashMismatch)
	}
	return v2LoadedArtifact{artifact: artifact, hash: hash, source: sourceURL, legacy: legacy}, nil
}

func parseV2MemoryPackArtifactJSON(data []byte) (V2MemoryPackArtifact, bool, error) {
	if len(data) == 0 {
		return V2MemoryPackArtifact{}, false, fmt.Errorf("%w: empty artifact", ErrInvalidArtifact)
	}
	if len(data) > maxArtifactBytes {
		return V2MemoryPackArtifact{}, false, fmt.Errorf("%w: artifact exceeds %d bytes", ErrInvalidArtifact, maxArtifactBytes)
	}
	var probe struct {
		Format        string `json:"format"`
		SchemaVersion string `json:"schema_version"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return V2MemoryPackArtifact{}, false, fmt.Errorf("%w: %v", ErrInvalidArtifact, err)
	}
	if probe.Format == V2MemoryPackFormat {
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		var artifact V2MemoryPackArtifact
		if err := dec.Decode(&artifact); err != nil {
			return V2MemoryPackArtifact{}, false, fmt.Errorf("%w: %v", ErrInvalidArtifact, err)
		}
		if err := ensureSingleJSONValue(dec); err != nil {
			return V2MemoryPackArtifact{}, false, err
		}
		if err := validateV2MemoryPackArtifact(artifact); err != nil {
			return V2MemoryPackArtifact{}, false, err
		}
		return normalizeV2MemoryPackArtifact(artifact), false, nil
	}
	if probe.SchemaVersion == SchemaVersion || probe.SchemaVersion == LegacySchemaVersion {
		pack, err := parseArtifactJSON(data)
		if err != nil {
			return V2MemoryPackArtifact{}, false, err
		}
		return v2MemoryPackFromLegacy(pack), true, nil
	}
	return V2MemoryPackArtifact{}, false, fmt.Errorf("%w: format must be %q", ErrInvalidArtifact, V2MemoryPackFormat)
}

func canonicalV2MemoryPackArtifact(artifact V2MemoryPackArtifact) ([]byte, string, error) {
	artifact = normalizeV2MemoryPackArtifact(artifact)
	if err := validateV2MemoryPackArtifact(artifact); err != nil {
		return nil, "", err
	}
	hashable := artifact
	hashable.ContentSHA256 = ""
	data, err := json.Marshal(hashable)
	if err != nil {
		return nil, "", fmt.Errorf("v2 memory pack canonicalize: %w", err)
	}
	sum := sha256.Sum256(data)
	return data, hex.EncodeToString(sum[:]), nil
}

func marshalV2MemoryPackArtifact(artifact V2MemoryPackArtifact) ([]byte, error) {
	artifact = normalizeV2MemoryPackArtifact(artifact)
	if err := validateV2MemoryPackArtifact(artifact); err != nil {
		return nil, err
	}
	data, err := json.Marshal(artifact)
	if err != nil {
		return nil, fmt.Errorf("v2 memory pack marshal: %w", err)
	}
	return data, nil
}

func validateV2MemoryPackArtifact(artifact V2MemoryPackArtifact) error {
	if artifact.Format != V2MemoryPackFormat {
		return fmt.Errorf("%w: format must be %q", ErrInvalidArtifact, V2MemoryPackFormat)
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
		if err := validateV2MemoryPackRelationship(item); err != nil {
			return fmt.Errorf("%w: relationships[%d]: %v", ErrInvalidArtifact, i, err)
		}
		if _, exists := seen[item.ItemID]; exists {
			return fmt.Errorf("%w: relationships[%d]: duplicate item_id %q", ErrInvalidArtifact, i, item.ItemID)
		}
		seen[item.ItemID] = struct{}{}
	}
	fragments := map[string]struct{}{}
	for i, fragment := range artifact.EvidenceFragments {
		id := strings.TrimSpace(fragment.FragmentID)
		if id == "" {
			return fmt.Errorf("%w: evidence_fragments[%d]: fragment_id is required", ErrInvalidArtifact, i)
		}
		if strings.TrimSpace(fragment.Content) == "" {
			return fmt.Errorf("%w: evidence_fragments[%d]: content is required", ErrInvalidArtifact, i)
		}
		if _, exists := fragments[id]; exists {
			return fmt.Errorf("%w: evidence_fragments[%d]: duplicate fragment_id %q", ErrInvalidArtifact, i, id)
		}
		fragments[id] = struct{}{}
	}
	for i, support := range artifact.EvidenceSupports {
		if _, exists := seen[support.RelationshipItemID]; !exists {
			return fmt.Errorf("%w: evidence_supports[%d]: relationship item %q is missing", ErrInvalidArtifact, i, support.RelationshipItemID)
		}
		if _, exists := fragments[support.FragmentID]; !exists {
			return fmt.Errorf("%w: evidence_supports[%d]: fragment %q is missing", ErrInvalidArtifact, i, support.FragmentID)
		}
	}
	return nil
}

func validateV2MemoryPackRelationship(item V2MemoryPackRelationship) error {
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
	} else if strings.TrimSpace(item.Object.DisplayName) == "" {
		return errors.New("object display_name is required")
	}
	return nil
}

func normalizeV2MemoryPackArtifact(artifact V2MemoryPackArtifact) V2MemoryPackArtifact {
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
		item.Subject = normalizeV2MemoryPackEndpoint(item.Subject)
		item.Object = normalizeV2MemoryPackEndpoint(item.Object)
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

func normalizeV2MemoryPackEndpoint(endpoint V2MemoryPackEndpoint) V2MemoryPackEndpoint {
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

func inspectV2MemoryPack(artifact V2MemoryPackArtifact, hash string, sourceURL string) *V2InspectResult {
	result := &V2InspectResult{
		ArtifactHash: hash,
		Format:       artifact.Format,
		Name:         artifact.Name,
		Description:  artifact.Description,
		ItemCount:    len(artifact.Relationships),
		SourceURL:    sourceURL,
		Items:        make([]V2InspectItem, 0, len(artifact.Relationships)),
		SupportSummary: V2SupportSummary{
			FragmentCount: len(artifact.EvidenceFragments),
			SupportCount:  len(artifact.EvidenceSupports),
		},
	}
	fragments := map[string]struct{}{}
	for _, fragment := range artifact.EvidenceFragments {
		fragments[fragment.FragmentID] = struct{}{}
	}
	for _, item := range artifact.Relationships {
		inspected := V2InspectItem{
			ItemID:               item.ItemID,
			SourceRelationshipID: item.SourceRelationshipID,
			Status:               "ready",
			PredicateKey:         item.PredicateKey,
			Subject:              item.Subject.DisplayName,
			Object:               v2MemoryPackEndpointText(item.Object),
			SupportFragmentIDs:   append([]string(nil), item.SupportFragmentIDs...),
		}
		if !v2MemoryPackSupportedPredicate(item.PredicateKey) {
			inspected.Status = "needs_review"
			inspected.Severity = "medium"
			inspected.Message = "predicate requires V2 placement review"
			result.DecisionsRequired = append(result.DecisionsRequired, V2ConflictPrompt{
				ItemID:         item.ItemID,
				Reason:         "predicate is not registered in the destination V2 predicate set",
				AllowedActions: []string{"skip", "import_for_review"},
			})
		}
		for _, fragmentID := range item.SupportFragmentIDs {
			if _, ok := fragments[fragmentID]; !ok {
				inspected.Status = "needs_review"
				inspected.Severity = "high"
				inspected.Message = "support fragment is not present in artifact"
			}
		}
		result.Items = append(result.Items, inspected)
	}
	return result
}

func v2MemoryPackRelationshipFromTrace(record *repository.V2RelationshipTraceRecord) V2MemoryPackRelationship {
	itemID := record.RelationshipID
	if itemID == "" {
		itemID = "rel_" + v2MemoryPackShortHash(record.SemanticGroupKey)
	}
	object := V2MemoryPackEndpoint{
		Ref:         "object",
		Kind:        "entity",
		SourceID:    record.ObjectEntityID,
		DisplayName: record.ObjectEntityName,
	}
	if record.ObjectValueID != "" {
		object = V2MemoryPackEndpoint{
			Ref:       "object",
			Kind:      "value",
			SourceID:  record.ObjectValueID,
			ValueType: record.ObjectValueType,
			Value:     record.ObjectValue,
		}
	}
	return V2MemoryPackRelationship{
		ItemID:                    itemID,
		SourceRelationshipID:      record.RelationshipID,
		SourceRelationshipVersion: record.Version,
		SourceOwnerProfileID:      record.OwnerProfileID,
		Subject: V2MemoryPackEndpoint{
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
		Tier:             record.Tier,
		Status:           record.Status,
	}
}

func v2MemoryPackFromLegacy(pack SkillPack) V2MemoryPackArtifact {
	createdAt := time.Now().UTC().Format(time.RFC3339Nano)
	if pack.ExportedAt != nil {
		createdAt = pack.ExportedAt.UTC().Format(time.RFC3339Nano)
	}
	artifact := V2MemoryPackArtifact{
		Format:              V2MemoryPackFormat,
		PackID:              "legacy_" + v2MemoryPackShortHash(pack.Name),
		Name:                pack.Name,
		Description:         pack.Description,
		CreatedAt:           createdAt,
		Source:              V2MemoryPackSource{InstallationID: "legacy"},
		LegacySchemaVersion: pack.SchemaVersion,
	}
	for i, item := range pack.Items {
		itemID := item.SourceID
		if itemID == "" {
			itemID = fmt.Sprintf("legacy-item-%d", i)
		}
		relationship := V2MemoryPackRelationship{
			ItemID:               itemID,
			SourceRelationshipID: item.SourceID,
			Subject: V2MemoryPackEndpoint{
				Ref:         fmt.Sprintf("legacy-subject-%d", i),
				Kind:        "entity",
				DisplayName: item.Subject,
			},
			PredicateKey:     item.Predicate,
			PredicateVersion: 1,
			Object: V2MemoryPackEndpoint{
				Ref:         fmt.Sprintf("legacy-object-%d", i),
				Kind:        "entity",
				DisplayName: item.Object,
			},
			Status:             string(domain.V2RelationshipStatusNeedsReview),
			SupportFragmentIDs: append([]string(nil), item.SupportFragmentIDs...),
			Metadata: map[string]any{
				"legacy_source_kind": item.SourceKind,
				"legacy_source_id":   item.SourceID,
			},
		}
		artifact.Relationships = append(artifact.Relationships, relationship)
	}
	if pack.Support != nil {
		for _, fragment := range pack.Support.Fragments {
			artifact.EvidenceFragments = append(artifact.EvidenceFragments, V2MemoryPackEvidenceFragment{
				FragmentID: fragment.FragmentID,
				Content:    fragment.Content,
				SourceType: fragment.SourceType,
				Authority:  fragment.Authority,
				SourceRef:  fragment.Source,
				Labels:     append([]string(nil), fragment.Labels...),
			})
		}
	}
	return normalizeV2MemoryPackArtifact(artifact)
}
