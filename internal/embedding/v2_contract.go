package embedding

const V2EmbeddingContractVersion = "dense-mem.v2.embedding.v1"

type V2EmbeddingSourceKind string

const (
	V2EmbeddingSourceEvidence       V2EmbeddingSourceKind = "evidence"
	V2EmbeddingSourceSearchDocument V2EmbeddingSourceKind = "search_document"
	V2EmbeddingSourceRecallQuery    V2EmbeddingSourceKind = "recall_query"
)

func V2EmbeddingSourceKinds() []string {
	return []string{
		string(V2EmbeddingSourceEvidence),
		string(V2EmbeddingSourceSearchDocument),
		string(V2EmbeddingSourceRecallQuery),
	}
}
