package memorypack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/requestctx"
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
	requestctx.ReportProgress(ctx, requestctx.ProgressUpdate{Stage: "validating_input"})
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
	const readBatchSize = 8
	batches := (len(ids) + readBatchSize - 1) / readBatchSize
	empty, err := json.Marshal(artifact)
	if err != nil {
		return nil, err
	}
	bytesUsed := len(empty)
	appendOmission := func(value string) {
		encoded, _ := json.Marshal(value)
		if len(artifact.Omissions) > 0 {
			bytesUsed++
		}
		bytesUsed += len(encoded)
		artifact.Omissions = append(artifact.Omissions, value)
	}
	exported := map[string]bool{}
	traceInput := tracecontract.Input{TeamID: actor.TeamID.String(), IncludeEvidenceContent: req.IncludeEvidence, MaxEvents: maxTraceEvents, MaxFragmentContentRunes: maxFragmentContentRunes}
	for offset := 0; offset < len(ids); offset += readBatchSize {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		page := ids[offset:min(offset+readBatchSize, len(ids))]
		requestctx.ReportProgress(ctx, requestctx.ProgressUpdate{Stage: "export_read", Batch: offset/readBatchSize + 1, Batches: batches})
		traces, err := s.deps.Semantic.TraceEvidenceBatch(ctx, traceInput, page)
		if err != nil {
			return nil, err
		}
		if len(traces) != len(page) {
			return nil, tracecontract.ErrEvidenceNotFound
		}
		for i, trace := range traces {
			if trace == nil || trace.Evidence == nil || trace.Evidence.TeamID != actor.TeamID.String() {
				return nil, tracecontract.ErrEvidenceNotFound
			}
			if exported[trace.Evidence.FragmentID] {
				continue
			}
			item := MemoryPackEvidence{TraceEvidenceFragment: *trace.Evidence, Occurrences: trace.Occurrences,
				SourceRevisions: trace.SourceRevisions, Decisions: trace.Decisions, Lifecycle: trace.Lifecycle, Omissions: trace.Omissions}
			item = normalizeMemoryPackItem(item)
			encoded, err := json.Marshal(item)
			if err != nil {
				return nil, err
			}
			itemBytes := len(encoded)
			if len(artifact.Evidence) > 0 {
				itemBytes++
			}
			if bytesUsed+itemBytes > maxMemoryPackArtifactBytes-128*1024 {
				appendOmission(page[i] + ":artifact_byte_limit")
				continue
			}
			bytesUsed += itemBytes
			exported[trace.Evidence.FragmentID] = true
			artifact.Evidence = append(artifact.Evidence, item)
			for _, omission := range item.Omissions {
				appendOmission(page[i] + ":" + omission)
			}
		}
	}
	requestctx.ReportProgress(ctx, requestctx.ProgressUpdate{Stage: "export_artifact"})
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
