//go:build integration

package postgres

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	organization "github.com/markhuangai/dense-mem/internal/ontology"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/stretchr/testify/require"
)

type finalizationFixture struct {
	f         *ontologyFixture
	window    *ontology.MaintenanceWindow
	claim     *ontology.MaintenanceClaim
	organizer *organization.Service
	calls     *atomic.Int32
	result    ontology.OrganizationResult
}

type finalizationAccounting struct {
	store *Store
	claim ontology.MaintenanceClaim
}

func (a finalizationAccounting) BeforeAttempt(ctx context.Context, id string, number, input, output int) (context.Context, error) {
	return modelprovider.WithAdmissionCallback(ctx, func(dispatch context.Context) error {
		return a.store.AdmitMaintenanceAttempt(dispatch, a.claim, id, number, input, output, time.Now().UTC())
	}), nil
}

func (a finalizationAccounting) AfterAttempt(ctx context.Context, id string, attempt ontology.AssessmentAttempt, _ bool) error {
	return a.store.ReconcileMaintenanceAttempt(ctx, a.claim, id, attempt)
}

func newFinalizationFixture(t *testing.T, texts ...string) *finalizationFixture {
	t.Helper()
	f := newOrganizationFixture(t)
	if len(texts) == 0 {
		texts = []string{"Atlas uses PostgreSQL for storage.", "Atlas stores data in PostgreSQL."}
	}
	for i, text := range texts {
		f.organizationEvidence(t, i%len(f.owners), text, nil)
	}
	config := concurrencyConfig(t, f, 4, 4)
	window := maintenanceWindow(t, f, config)
	_, err := f.store.MaintenanceCommand(context.Background(), domain.OntologyMaintenanceCommand{Action: "run", OperationKey: uuid.NewString(), MaxBatches: 1}, time.Now().UTC(), nil)
	require.NoError(t, err)
	_, claim := maintenanceClaim(t, f, window)
	organizer, calls := organizationFixtureServiceWithAccounting(t, f, nil, nil, "fixture-model", maintenanceFixtureLimits(), finalizationAccounting{f.store, *claim}, 300000, map[string]int{"prompt_tokens": 17, "completion_tokens": 9, "total_tokens": 26})
	return &finalizationFixture{f: f, window: window, claim: claim, organizer: organizer, calls: calls}
}

func (x *finalizationFixture) stage(t *testing.T) {
	t.Helper()
	var err error
	x.result, err = x.organizer.Organize(ontology.WithMaintenanceClaim(context.Background(), *x.claim), x.f.team,
		ontology.OrganizationInput{OperationKey: "maintenance:" + x.claim.ID, Sources: x.claim.Sources})
	require.NoError(t, err)
	require.True(t, x.result.Current)
}

func (x *finalizationFixture) batchStatus(t *testing.T) string {
	t.Helper()
	var status string
	require.NoError(t, x.f.admin.Raw(`SELECT status FROM ontology_maintenance_batches WHERE batch_id=?::uuid`, x.claim.ID).Row().Scan(&status))
	return status
}

func (x *finalizationFixture) recover(t *testing.T) {
	t.Helper()
	turn, err := NewStore(x.f.app, x.f.rls).ClaimMaintenanceTurn(context.Background(), x.window.ID, time.Now().UTC(), time.Minute)
	require.NoError(t, err)
	if turn != nil {
		require.NoError(t, x.f.store.ReleaseMaintenanceTurn(context.Background(), *turn))
	}
}
