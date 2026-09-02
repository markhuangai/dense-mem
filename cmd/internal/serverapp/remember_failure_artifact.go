package serverapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
	rememberapp "github.com/markhuangai/dense-mem/internal/service/remember"
)

func rememberFailureRequestArtifact(input rememberapp.RememberProcessRequest, attemptID string, evidence []repository.EvidenceFragment) (repository.RememberFailureArtifactInput, bool) {
	if len(evidence) == 0 {
		return repository.RememberFailureArtifactInput{}, false
	}
	payload := rememberFailureRequestArtifactPayload{
		ContractVersion: domain.ContractVersion,
		SubmissionID:    rememberFailureArtifactIdentifier(attemptID),
		SubmissionKind:  "remember",
		RequestHash:     rememberFailureArtifactHashOrValue(input.RequestHash),
		IdempotencyHash: rememberFailureArtifactHash(strings.TrimSpace(input.IdempotencyKey)),
		Evidence:        rememberFailureEvidenceArtifacts(evidence),
		Relationships:   rememberFailureRelationshipArtifacts(input.Proposal),
		EntityHints:     rememberFailureEntityArtifacts(input.Proposal),
	}
	encoded, err := json.Marshal(payload)
	if err != nil || len(encoded) > rememberFailureArtifactMaxBytes {
		return repository.RememberFailureArtifactInput{}, false
	}
	return repository.RememberFailureArtifactInput{ArtifactKind: "request", ContentType: "application/json", Content: encoded}, true
}

const (
	rememberFailureArtifactMaxBytes             = 256 * 1024
	rememberFailureArtifactMaxRelationshipItems = 200
	rememberFailureArtifactMaxEntityItems       = 400
)

type rememberFailureRequestArtifactPayload struct {
	ContractVersion string                                `json:"contract_version"`
	SubmissionID    string                                `json:"submission_id"`
	SubmissionKind  string                                `json:"submission_kind"`
	RequestHash     string                                `json:"request_hash"`
	IdempotencyHash string                                `json:"idempotency_key_hash"`
	Evidence        []rememberFailureEvidenceArtifact     `json:"evidence"`
	Relationships   []rememberFailureRelationshipArtifact `json:"relationships"`
	EntityHints     []rememberFailureEntityArtifact       `json:"entity_hints,omitempty"`
}

type rememberFailureEvidenceArtifact struct {
	Index       int      `json:"index"`
	ContentHash string   `json:"content_hash"`
	ByteCount   int      `json:"byte_count"`
	RuneCount   int      `json:"rune_count"`
	Supersedes  []string `json:"supersedes_evidence_ids,omitempty"`
}

type rememberFailureRelationshipArtifact struct {
	Index           int                               `json:"index"`
	RefHash         string                            `json:"ref_hash"`
	EvidenceIndices []int                             `json:"evidence_indices,omitempty"`
	Subject         rememberFailureEntityArtifact     `json:"subject,omitempty"`
	Predicate       rememberFailurePredicateArtifact  `json:"predicate,omitempty"`
	Object          *rememberFailureObjectArtifact    `json:"object,omitempty"`
	Polarity        string                            `json:"polarity,omitempty"`
	ValidFrom       string                            `json:"valid_from,omitempty"`
	ValidTo         string                            `json:"valid_to,omitempty"`
	Correction      *rememberFailureLifecycleArtifact `json:"correction_target,omitempty"`
	Conflict        *rememberFailureLifecycleArtifact `json:"conflict_context,omitempty"`
}

type rememberFailureEntityArtifact struct {
	NameHash      string `json:"name_hash,omitempty"`
	EntityKind    string `json:"entity_kind,omitempty"`
	KnownEntityID string `json:"known_entity_id,omitempty"`
}

type rememberFailurePredicateArtifact struct {
	ProposedKeyHash string `json:"proposed_key_hash,omitempty"`
	KnownKeyHash    string `json:"known_predicate_key_hash,omitempty"`
}

type rememberFailureObjectArtifact struct {
	Entity *rememberFailureEntityArtifact `json:"entity,omitempty"`
	Value  *rememberFailureValueArtifact  `json:"value,omitempty"`
}

type rememberFailureValueArtifact struct {
	Type        string `json:"type,omitempty"`
	ValueHash   string `json:"value_hash,omitempty"`
	DisplayHash string `json:"display_hash,omitempty"`
	UnitHash    string `json:"unit_hash,omitempty"`
}

type rememberFailureLifecycleArtifact struct {
	IDHash          string `json:"id_hash,omitempty"`
	ExpectedVersion int    `json:"expected_version,omitempty"`
}

func rememberFailureEvidenceArtifacts(evidence []repository.EvidenceFragment) []rememberFailureEvidenceArtifact {
	artifacts := make([]rememberFailureEvidenceArtifact, 0, len(evidence))
	for _, item := range evidence {
		artifacts = append(artifacts, rememberFailureEvidenceArtifact{
			Index: item.EvidenceIndex, ContentHash: rememberFailureArtifactHash(item.Content),
			ByteCount: len([]byte(item.Content)), RuneCount: len([]rune(item.Content)),
			Supersedes: rememberFailureUUIDSet(item.SupersededEvidenceIDs),
		})
	}
	return artifacts
}

func rememberFailureRelationshipArtifacts(proposal map[string]any) []rememberFailureRelationshipArtifact {
	items := rememberFailureProposalObjects(proposal, "relationship_hints", "relationships")
	if len(items) > rememberFailureArtifactMaxRelationshipItems {
		items = items[:rememberFailureArtifactMaxRelationshipItems]
	}
	artifacts := make([]rememberFailureRelationshipArtifact, 0, len(items))
	for index, item := range items {
		artifact := rememberFailureRelationshipArtifact{
			Index:           index,
			RefHash:         rememberFailureArtifactHash(rememberFailureString(item["ref"])),
			EvidenceIndices: rememberFailureEvidenceIndices(item["evidence_indices"]),
			Polarity:        rememberFailureEnum(rememberFailureString(item["polarity"]), "+", "-"),
			ValidFrom:       rememberFailureTimestamp(item["valid_from"]),
			ValidTo:         rememberFailureTimestamp(item["valid_to"]),
		}
		artifact.Subject = rememberFailureEntityArtifactFromValue(item["subject"])
		artifact.Predicate = rememberFailurePredicateArtifactFromValue(item["predicate"])
		artifact.Object = rememberFailureObjectArtifactFromValue(item["object"])
		artifact.Correction = rememberFailureLifecycleArtifactFromValue(item["correction_target"], "relationship_id")
		artifact.Conflict = rememberFailureLifecycleArtifactFromValue(item["conflict_context"], "conflict_id")
		artifacts = append(artifacts, artifact)
	}
	return artifacts
}

func rememberFailureEntityArtifacts(proposal map[string]any) []rememberFailureEntityArtifact {
	items := rememberFailureProposalObjects(proposal, "entity_hints")
	if len(items) > rememberFailureArtifactMaxEntityItems {
		items = items[:rememberFailureArtifactMaxEntityItems]
	}
	artifacts := make([]rememberFailureEntityArtifact, 0, len(items))
	for _, item := range items {
		artifacts = append(artifacts, rememberFailureEntityArtifactFromValue(item))
	}
	return artifacts
}

func rememberFailureProposalObjects(proposal map[string]any, keys ...string) []map[string]any {
	if proposal == nil {
		return nil
	}
	var raw any
	for _, key := range keys {
		if value, ok := proposal[key]; ok {
			raw = value
			break
		}
	}
	switch typed := raw.(type) {
	case []map[string]any:
		return append([]map[string]any(nil), typed...)
	case []any:
		items := make([]map[string]any, 0, len(typed))
		for _, value := range typed {
			if fields, ok := value.(map[string]any); ok {
				items = append(items, fields)
			} else {
				items = append(items, map[string]any{})
			}
		}
		return items
	default:
		return nil
	}
}

func rememberFailureEntityArtifactFromValue(raw any) rememberFailureEntityArtifact {
	fields, _ := raw.(map[string]any)
	return rememberFailureEntityArtifact{
		NameHash:      rememberFailureArtifactHash(rememberFailureString(fields["name"])),
		EntityKind:    rememberFailureEnum(rememberFailureString(fields["entity_kind"]), domain.EntityKinds()...),
		KnownEntityID: rememberFailureUUID(rememberFailureString(fields["known_entity_id"])),
	}
}

func rememberFailurePredicateArtifactFromValue(raw any) rememberFailurePredicateArtifact {
	fields, _ := raw.(map[string]any)
	return rememberFailurePredicateArtifact{
		ProposedKeyHash: rememberFailureArtifactHash(rememberFailureString(fields["proposed_key"])),
		KnownKeyHash:    rememberFailureArtifactHash(rememberFailureString(fields["known_predicate_key"])),
	}
}

func rememberFailureObjectArtifactFromValue(raw any) *rememberFailureObjectArtifact {
	fields, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	if entity, ok := fields["entity"].(map[string]any); ok {
		value := rememberFailureEntityArtifactFromValue(entity)
		return &rememberFailureObjectArtifact{Entity: &value}
	}
	if value, ok := fields["value"].(map[string]any); ok {
		return &rememberFailureObjectArtifact{Value: &rememberFailureValueArtifact{
			Type:        rememberFailureEnum(rememberFailureString(value["type"]), domain.ValueTypes()...),
			ValueHash:   rememberFailureArtifactHash(rememberFailureJSONValue(value["value"])),
			DisplayHash: rememberFailureArtifactHash(rememberFailureString(value["display"])),
			UnitHash:    rememberFailureArtifactHash(rememberFailureString(value["unit"])),
		}}
	}
	return nil
}

func rememberFailureLifecycleArtifactFromValue(raw any, idField string) *rememberFailureLifecycleArtifact {
	fields, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	version := rememberFailureInteger(fields["expected_version"])
	if version < 1 {
		version = 0
	}
	return &rememberFailureLifecycleArtifact{
		IDHash: rememberFailureArtifactHash(rememberFailureString(fields[idField])), ExpectedVersion: version,
	}
}

func rememberFailureEvidenceIndices(raw any) []int {
	values := rememberFailureAnySlice(raw)
	indices := make([]int, 0, len(values))
	seen := map[int]struct{}{}
	for _, value := range values {
		index := rememberFailureInteger(value)
		if index < 0 || index >= 20 {
			continue
		}
		if _, ok := seen[index]; ok {
			continue
		}
		seen[index] = struct{}{}
		indices = append(indices, index)
	}
	sort.Ints(indices)
	return indices
}

func rememberFailureUUIDSet(raw []string) []string {
	values := make([]string, 0, len(raw))
	seen := map[string]struct{}{}
	for _, value := range raw {
		if normalized := rememberFailureUUID(value); normalized != "" {
			if _, ok := seen[normalized]; !ok {
				seen[normalized] = struct{}{}
				values = append(values, normalized)
			}
		}
	}
	sort.Strings(values)
	return values
}

func rememberFailureUUID(value string) string {
	parsed, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil {
		return ""
	}
	return parsed.String()
}

func rememberFailureTimestamp(raw any) string {
	value := strings.TrimSpace(rememberFailureString(raw))
	if value == "" {
		return ""
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return ""
	}
	return parsed.UTC().Format(time.RFC3339Nano)
}

func rememberFailureEnum(value string, allowed ...string) string {
	value = strings.TrimSpace(value)
	for _, candidate := range allowed {
		if value == candidate {
			return value
		}
	}
	return ""
}

func rememberFailureString(raw any) string {
	value, _ := raw.(string)
	return strings.TrimSpace(value)
}

func rememberFailureInteger(raw any) int {
	switch value := raw.(type) {
	case int:
		return value
	case int8:
		return int(value)
	case int16:
		return int(value)
	case int32:
		return int(value)
	case int64:
		return int(value)
	case uint:
		return int(value)
	case uint8:
		return int(value)
	case uint16:
		return int(value)
	case uint32:
		return int(value)
	case uint64:
		if value <= uint64(^uint(0)>>1) {
			return int(value)
		}
	case float64:
		if value == float64(int(value)) {
			return int(value)
		}
	case float32:
		if value == float32(int(value)) {
			return int(value)
		}
	case json.Number:
		if parsed, err := value.Int64(); err == nil {
			return int(parsed)
		}
	}
	return 0
}

func rememberFailureAnySlice(raw any) []any {
	switch values := raw.(type) {
	case []any:
		return values
	case []int:
		out := make([]any, len(values))
		for index, value := range values {
			out[index] = value
		}
		return out
	case []string:
		out := make([]any, len(values))
		for index, value := range values {
			out[index] = value
		}
		return out
	default:
		return nil
	}
}

func rememberFailureJSONValue(value any) string {
	if value == nil {
		return ""
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(encoded)
}

func rememberFailureArtifactHash(value string) string {
	if value == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(value))
	return fmt.Sprintf("sha256:%x", digest[:])
}

func rememberFailureArtifactHashOrValue(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "sha256:") && len(value) == len("sha256:")+64 {
		if _, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:")); err == nil {
			return value
		}
	}
	return rememberFailureArtifactHash(value)
}

func rememberFailureArtifactIdentifier(value string) string {
	if identifier := rememberFailureUUID(value); identifier != "" {
		return identifier
	}
	return rememberFailureArtifactHash(strings.TrimSpace(value))
}

func rememberFailureRecoveryContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), rememberapp.RememberFailurePersistenceBudget)
}
