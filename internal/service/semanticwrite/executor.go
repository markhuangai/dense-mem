package semanticwrite

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

const MaxDocuments = 256

var (
	ErrInvalidPlan             = errors.New("semantic write: invalid embedding plan")
	ErrProviderUnavailable     = errors.New("semantic write: embedding provider unavailable")
	ErrProviderResponseInvalid = errors.New("semantic write: embedding provider response invalid")
)

// Document is one rendered search document. Hash is the canonical document
// identity used to associate the provider result with its planned source.
type Document struct {
	Hash string
	Text string
}

// Fence identifies the active search contract that the caller must revalidate
// before committing the derived vectors.
type Fence struct {
	Model                   string
	Dimensions              int
	EmbeddingContractID     string
	SearchGenerationID      string
	SearchGenerationVersion int64
}

// Plan is ordered and must contain one entry per unique document hash.
type Plan struct {
	Documents []Document
	Fence     Fence
	Timeout   time.Duration
}

// Embedding is associated with the exact document hash from the plan rather
// than exposing an unlabelled positional vector to committers.
type Embedding struct {
	DocumentHash string
	Vector       []float32
}

// IndexedEmbedding is the provider response for one input. Providers must
// return the input index explicitly so the executor can fence order and
// association before a caller is allowed to commit derived state.
type IndexedEmbedding struct {
	Index  int
	Vector []float32
}

type Result struct {
	Fence      Fence
	Model      string
	Embeddings []Embedding
}

// BatchProvider is intentionally narrower than concrete provider packages so
// the executor remains a reusable application port.
type BatchProvider interface {
	EmbedBatch(context.Context, []string) ([]IndexedEmbedding, string, error)
	ModelName() string
	Dimensions() int
	IsAvailable() bool
}

type Executor struct {
	provider BatchProvider
}

func NewExecutor(provider BatchProvider) *Executor {
	return &Executor{provider: provider}
}

// Execute performs at most one provider call. An empty plan is a valid
// not-required path and therefore does not call the provider.
func (e *Executor) Execute(ctx context.Context, plan Plan) (Result, error) {
	if err := validatePlan(plan); err != nil {
		return Result{}, err
	}
	result := Result{Fence: plan.Fence, Model: plan.Fence.Model, Embeddings: []Embedding{}}
	if len(plan.Documents) == 0 {
		return result, nil
	}
	if e == nil || e.provider == nil {
		return Result{}, fmt.Errorf("%w: provider is required", ErrProviderUnavailable)
	}
	if !e.provider.IsAvailable() {
		return Result{}, ErrProviderUnavailable
	}
	if model := strings.TrimSpace(e.provider.ModelName()); model != plan.Fence.Model {
		return Result{}, ErrProviderResponseInvalid
	}
	if dimensions := e.provider.Dimensions(); dimensions != plan.Fence.Dimensions {
		return Result{}, ErrProviderResponseInvalid
	}

	texts := make([]string, 0, len(plan.Documents))
	for _, document := range plan.Documents {
		texts = append(texts, document.Text)
	}
	providerCtx, cancel := context.WithTimeout(ctx, plan.Timeout)
	defer cancel()
	if err := providerCtx.Err(); err != nil {
		return Result{}, err
	}
	vectors, model, err := e.provider.EmbedBatch(providerCtx, texts)
	if err != nil {
		if providerCtx.Err() != nil {
			return Result{}, providerCtx.Err()
		}
		return Result{}, fmt.Errorf("%w: provider call failed", ErrProviderUnavailable)
	}
	if err := providerCtx.Err(); err != nil {
		return Result{}, err
	}
	if model != plan.Fence.Model {
		return Result{}, ErrProviderResponseInvalid
	}
	if len(vectors) != len(plan.Documents) {
		return Result{}, ErrProviderResponseInvalid
	}

	result.Embeddings = make([]Embedding, len(plan.Documents))
	seenIndices := make(map[int]struct{}, len(vectors))
	for responseIndex, item := range vectors {
		if item.Index < 0 || item.Index >= len(plan.Documents) {
			return Result{}, ErrProviderResponseInvalid
		}
		if _, ok := seenIndices[item.Index]; ok || item.Index != responseIndex {
			return Result{}, ErrProviderResponseInvalid
		}
		seenIndices[item.Index] = struct{}{}
		if err := validateVector(item.Vector, plan.Fence.Dimensions); err != nil {
			return Result{}, ErrProviderResponseInvalid
		}
		result.Embeddings[responseIndex] = Embedding{
			DocumentHash: plan.Documents[item.Index].Hash,
			Vector:       append([]float32(nil), item.Vector...),
		}
	}
	return result, nil
}

func validatePlan(plan Plan) error {
	if len(plan.Documents) > MaxDocuments {
		return fmt.Errorf("%w: %d documents exceeds limit %d", ErrInvalidPlan, len(plan.Documents), MaxDocuments)
	}
	if plan.Timeout <= 0 {
		return fmt.Errorf("%w: timeout must be positive", ErrInvalidPlan)
	}
	if strings.TrimSpace(plan.Fence.Model) == "" ||
		strings.TrimSpace(plan.Fence.Model) != plan.Fence.Model ||
		plan.Fence.Dimensions <= 0 ||
		strings.TrimSpace(plan.Fence.EmbeddingContractID) == "" ||
		strings.TrimSpace(plan.Fence.EmbeddingContractID) != plan.Fence.EmbeddingContractID ||
		strings.TrimSpace(plan.Fence.SearchGenerationID) == "" ||
		strings.TrimSpace(plan.Fence.SearchGenerationID) != plan.Fence.SearchGenerationID ||
		plan.Fence.SearchGenerationVersion < 1 {
		return fmt.Errorf("%w: complete active search fence is required", ErrInvalidPlan)
	}
	seen := make(map[string]struct{}, len(plan.Documents))
	for index, document := range plan.Documents {
		hash := strings.TrimSpace(document.Hash)
		if hash == "" || hash != document.Hash || strings.TrimSpace(document.Text) == "" {
			return fmt.Errorf("%w: document %d requires hash and text", ErrInvalidPlan, index)
		}
		if _, ok := seen[hash]; ok {
			return fmt.Errorf("%w: document hash %q is duplicated", ErrInvalidPlan, hash)
		}
		seen[hash] = struct{}{}
	}
	return nil
}

func validateVector(vector []float32, dimensions int) error {
	if len(vector) != dimensions {
		return fmt.Errorf("dimensions %d, expected %d", len(vector), dimensions)
	}
	for index, value := range vector {
		f := float64(value)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return fmt.Errorf("non-finite value at index %d", index)
		}
	}
	return nil
}
