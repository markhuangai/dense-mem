package main

import (
	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
	"github.com/markhuangai/dense-mem/internal/service/migrationexecutor"
	"github.com/markhuangai/dense-mem/internal/storage/neo4j"
)

func buildV2MigrationExecutor(
	cfg config.Config,
	store migrationexecutor.Store,
	neo4jClient neo4j.Neo4jClientInterface,
	ledger *repository.V2LedgerRepositoryImpl,
) (migrationexecutor.Service, error) {
	if cfg.GetV2BootMode() != config.V2BootModeDormant || !cfg.GetV2LegacyMigrationRequired() {
		return nil, nil
	}
	if neo4jClient == nil || ledger == nil {
		return nil, migrationexecutor.ErrMissingDependency
	}
	reader, err := neo4j.NewLegacyCorpusMigrationAdapter(neo4jClient, neo4j.LegacyCorpusAdapterConfig{Enabled: true})
	if err != nil {
		return nil, err
	}
	remember := memoryservice.NewV2RememberService(memoryservice.V2RememberDependencies{Ledger: ledger})
	return buildV2MigrationExecutorFromDependencies(cfg, store, reader, remember)
}

func buildV2MigrationExecutorFromDependencies(
	cfg config.Config,
	store migrationexecutor.Store,
	reader migrationexecutor.LegacyCorpusReader,
	remember migrationexecutor.RememberService,
) (migrationexecutor.Service, error) {
	if cfg.GetV2BootMode() != config.V2BootModeDormant || !cfg.GetV2LegacyMigrationRequired() {
		return nil, nil
	}
	if store == nil || reader == nil || remember == nil {
		return nil, migrationexecutor.ErrMissingDependency
	}
	return migrationexecutor.New(store, reader, remember, migrationexecutor.Config{
		WorkerID: "server-v2-migration",
	}), nil
}
