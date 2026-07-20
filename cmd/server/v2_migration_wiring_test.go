package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
	"github.com/markhuangai/dense-mem/internal/service/migrationexecutor"
	"github.com/markhuangai/dense-mem/internal/storage/neo4j"
)

func TestBuildV2MigrationExecutorFromDependenciesGatesByMode(t *testing.T) {
	cases := []config.Config{
		{V2BootMode: config.V2BootModeOff, V2LegacyMigrationRequired: true},
		{V2BootMode: config.V2BootModeUAT, V2LegacyMigrationRequired: true},
		{V2BootMode: config.V2BootModeDormant, V2LegacyMigrationRequired: false},
	}
	for _, cfg := range cases {
		svc, err := buildV2MigrationExecutorFromDependencies(cfg, nil, nil, nil)
		if err != nil {
			t.Fatalf("buildV2MigrationExecutorFromDependencies(%+v) err = %v", cfg, err)
		}
		if svc != nil {
			t.Fatalf("buildV2MigrationExecutorFromDependencies(%+v) returned service outside required dormant mode", cfg)
		}
	}
}

func TestBuildV2MigrationExecutorFromDependenciesRequiresCredentialAndDeps(t *testing.T) {
	cfg := config.Config{
		V2BootMode:                config.V2BootModeDormant,
		V2LegacyMigrationRequired: true,
	}
	_, err := buildV2MigrationExecutorFromDependencies(cfg, &v2MigrationStoreStub{}, &legacyCorpusReaderStub{}, &migrationRememberStub{})
	if err == nil {
		t.Fatal("expected missing credential error, got nil")
	}
	var validationErr *config.ValidationError
	if !errors.As(err, &validationErr) || validationErr.Field != "V2_MIGRATION_CREDENTIAL_ID" {
		t.Fatalf("err = %v; want V2_MIGRATION_CREDENTIAL_ID validation error", err)
	}

	cfg.V2MigrationCredentialID = "11111111-1111-4111-8111-111111111111"
	_, err = buildV2MigrationExecutorFromDependencies(cfg, nil, &legacyCorpusReaderStub{}, &migrationRememberStub{})
	if !errors.Is(err, migrationexecutor.ErrMissingDependency) {
		t.Fatalf("err = %v; want ErrMissingDependency", err)
	}

	svc, err := buildV2MigrationExecutorFromDependencies(cfg, &v2MigrationStoreStub{}, &legacyCorpusReaderStub{}, &migrationRememberStub{})
	if err != nil {
		t.Fatalf("buildV2MigrationExecutorFromDependencies err = %v", err)
	}
	if svc == nil {
		t.Fatal("service = nil; want migration executor")
	}
}

type v2MigrationStoreStub struct{}

func (s *v2MigrationStoreStub) GetLatestRun(context.Context) (*domain.V2MigrationRun, error) {
	return nil, nil
}

func (s *v2MigrationStoreStub) UpsertMigrationCorpusItem(context.Context, repository.V2UpsertMigrationCorpusItemInput) (*domain.V2MigrationCorpusItem, error) {
	return nil, nil
}

func (s *v2MigrationStoreStub) UpdateMigrationCorpusOutcome(context.Context, repository.V2UpdateMigrationCorpusOutcomeInput) (*domain.V2MigrationCorpusItem, error) {
	return nil, nil
}

func (s *v2MigrationStoreStub) UpsertMigrationSourceMap(context.Context, repository.V2UpsertMigrationSourceMapInput) error {
	return nil
}

func (s *v2MigrationStoreStub) UpsertMigrationCheckpoint(context.Context, repository.V2UpsertMigrationCheckpointInput) error {
	return nil
}

func (s *v2MigrationStoreStub) GetMigrationCheckpoint(context.Context, string, string) (map[string]any, error) {
	return nil, nil
}

func (s *v2MigrationStoreStub) RecordMigrationError(context.Context, repository.V2RecordMigrationErrorInput) error {
	return nil
}

func (s *v2MigrationStoreStub) RecordMigrationExclusion(context.Context, repository.V2RecordMigrationExclusionInput) error {
	return nil
}

func (s *v2MigrationStoreStub) RefreshMigrationRunStats(context.Context, string, time.Time) (*domain.V2MigrationRun, error) {
	return nil, nil
}

type legacyCorpusReaderStub struct{}

func (s *legacyCorpusReaderStub) ReadCorpusPage(context.Context, neo4j.LegacyCorpusPageRequest) (neo4j.LegacyCorpusPage, error) {
	return neo4j.LegacyCorpusPage{}, nil
}

type migrationRememberStub struct{}

func (s *migrationRememberStub) RememberV2(context.Context, memoryservice.V2RememberRequest) (*memoryservice.V2RememberResult, error) {
	return nil, nil
}
