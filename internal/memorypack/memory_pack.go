package memorypack

import (
	"context"
	"errors"
	"fmt"
	"github.com/google/uuid"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	"github.com/markhuangai/dense-mem/internal/observability"
	tracecontract "github.com/markhuangai/dense-mem/internal/trace/contract"
	"log/slog"
	"sort"
	"strings"
	"time"
)

const (
	maxExportEvidence             = 500
	maxTraceEvents                = 100
	maxFragmentContentRunes       = knowledge.MaxRememberItemRunes
	maxMemoryPackNameBytes        = 256
	maxMemoryPackDescriptionBytes = 1024
	maxMemoryPackArtifactBytes    = 4 * 1024 * 1024
)

func NewMemoryPackService(deps MemoryPackDependencies) MemoryPackService {
	now := deps.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &memoryPackService{deps: deps, now: now}
}
func (s *memoryPackService) Export(ctx context.Context, req ExportRequest) (result *ExportResult, err error) {
	actor, err := memoryPackActor(ctx)
	if err != nil {
		return nil, err
	}
	if s == nil || s.deps.Semantic == nil {
		return nil, errors.New("memory pack export: Evidence reader is required")
	}
	defer func() {
		if s.deps.Logger == nil {
			return
		}
		attrs := []observability.LogAttr{observability.String("team_id", actor.TeamID.String()), observability.String("owner_profile_id", actor.OwnerID.String()), {Key: "requested_evidence_ids", Value: req.EvidenceIDs}, {Key: "max_events", Value: maxTraceEvents}, {Key: "max_content_runes", Value: maxFragmentContentRunes}, {Key: "artifact_byte_limit", Value: maxMemoryPackArtifactBytes}}
		if err != nil {
			attrs = append(attrs, observability.String("outcome", "failed"), observability.String("reason", err.Error()))
		} else {
			attrs = append(attrs, observability.String("outcome", "completed"), observability.LogAttr{Key: "item_count", Value: result.ItemCount}, observability.LogAttr{Key: "omissions", Value: result.Omissions}, observability.String("content_sha256", result.SHA256))
		}
		if logger, ok := s.deps.Logger.(observability.ContextLogProvider); ok {
			logger.LogContext(ctx, slog.LevelDebug, "memory_pack_evidence_selection", attrs...)
		} else {
			s.deps.Logger.Debug("memory_pack_evidence_selection", attrs...)
		}
	}()
	name, description := strings.TrimSpace(req.Name), strings.TrimSpace(req.Description)
	if name == "" || len(name) > maxMemoryPackNameBytes || len(description) > maxMemoryPackDescriptionBytes {
		return nil, errors.New("memory pack export: name or description exceeds bounds")
	}
	if len(req.EvidenceIDs) < 1 || len(req.EvidenceIDs) > maxExportEvidence {
		return nil, errors.New("memory pack export: evidence_ids must contain between 1 and 500 UUIDs")
	}
	ids := make([]string, 0, len(req.EvidenceIDs))
	seen := map[string]bool{}
	for _, value := range req.EvidenceIDs {
		id, parseErr := uuid.Parse(strings.TrimSpace(value))
		if parseErr != nil || id == uuid.Nil || seen[id.String()] {
			return nil, errors.New("memory pack export: evidence_ids must contain unique UUIDs")
		}
		seen[id.String()] = true
		ids = append(ids, id.String())
	}
	sort.Strings(ids)
	now := s.now().UTC()
	artifact := MemoryPackArtifact{Format: MemoryPackFormat, PackID: "pack_" + memoryPackShortHash(strings.Join(ids, "\x00")+now.Format(time.RFC3339Nano)), Name: name, Description: description, CreatedAt: now.Format(time.RFC3339Nano), Source: MemoryPackSource{TeamID: actor.TeamID.String(), ExportedBy: actor.OwnerID.String()}, Evidence: []MemoryPackEvidence{}, Omissions: []string{}}
	exported := map[string]bool{}
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		trace, readErr := s.deps.Semantic.TraceEvidence(ctx, tracecontract.Input{TeamID: actor.TeamID.String(), EvidenceID: id, IncludeEvidenceContent: req.IncludeEvidence, MaxEvents: maxTraceEvents, MaxFragmentContentRunes: maxFragmentContentRunes})
		if readErr != nil {
			return nil, readErr
		}
		if trace == nil || trace.Evidence == nil || trace.Evidence.TeamID != actor.TeamID.String() {
			return nil, tracecontract.ErrEvidenceNotFound
		}
		if exported[trace.Evidence.FragmentID] {
			continue
		}
		item := MemoryPackEvidence{TraceEvidenceFragment: *trace.Evidence, Occurrences: trace.Occurrences, SourceRevisions: trace.SourceRevisions, Decisions: trace.Decisions, Lifecycle: trace.Lifecycle, Omissions: append([]string{}, trace.Omissions...)}
		artifact.Evidence = append(artifact.Evidence, item)
		encoded, _, encodeErr := canonicalMemoryPackArtifact(artifact)
		if encodeErr != nil {
			return nil, encodeErr
		}
		if len(encoded) > maxMemoryPackArtifactBytes-128*1024 {
			artifact.Evidence = artifact.Evidence[:len(artifact.Evidence)-1]
			artifact.Omissions = append(artifact.Omissions, id+":artifact_byte_limit")
		} else {
			exported[trace.Evidence.FragmentID] = true
			for _, omission := range trace.Omissions {
				artifact.Omissions = append(artifact.Omissions, id+":"+omission)
			}
		}
	}
	artifact = normalizeMemoryPackArtifact(artifact)
	_, hash, err := canonicalMemoryPackArtifact(artifact)
	if err != nil {
		return nil, err
	}
	artifact.ContentSHA256 = hash
	encoded, err := marshalMemoryPackArtifact(artifact)
	if err != nil {
		return nil, err
	}
	if len(encoded) > maxMemoryPackArtifactBytes {
		return nil, fmt.Errorf("memory pack export: artifact exceeds %d bytes", maxMemoryPackArtifactBytes)
	}
	return &ExportResult{Artifact: artifact, CanonicalJSON: string(encoded), SHA256: hash, ItemCount: len(artifact.Evidence), Filename: skillPackFilename(name), ContentType: "application/json", Omissions: append([]string{}, artifact.Omissions...)}, nil
}
