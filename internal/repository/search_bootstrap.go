package repository

import (
	searchmaintenance "github.com/markhuangai/dense-mem/internal/search/maintenance"
	searchpostgres "github.com/markhuangai/dense-mem/internal/search/postgres"
)

// Bootstrap helpers remain compatibility aliases for repository fixtures. The
// native PostgreSQL adapter owns their implementation.
type searchIndexGenerationDefinition = searchpostgres.SearchIndexGenerationDefinition

func normalizeEnsureActiveSearchContractInput(input EnsureActiveSearchContractInput) EnsureActiveSearchContractInput {
	normalized := searchpostgres.NormalizeEnsureActiveSearchContractInput(searchmaintenance.EnsureActiveSearchContractInput{
		Provider: input.Provider, Model: input.Model, Dimensions: input.Dimensions,
		VectorNormalization: input.VectorNormalization, DocumentFormatVersion: input.DocumentFormatVersion,
		QueryFormatVersion: input.QueryFormatVersion, ExactMaxRows: input.ExactMaxRows,
		CandidateLimit: input.CandidateLimit,
	})
	return EnsureActiveSearchContractInput{
		Provider: normalized.Provider, Model: normalized.Model, Dimensions: normalized.Dimensions,
		VectorNormalization: normalized.VectorNormalization, DocumentFormatVersion: normalized.DocumentFormatVersion,
		QueryFormatVersion: normalized.QueryFormatVersion, ExactMaxRows: normalized.ExactMaxRows,
		CandidateLimit: normalized.CandidateLimit,
	}
}

func validateEnsureActiveSearchContractInput(input EnsureActiveSearchContractInput) error {
	return searchpostgres.ValidateEnsureActiveSearchContractInput(searchmaintenance.EnsureActiveSearchContractInput{
		Provider: input.Provider, Model: input.Model, Dimensions: input.Dimensions,
		VectorNormalization: input.VectorNormalization, DocumentFormatVersion: input.DocumentFormatVersion,
		QueryFormatVersion: input.QueryFormatVersion, ExactMaxRows: input.ExactMaxRows,
		CandidateLimit: input.CandidateLimit,
	})
}

func validateActiveContractMatchesConfig(contract *ActiveSearchContract, input EnsureActiveSearchContractInput) error {
	return searchpostgres.ValidateActiveContractMatchesConfig(contract, searchmaintenance.EnsureActiveSearchContractInput{
		Provider: input.Provider, Model: input.Model, Dimensions: input.Dimensions,
		VectorNormalization: input.VectorNormalization, DocumentFormatVersion: input.DocumentFormatVersion,
		QueryFormatVersion: input.QueryFormatVersion, ExactMaxRows: input.ExactMaxRows,
		CandidateLimit: input.CandidateLimit,
	})
}

func deriveSearchGenerationSpec(contractID string, input EnsureActiveSearchContractInput) searchIndexGenerationDefinition {
	return searchpostgres.DeriveSearchGenerationSpec(contractID, searchmaintenance.EnsureActiveSearchContractInput{
		Provider: input.Provider, Model: input.Model, Dimensions: input.Dimensions,
		VectorNormalization: input.VectorNormalization, DocumentFormatVersion: input.DocumentFormatVersion,
		QueryFormatVersion: input.QueryFormatVersion, ExactMaxRows: input.ExactMaxRows,
		CandidateLimit: input.CandidateLimit,
	})
}

func validateSearchGenerationMatchesSpec(generation, spec searchIndexGenerationDefinition) error {
	return searchpostgres.ValidateSearchGenerationMatchesSpec(generation, spec)
}

// Keep these names available to any retained repository-only fixture code.
func embeddingContractKey(input EnsureActiveSearchContractInput) string {
	return searchpostgres.EmbeddingContractKey(searchmaintenance.EnsureActiveSearchContractInput{
		Provider: input.Provider, Model: input.Model, Dimensions: input.Dimensions,
		VectorNormalization: input.VectorNormalization, DocumentFormatVersion: input.DocumentFormatVersion,
		QueryFormatVersion: input.QueryFormatVersion, ExactMaxRows: input.ExactMaxRows,
		CandidateLimit: input.CandidateLimit,
	})
}

func derivedSearchIndexName(contractID string, dimensions int, strategy string) string {
	return searchpostgres.DerivedSearchIndexName(contractID, dimensions, strategy)
}

func searchPhysicalIndexNameMatchesSpec(physicalIndexName string, spec searchIndexGenerationDefinition) bool {
	return searchpostgres.SearchPhysicalIndexNameMatchesSpec(physicalIndexName, spec)
}
