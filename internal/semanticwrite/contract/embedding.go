// Package contract contains the provider-independent synchronous semantic
// write plan and result contracts.
package contract

import (
	"context"
	"time"
)

type Document struct {
	Hash string
	Text string
}

type Fence struct {
	Model                   string
	Dimensions              int
	EmbeddingContractID     string
	SearchGenerationID      string
	SearchGenerationVersion int64
}

func (f Fence) LogFields() map[string]any {
	return map[string]any{
		"model": f.Model, "dimensions": f.Dimensions, "embedding_contract_id": f.EmbeddingContractID,
		"search_generation_id": f.SearchGenerationID, "search_generation_version": f.SearchGenerationVersion,
	}
}

type Plan struct {
	Documents []Document
	Fence     Fence
	Timeout   time.Duration
}

type Embedding struct {
	DocumentHash string
	Vector       []float32
}

type IndexedEmbedding struct {
	Index  int
	Vector []float32
}

type Result struct {
	Fence      Fence
	Model      string
	Embeddings []Embedding
}

type BatchProvider interface {
	EmbedBatch(context.Context, []string) ([]IndexedEmbedding, string, error)
	ModelName() string
	Dimensions() int
	IsAvailable() bool
}
