package embedding

import embeddingcontract "github.com/markhuangai/dense-mem/internal/embedding/contract"

const EmbeddingContractVersion = embeddingcontract.EmbeddingContractVersion

type EmbeddingSourceKind = embeddingcontract.EmbeddingSourceKind

const (
	EmbeddingSourceEvidence       = embeddingcontract.EmbeddingSourceEvidence
	EmbeddingSourceSearchDocument = embeddingcontract.EmbeddingSourceSearchDocument
	EmbeddingSourceRecallQuery    = embeddingcontract.EmbeddingSourceRecallQuery
)

func EmbeddingSourceKinds() []string { return embeddingcontract.EmbeddingSourceKinds() }
