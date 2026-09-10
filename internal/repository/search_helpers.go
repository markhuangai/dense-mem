package repository

import (
	searchpostgres "github.com/markhuangai/dense-mem/internal/search/postgres"
)

const searchDefaultQueryEFSearch = 40

type searchHitScanner interface {
	Scan(dest ...any) error
}

func scanSearchHit(scanner searchHitScanner) (SearchHit, error) {
	return searchpostgres.ScanSearchHit(scanner)
}

// Search read validation and index compatibility helpers are retained as
// forwarding seams for legacy repository tests and recall SQL builders.
func normalizeFullTextSearchInput(input FullTextSearchInput) FullTextSearchInput {
	return searchpostgres.NormalizeFullTextSearchInput(input)
}

func validateFullTextSearchInput(input FullTextSearchInput) error {
	return searchpostgres.ValidateFullTextSearchInput(input)
}

func normalizeExactVectorSearchInput(input ExactVectorSearchInput) ExactVectorSearchInput {
	return searchpostgres.NormalizeExactVectorSearchInput(input)
}

func validateExactVectorSearchInput(input ExactVectorSearchInput) error {
	return searchpostgres.ValidateExactVectorSearchInput(input)
}

func searchMissingIndexCompatibility(contract *ActiveSearchContract, indexDefinition string) []string {
	return searchpostgres.SearchMissingIndexCompatibility(contract, indexDefinition)
}

func searchIndexExpressionCompatible(contract *ActiveSearchContract, indexDefinition string) bool {
	return searchpostgres.SearchIndexExpressionCompatible(contract, indexDefinition)
}

func vectorLiteral(values []float32) (string, error) {
	return searchpostgres.VectorLiteral(values)
}

// Keep these aliases available to retained package-local fixtures.
func validSearchSourceKind(kind string) bool  { return searchpostgres.ValidSearchSourceKind(kind) }
func defaultProjectionFormat(kind string) int { return searchpostgres.DefaultProjectionFormat(kind) }
