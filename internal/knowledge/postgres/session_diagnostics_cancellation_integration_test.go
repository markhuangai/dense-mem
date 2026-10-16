//go:build integration

package postgres

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	privacy "github.com/markhuangai/dense-mem/internal/privacy/postgres"
	processor "github.com/markhuangai/dense-mem/internal/remember/service/processor"
	"github.com/markhuangai/dense-mem/internal/session/extraction"
	service "github.com/markhuangai/dense-mem/internal/session/service"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestSessionCancelledDiagnosticsCannotReviveSealedOrErasedGeneration(t *testing.T) {
	for _, erase := range []bool{false, true} {
		t.Run(map[bool]string{false: "sealed", true: "erased"}[erase], func(t *testing.T) {
			admin, app, rls, cleanup := setupLedgerRepositoryDB(t)
			t.Cleanup(cleanup)
			team := createLedgerTeam(t, admin, rls, "cancelled-diagnostic")
			owner := createLedgerProfile(t, admin, rls, team, "owner")
			base, intake := sessionIntakeFixture(t, admin, app, rls, team, owner, domain.MemorySpacePrivate)
			store := NewStore(app, rls, ConflictRuntimeConfig{})
			outbound := &sessionOutbound{block: true, entered: make(chan struct{}, 1)}
			var logs bytes.Buffer
			api := service.NewService(service.Dependencies{Enabled: true, Repository: store, Preparer: processor.NewSynchronousProcessor(processor.ProcessorDependencies{Ledger: store}), Tokenizer: "o200k_base",
				Extractor: extraction.NewProvider(outbound, "fixture", assessor.DefaultSemanticAssessmentLimits()), DiagnosticProtector: observability.NewCredentialProtector("session-credential-canary"), Logger: observability.NewWithHandler(slog.NewTextHandler(&logs, nil))})
			ctx, cancel := context.WithCancel(base)
			done := make(chan error, 1)
			t.Cleanup(func() {
				cancel()
				select {
				case <-done:
				case <-time.After(10 * time.Second):
					t.Error("cancelled diagnostic worker did not stop before fixture cleanup")
				}
			})
			go func() { defer close(done); _, err := api.Ingest(ctx, intake.Request); done <- err }()
			select {
			case <-outbound.entered:
			case <-time.After(10 * time.Second):
				t.Fatal("session provider was not dispatched")
			}
			staged, err := store.LookupSession(base, intake.Scope, intake.Request.IdempotencyKey)
			require.NoError(t, err)
			if erase {
				eraser := privacy.NewPrivateMemoryRepository(app, rls)
				require.NoError(t, eraser.Prepare(base))
				_, _, err := eraser.RequestControlErasure(base, uuid.MustParse(intake.Scope.SpaceID), privacy.Hash("session", staged.ID), privacy.Hash("session-erasure", staged.ID), "operator_request")
				require.NoError(t, err)
				claim, err := eraser.ClaimNext(base, "cancelled-session-test", time.Minute)
				require.NoError(t, err)
				require.NotNil(t, claim)
				completed, err := eraser.ExecuteClaim(base, claim.ID, claim.WorkerID, claim.Fence)
				require.NoError(t, err)
				require.Equal(t, domain.PrivateMemoryErasureCompleted, completed.Status)
			} else {
				require.NoError(t, rls.WithSystemTx(base, admin, func(tx *gorm.DB) error {
					return tx.Exec(`UPDATE memory_spaces SET lifecycle_state='sealed',generation=generation+1,sealed_at=now() WHERE id=?::uuid`, intake.Scope.SpaceID).Error
				}))
			}
			cancel()
			select {
			case err := <-done:
				require.Error(t, err)
			case <-time.After(10 * time.Second):
				t.Fatal("cancelled session did not finish")
			}
			var count int64
			require.NoError(t, rls.WithSystemTx(base, admin, func(tx *gorm.DB) error {
				return tx.Raw(`SELECT count(*) FROM session_submission_diagnostics WHERE submission_id=?::uuid`, staged.ID).Row().Scan(&count)
			}))
			require.Zero(t, count)
			require.Contains(t, logs.String(), "session_diagnostics_unavailable")
			require.Contains(t, logs.String(), "persistence_failed")
			require.NotContains(t, logs.String(), "session-credential-canary")
		})
	}
}
