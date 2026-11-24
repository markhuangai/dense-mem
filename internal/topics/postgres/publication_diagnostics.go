package postgres

import (
	topic "github.com/markhuangai/dense-mem/internal/topics/contract"
	"gorm.io/gorm"
)

func notePublicationRevision(tx *gorm.DB, revision int64) {
	if diagnostic := topic.PublicationDiagnosticFromContext(tx.Statement.Context); diagnostic != nil {
		diagnostic.ObservedRevision = &revision
	}
}

func notePublicationDefinitions(tx *gorm.DB, records map[string]topic.Record) {
	diagnostic := topic.PublicationDiagnosticFromContext(tx.Statement.Context)
	if diagnostic == nil {
		return
	}
	if diagnostic.Definitions == nil {
		diagnostic.Definitions = map[string]topic.Record{}
	}
	for id, record := range records {
		if record.Definition == nil {
			continue
		}
		if _, exists := diagnostic.Definitions[id]; !exists && len(diagnostic.Definitions) >= topic.MaxDependencyRecords {
			diagnostic.DefinitionsState = "truncated"
			continue
		}
		diagnostic.Definitions[id] = record
	}
}
