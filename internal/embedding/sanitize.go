package embedding

import embeddingcontract "github.com/markhuangai/dense-mem/internal/embedding/contract"

func SanitizeError(err error, apiKey string) error {
	return embeddingcontract.SanitizeError(err, apiKey)
}

func ScrubLogFields(fields ...any) []any {
	return embeddingcontract.ScrubLogFields(fields...)
}
