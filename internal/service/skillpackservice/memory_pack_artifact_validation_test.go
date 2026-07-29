package skillpackservice

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/repository"
)

func TestMemoryPackArtifactReadersRejectInvalidStructuredArtifacts(t *testing.T) {
	invalidCurrent := MemoryPackArtifact{
		Format:    MemoryPackFormat,
		PackID:    "pack-current-invalid",
		Name:      "Current invalid",
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	invalidV23 := memoryPackArtifactV23{
		Format:    memoryPackV23Format,
		PackID:    "pack-v23-invalid",
		Name:      "V23 invalid",
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	invalidLegacy := SkillPack{SchemaVersion: SchemaVersion, Name: "Legacy invalid"}

	for _, tc := range []struct {
		name     string
		artifact any
	}{
		{name: "current", artifact: invalidCurrent},
		{name: "v2.3", artifact: invalidV23},
		{name: "legacy", artifact: invalidLegacy},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.artifact)
			if err != nil {
				t.Fatalf("marshal invalid artifact: %v", err)
			}
			_, _, err = parseMemoryPackArtifactJSON(data)
			if !errors.Is(err, ErrInvalidArtifact) {
				t.Fatalf("parse err = %v, want ErrInvalidArtifact", err)
			}
		})
	}

	for _, tc := range []struct {
		name string
		run  func() error
	}{
		{name: "current canonical", run: func() error { _, _, err := canonicalMemoryPackArtifact(invalidCurrent); return err }},
		{name: "current marshal", run: func() error { _, err := marshalMemoryPackArtifact(invalidCurrent); return err }},
		{name: "v2.3 canonical", run: func() error { _, _, err := canonicalMemoryPackArtifactV23(invalidV23); return err }},
		{name: "legacy canonical", run: func() error { _, _, err := canonicalLegacyMemoryPackArtifact(invalidLegacy); return err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.run(); !errors.Is(err, ErrInvalidArtifact) {
				t.Fatalf("canonicalization err = %v, want ErrInvalidArtifact", err)
			}
		})
	}

	_, _, err := canonicalMemoryPackArtifactV23(memoryPackArtifactV23{Format: MemoryPackFormat})
	if !errors.Is(err, ErrInvalidArtifact) || !strings.Contains(err.Error(), "format must be") {
		t.Fatalf("v2.3 wrong-format error = %v", err)
	}
}

func TestMemoryPackArtifactNormalizersPreserveCurrentShapes(t *testing.T) {
	endpoint := normalizeMemoryPackEndpoint(MemoryPackEndpoint{
		Ref:         " object ",
		Kind:        " ",
		SourceID:    " entity-1 ",
		DisplayName: " PostgreSQL ",
	})
	if endpoint.Ref != "object" || endpoint.Kind != "entity" || endpoint.SourceID != "entity-1" || endpoint.DisplayName != "PostgreSQL" {
		t.Fatalf("normalized endpoint = %#v", endpoint)
	}

	relationship := memoryPackRelationshipFromTrace(&repository.RelationshipTraceRecord{
		SemanticGroupKey: "dense-mem:uses:postgres",
		SubjectEntityID:  "entity-dense-mem",
		SubjectName:      "Dense-Mem",
		PredicateKey:     "uses",
		PredicateVersion: 1,
		ObjectEntityID:   "entity-postgres",
		ObjectEntityName: "PostgreSQL",
	})
	if !strings.HasPrefix(relationship.ItemID, "rel_") || relationship.Object.Kind != "entity" {
		t.Fatalf("derived relationship = %#v", relationship)
	}
}
