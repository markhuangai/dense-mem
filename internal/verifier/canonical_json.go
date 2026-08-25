package verifier

import "github.com/markhuangai/dense-mem/internal/assessor"

// CanonicalJSON keeps the legacy verifier API pointed at the assessor's
// strict canonical JSON implementation.
func CanonicalJSON(raw []byte) ([]byte, error) {
	return assessor.CanonicalJSON(raw)
}
