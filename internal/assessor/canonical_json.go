package assessor

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"

	"github.com/markhuangai/dense-mem/internal/jsonstrict"
)

// CanonicalJSON preserves JSON values while serializing object keys in the
// deterministic order used for durable response hashes.
func CanonicalJSON(raw []byte) ([]byte, error) {
	if err := jsonstrict.RejectDuplicateFields(raw); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("JSON contains trailing values")
	}
	return json.Marshal(value)
}
