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
