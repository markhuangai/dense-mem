package postgres

import (
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"gorm.io/gorm"
)

func notePublicationRevision(tx *gorm.DB, revision int64) {
	if diagnostic := ontology.PublicationDiagnosticFromContext(tx.Statement.Context); diagnostic != nil {
		diagnostic.ObservedRevision = &revision
	}
}

func notePublicationDefinitions(tx *gorm.DB, records map[string]ontology.Record) {
	diagnostic := ontology.PublicationDiagnosticFromContext(tx.Statement.Context)
	if diagnostic == nil {
		return
	}
	if diagnostic.Definitions == nil {
		diagnostic.Definitions = map[string]ontology.Record{}
	}
	for id, record := range records {
		if record.Definition == nil {
			continue
		}
		if _, exists := diagnostic.Definitions[id]; !exists && len(diagnostic.Definitions) >= ontology.MaxDependencyRecords {
			diagnostic.DefinitionsState = "truncated"
			continue
		}
		diagnostic.Definitions[id] = record
	}
}
