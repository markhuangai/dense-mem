package embedding

const EmbeddingContractVersion = "dense-mem.v2.embedding.v1"

type EmbeddingSourceKind string

const (
	EmbeddingSourceEvidence       EmbeddingSourceKind = "evidence"
	EmbeddingSourceSearchDocument EmbeddingSourceKind = "search_document"
	EmbeddingSourceRecallQuery    EmbeddingSourceKind = "recall_query"
)

func EmbeddingSourceKinds() []string {
	return []string{
		string(EmbeddingSourceEvidence),
		string(EmbeddingSourceSearchDocument),
		string(EmbeddingSourceRecallQuery),
	}
}
