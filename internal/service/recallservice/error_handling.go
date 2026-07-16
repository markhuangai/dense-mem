package recallservice

import (
	"errors"

	"github.com/markhuangai/dense-mem/internal/embedding"
	"github.com/markhuangai/dense-mem/internal/observability"
)

// sanitizeEmbeddingError classifies the provider error type but strips any
// message contents so provider internals never surface to callers.
func sanitizeEmbeddingError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, embedding.ErrEmbeddingTimeout):
		return errors.New("recall: embedding timeout")
	case errors.Is(err, embedding.ErrEmbeddingRateLimit):
		return errors.New("recall: embedding rate limited")
	case errors.Is(err, embedding.ErrEmbeddingProvider):
		return errors.New("recall: embedding provider error")
	}
	return errors.New("recall: embedding unavailable")
}

func (s *recallService) logEmbeddingError(err error) {
	if s.logger == nil {
		return
	}
	s.logger.Warn("recall: embedding provider failed", observability.String("error", err.Error()))
}

func (s *recallService) logKeywordError(err error) {
	if s.logger == nil {
		return
	}
	s.logger.Error("recall: keyword branch failed", err)
}

func (s *recallService) logHydrateError(id string, err error) {
	if s.logger == nil {
		return
	}
	s.logger.Warn("recall: hydrate miss",
		observability.String("fragment_id", id),
		observability.String("error", err.Error()),
	)
}
