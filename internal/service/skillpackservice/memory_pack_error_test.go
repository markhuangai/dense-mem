package skillpackservice

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
)

func TestV2MemoryPackLoadRejectsMissingUnsafeAndFailedURLSources(t *testing.T) {
	ctx := authenticatedV2MemoryPackContext(uuid.New(), uuid.New(), uuid.New())
	largeArtifact := strings.Repeat("x", maxArtifactBytes+1)
	tests := []struct {
		name   string
		svc    MemoryPackService
		req    V2InspectRequest
		want   string
		target error
	}{
		{
			name: "missing source",
			svc:  NewMemoryPackService(MemoryPackDependencies{}),
			req:  V2InspectRequest{},
			want: "artifact_json or url is required",
		},
		{
			name:   "unsafe url",
			svc:    NewMemoryPackService(MemoryPackDependencies{}),
			req:    V2InspectRequest{URL: "http://example.com/pack.json"},
			target: ErrUnsafeURL,
		},
		{
			name: "client error",
			svc: NewMemoryPackService(MemoryPackDependencies{
				HTTPClient: v2ArtifactHTTPClientFunc(func(*http.Request) (*http.Response, error) {
					return nil, errors.New("network down")
				}),
			}),
			req:  V2InspectRequest{URL: "https://example.com/pack.json"},
			want: "network down",
		},
		{
			name: "bad status",
			svc: NewMemoryPackService(MemoryPackDependencies{
				HTTPClient: v2ArtifactHTTPClientFunc(func(*http.Request) (*http.Response, error) {
					return &http.Response{
						StatusCode: http.StatusServiceUnavailable,
						Body:       io.NopCloser(strings.NewReader("unavailable")),
					}, nil
				}),
			}),
			req:  V2InspectRequest{URL: "https://example.com/pack.json"},
			want: "status 503",
		},
		{
			name: "too large inline artifact",
			svc:  NewMemoryPackService(MemoryPackDependencies{}),
			req:  V2InspectRequest{ArtifactJSON: largeArtifact},
			want: "artifact exceeds",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.svc.Inspect(ctx, tc.req)
			if tc.target != nil {
				if !errors.Is(err, tc.target) {
					t.Fatalf("err = %v, want target %v", err, tc.target)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestV2MemoryPackImportSelectedSubsetUsesDestinationOwnerAndSourceProvenance(t *testing.T) {
	teamID := uuid.New()
	ownerID := uuid.New()
	artifactJSON := testV2ArtifactJSON(t, V2MemoryPackArtifact{
		Format:    MemoryPackFormat,
		PackID:    "pack-subset",
		Name:      "Subset pack",
		CreatedAt: time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano),
		Source:    V2MemoryPackSource{InstallationID: "source-installation"},
		Relationships: []V2MemoryPackRelationship{
			{
				ItemID:           "item-1",
				Subject:          V2MemoryPackEndpoint{Ref: "subject", Kind: "entity", DisplayName: "Dense-Mem"},
				PredicateKey:     "uses",
				PredicateVersion: 1,
				Object:           V2MemoryPackEndpoint{Ref: "object-1", Kind: "entity", DisplayName: "GraphDB"},
			},
			{
				ItemID:           "item-2",
				Subject:          V2MemoryPackEndpoint{Ref: "subject", Kind: "entity", DisplayName: "Dense-Mem"},
				PredicateKey:     "uses",
				PredicateVersion: 1,
				Object:           V2MemoryPackEndpoint{Ref: "object-2", Kind: "entity", DisplayName: "PostgreSQL"},
			},
		},
	})
	remember := &v2RememberStub{result: &memoryservice.V2RememberResult{
		IngestID:        "ingest-subset",
		ProcessingState: string(domain.V2PlacementRunQueued),
	}}
	result, err := NewMemoryPackService(MemoryPackDependencies{
		Remember: remember,
		Ledger:   newV2LedgerStub(),
	}).Import(
		authenticatedV2MemoryPackContext(teamID, ownerID, uuid.New()),
		V2ImportRequest{
			ArtifactJSON:    artifactJSON,
			Mode:            ModeReview,
			SelectedItemIDs: []string{"item-2"},
			ConflictDecisions: []V2ImportItemDecision{{
				ItemID: "item-2",
				Action: "import_for_review",
			}},
		},
	)
	if err != nil {
		t.Fatalf("ImportV2 selected subset: %v", err)
	}
	if result.AppliedCount != 1 || result.SkippedCount != 1 {
		t.Fatalf("subset import counts = %#v", result)
	}
	if result.Items[0].Status != "skipped" || result.Items[1].Status != "staged" || result.Items[1].PlacementItemID != "" {
		t.Fatalf("subset import items = %#v", result.Items)
	}
	if len(remember.reqs) != 1 || len(remember.reqs[0].Evidence) != 1 {
		t.Fatalf("remember requests = %#v", remember.reqs)
	}
	evidence := remember.reqs[0].Evidence[0]
	if evidence.Source != "source-installation" || evidence.Metadata["memory_pack_item_id"] != "item-2" {
		t.Fatalf("subset evidence = %#v", evidence)
	}
}

func TestV2MemoryPackLedgerHardFailuresRemainVisible(t *testing.T) {
	ctx := authenticatedV2MemoryPackContext(uuid.New(), uuid.New(), uuid.New())
	artifactJSON := testV2ArtifactJSON(t, V2MemoryPackArtifact{
		Format:    MemoryPackFormat,
		PackID:    "pack-ledger-failure",
		Name:      "Ledger failure pack",
		CreatedAt: time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano),
		Relationships: []V2MemoryPackRelationship{{
			ItemID:           "item-1",
			Subject:          V2MemoryPackEndpoint{Ref: "subject", Kind: "entity", DisplayName: "Dense-Mem"},
			PredicateKey:     "uses",
			PredicateVersion: 1,
			Object:           V2MemoryPackEndpoint{Ref: "object", Kind: "entity", DisplayName: "PostgreSQL"},
		}},
	})

	createLedger := newV2LedgerStub()
	createLedger.createErr = errors.New("create failed")
	_, err := NewMemoryPackService(MemoryPackDependencies{
		Remember: &v2RememberStub{},
		Ledger:   createLedger,
	}).Import(ctx, V2ImportRequest{ArtifactJSON: artifactJSON, Mode: ModeReview})
	if err == nil || !strings.Contains(err.Error(), "create failed") {
		t.Fatalf("create import err = %v, want create failed", err)
	}

	teamID := uuid.New()
	ownerID := uuid.New()
	importID := "import-ledger-failure"
	listLedger := newV2LedgerStub()
	listLedger.listErr = errors.New("list failed")
	listLedger.imports[ledgerKey(teamID.String(), importID)] = domain.SkillPackImport{
		ImportID:       importID,
		TeamID:         teamID.String(),
		OwnerProfileID: ownerID.String(),
		Status:         domain.SkillPackImportStatusApplied,
	}
	_, err = NewMemoryPackService(MemoryPackDependencies{Ledger: listLedger}).Rollback(
		authenticatedV2MemoryPackContext(teamID, ownerID, uuid.New()),
		V2RollbackRequest{ImportID: importID, Confirm: true},
	)
	if err == nil || !strings.Contains(err.Error(), "list failed") {
		t.Fatalf("list changes err = %v, want list failed", err)
	}

	markLedger := newV2LedgerStub()
	markLedger.markErr = errors.New("mark failed")
	markLedger.imports[ledgerKey(teamID.String(), importID)] = domain.SkillPackImport{
		ImportID:       importID,
		TeamID:         teamID.String(),
		OwnerProfileID: ownerID.String(),
		Status:         domain.SkillPackImportStatusApplied,
	}
	markSvc := NewMemoryPackService(MemoryPackDependencies{Ledger: markLedger})
	dryRun, err := markSvc.Rollback(
		authenticatedV2MemoryPackContext(teamID, ownerID, uuid.New()),
		V2RollbackRequest{ImportID: importID, DryRun: true},
	)
	if err != nil {
		t.Fatalf("mark rollback dry run: %v", err)
	}
	_, err = markSvc.Rollback(
		authenticatedV2MemoryPackContext(teamID, ownerID, uuid.New()),
		V2RollbackRequest{ImportID: importID, Confirm: true, ImpactToken: dryRun.ImpactToken},
	)
	if err == nil || !strings.Contains(err.Error(), "mark failed") {
		t.Fatalf("mark rollback err = %v, want mark failed", err)
	}
}

func TestV2MemoryPackExistingImportUsesSummaryIngestFallback(t *testing.T) {
	record := &domain.SkillPackImport{
		ImportID:     "import-existing",
		Status:       domain.SkillPackImportStatusApplied,
		AppliedCount: 1,
		Summary:      map[string]any{"ingest_id": "summary-ingest"},
	}
	result := importResultFromExisting(record, "hash", ModeReview)
	if result.IngestID != "summary-ingest" {
		t.Fatalf("existing import result = %#v", result)
	}
}
