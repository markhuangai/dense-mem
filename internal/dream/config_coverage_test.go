package dream

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

func TestEffectiveDreamingConfigSourcesAndValidation(t *testing.T) {
	base := domain.DreamingRuntimeConfig{Enabled: true, StartTimeLocal: "04:00", Timezone: "UTC", MaxOutputs: 5}
	if cfg, err := EffectiveDreamingConfig(base, map[string]any{"dreaming": map[string]any{"enabled": false}}); err != nil || cfg.Enabled || cfg.Source != "team" {
		t.Fatalf("team override = %+v, %v", cfg, err)
	}
	force := base
	force.ForceEnabled = true
	if cfg, err := EffectiveDreamingConfig(force, nil); err != nil || !cfg.Enabled || cfg.Source != "global_force" {
		t.Fatalf("force config = %+v, %v", cfg, err)
	}
	disabled := base
	disabled.Enabled = false
	if cfg, err := EffectiveDreamingConfig(disabled, map[string]any{"dreaming": map[string]any{"enabled": true}}); err != nil || cfg.Enabled || cfg.TeamEnabled {
		t.Fatalf("disabled config = %+v, %v", cfg, err)
	}
	for name, cfg := range []domain.DreamingRuntimeConfig{
		{Enabled: true, StartTimeLocal: "bad", Timezone: "UTC", MaxOutputs: 1},
		{Enabled: true, StartTimeLocal: "04:00", Timezone: "Not/AZone", MaxOutputs: 1},
		{Enabled: true, StartTimeLocal: "04:00", Timezone: "UTC", MaxOutputs: 51},
	} {
		if _, err := EffectiveDreamingConfig(cfg, nil); err == nil {
			t.Fatalf("invalid config %d was accepted", name)
		}
	}
	if cfg, err := EffectiveDreamingConfig(base, map[string]any{"dreaming": map[string]string{"enabled": "true"}}); err != nil || !cfg.Enabled {
		t.Fatalf("string team config = %+v, %v", cfg, err)
	}
	if cfg, err := EffectiveDreamingConfig(base, map[string]any{"dreaming": json.RawMessage(`{"enabled":true}`)}); err != nil || !cfg.Enabled {
		t.Fatalf("raw team config = %+v, %v", cfg, err)
	}
}

func TestDreamConfigHelpersAndActorTeamResolution(t *testing.T) {
	if _, ok := nestedMap(nil, "dreaming"); ok {
		t.Fatal("nil map unexpectedly resolved")
	}
	if _, ok := nestedMap(map[string]any{"dreaming": 1}, "dreaming"); ok {
		t.Fatal("non-map nested value unexpectedly resolved")
	}
	if value, ok := boolFromAny(true); !ok || !value {
		t.Fatal("bool value was not parsed")
	}
	if value, ok := boolFromAny(" false "); !ok || value {
		t.Fatal("string bool was not parsed")
	}
	if _, ok := boolFromAny(" "); ok {
		t.Fatal("blank bool was accepted")
	}
	if _, ok := boolFromAny(1); ok {
		t.Fatal("numeric bool was accepted")
	}
	if got := normalizeRuntimeConfig(domain.DreamingRuntimeConfig{}); got.StartTimeLocal == "" || got.Timezone == "" || got.MaxOutputs <= 0 {
		t.Fatalf("runtime defaults = %+v", got)
	}
	if got, err := globalDreamingConfig(context.Background(), nil); err != nil || got.Enabled {
		t.Fatalf("nil global config = %+v, %v", got, err)
	}
	teamID := uuid.New()
	ctx := requestctx.WithActor(context.Background(), requestctx.Actor{TeamID: teamID})
	if actor, ok := requestctx.ActorFromContext(ctx); !ok || actor.TeamID != teamID {
		t.Fatal("actor context was not retained")
	}
}

func TestDreamServiceGuardsAndLeaseSelection(t *testing.T) {
	svc := New(Dependencies{}).(*service)
	ctx := context.Background()
	if svc.cycleLease(false) != manualDreamCycleLease || svc.cycleLease(true) != scheduledDreamCycleLease {
		t.Fatalf("default leases = %s/%s", svc.cycleLease(false), svc.cycleLease(true))
	}
	svc.deps.ProviderCycleLease = time.Hour
	if svc.cycleLease(false) != time.Hour || svc.evidenceCycleLease() != time.Hour*evidenceDiscoveryTargetLimit*evidenceDiscoveryPassLimit*evidenceDiscoveryRegenerationLimit {
		t.Fatal("provider lease override was not applied")
	}
	for name, call := range map[string]func() error{
		"run":       func() error { _, err := svc.RunCycle(ctx, "", RunCycleRequest{}); return err },
		"scheduled": func() error { _, err := svc.RunScheduledCycle(ctx, "", time.Time{}); return err },
		"evidence":  func() error { _, err := svc.RunScheduledEvidenceCycle(ctx, "", time.Time{}); return err },
		"recover":   func() error { _, err := svc.RecoverScheduledCycle(ctx, ""); return err },
		"missed":    func() error { _, err := svc.RecordMissedScheduledCycle(ctx, "", ""); return err },
		"list":      func() error { _, _, err := svc.List(ctx, "", ListOptions{}); return err },
		"get":       func() error { _, err := svc.Get(ctx, "", ""); return err },
		"runs":      func() error { _, err := svc.ListRuns(ctx, "", 1); return err },
		"recall":    func() error { _, err := svc.Recall(ctx, "", "", 1); return err },
		"feedback":  func() error { _, err := svc.ResolveFeedback(ctx, "", ResolveFeedbackRequest{}); return err },
		"status":    func() error { _, err := svc.Status(ctx, ""); return err },
	} {
		t.Run(name, func(t *testing.T) {
			if err := call(); err == nil {
				t.Fatal("nil dream store was accepted")
			}
		})
	}
	if got := localRunDate(time.Date(2026, 1, 1, 23, 0, 0, 0, time.UTC), EffectiveConfig{DreamingRuntimeConfig: domain.DreamingRuntimeConfig{Timezone: "bad"}}); got != "2026-01-01" {
		t.Fatalf("invalid timezone run date = %q", got)
	}
	if _, err := parseTeamID("bad"); err == nil {
		t.Fatal("invalid team ID was accepted")
	}
}
