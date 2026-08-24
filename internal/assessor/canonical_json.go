package assessor

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// CanonicalJSON preserves JSON values while serializing object keys in the
// deterministic order used for durable response hashes.
func CanonicalJSON(raw []byte) ([]byte, error) {
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
