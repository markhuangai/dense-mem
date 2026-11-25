package memorypack

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	tracecontract "github.com/markhuangai/dense-mem/internal/trace/contract"
	"sort"
	"strings"
	"time"
)

var ErrInvalidArtifact = errors.New("invalid memory pack artifact")

func canonicalMemoryPackArtifact(artifact MemoryPackArtifact) ([]byte, string, error) {
	artifact = normalizeMemoryPackArtifact(artifact)
	if err := validateMemoryPackArtifact(artifact); err != nil {
		return nil, "", err
	}
	artifact.ContentSHA256 = ""
	data, err := json.Marshal(artifact)
	if err != nil {
		return nil, "", err
	}
	digest := sha256.Sum256(data)
	return data, hex.EncodeToString(digest[:]), nil
}
func marshalMemoryPackArtifact(artifact MemoryPackArtifact) ([]byte, error) {
	artifact = normalizeMemoryPackArtifact(artifact)
	if err := validateMemoryPackArtifact(artifact); err != nil {
		return nil, err
	}
	return json.Marshal(artifact)
}
func validateMemoryPackArtifact(artifact MemoryPackArtifact) error {
	if artifact.Format != MemoryPackFormat || artifact.Name == "" || len(artifact.Name) > maxMemoryPackNameBytes || len(artifact.Description) > maxMemoryPackDescriptionBytes {
		return fmt.Errorf("%w: invalid format, name or description", ErrInvalidArtifact)
	}
	if _, err := time.Parse(time.RFC3339Nano, artifact.CreatedAt); err != nil {
		return fmt.Errorf("%w: created_at", ErrInvalidArtifact)
	}
	if len(artifact.Evidence) > maxExportEvidence || artifact.Evidence == nil || artifact.Omissions == nil {
		return fmt.Errorf("%w: evidence or omissions bounds", ErrInvalidArtifact)
	}
	seen := map[string]bool{}
	for _, item := range artifact.Evidence {
		id, err := uuid.Parse(item.FragmentID)
		if err != nil || id == uuid.Nil || seen[id.String()] {
			return fmt.Errorf("%w: duplicated or invalid evidence ID", ErrInvalidArtifact)
		}
		seen[id.String()] = true
		for _, fragment := range append([]tracecontract.TraceEvidenceFragment{item.TraceEvidenceFragment}, item.Occurrences...) {
			if fragment.ContentOmitted {
				if fragment.Content != "" {
					return fmt.Errorf("%w: omitted content is present", ErrInvalidArtifact)
				}
				continue
			}
			if fragment.ContentTruncated {
				continue
			}
			digest := sha256.Sum256([]byte(fragment.Content))
			if strings.TrimPrefix(fragment.ContentHash, "sha256:") != hex.EncodeToString(digest[:]) {
				return fmt.Errorf("%w: exact Evidence content hash mismatch", ErrInvalidArtifact)
			}
		}
	}
	return nil
}
func normalizeMemoryPackArtifact(artifact MemoryPackArtifact) MemoryPackArtifact {
	artifact.Format = strings.TrimSpace(artifact.Format)
	artifact.PackID = strings.TrimSpace(artifact.PackID)
	artifact.Name = strings.TrimSpace(artifact.Name)
	artifact.Description = strings.TrimSpace(artifact.Description)
	artifact.Evidence = append([]MemoryPackEvidence{}, artifact.Evidence...)
	for index := range artifact.Evidence {
		item := &artifact.Evidence[index]
		item.Occurrences = append([]tracecontract.TraceEvidenceFragment{}, item.Occurrences...)
		item.SourceRevisions = append([]tracecontract.TraceSourceRevision{}, item.SourceRevisions...)
		item.Decisions = append([]tracecontract.TraceEvidenceDecision{}, item.Decisions...)
		item.Lifecycle = append([]tracecontract.TraceEvidenceLifecycleEvent{}, item.Lifecycle...)
		item.Omissions = append([]string{}, item.Omissions...)
		sort.Slice(item.Occurrences, func(i, j int) bool { return item.Occurrences[i].OccurrenceID < item.Occurrences[j].OccurrenceID })
		sort.Slice(item.SourceRevisions, func(i, j int) bool {
			return item.SourceRevisions[i].SourceRevisionID < item.SourceRevisions[j].SourceRevisionID
		})
		sort.Slice(item.Decisions, func(i, j int) bool { return item.Decisions[i].DecisionID < item.Decisions[j].DecisionID })
		sort.Slice(item.Lifecycle, func(i, j int) bool { return item.Lifecycle[i].LifecycleEventID < item.Lifecycle[j].LifecycleEventID })
		sort.Strings(item.Omissions)
	}
	sort.Slice(artifact.Evidence, func(i, j int) bool { return artifact.Evidence[i].FragmentID < artifact.Evidence[j].FragmentID })
	artifact.Omissions = append([]string{}, artifact.Omissions...)
	sort.Strings(artifact.Omissions)
	return artifact
}
