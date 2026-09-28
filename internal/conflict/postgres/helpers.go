package postgres

import storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"

func activeSemanticSpaceGenerationSQL(alias string) string {
	return storagepostgres.ActiveSemanticSpaceGenerationSQL(alias)
}
