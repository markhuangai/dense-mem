package verifier

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

// PrepareSemanticAssessmentEvidence adds request-local boundary references
// without changing the exact evidence used by durable span validation.
func PrepareSemanticAssessmentEvidence(evidence SemanticReviewEvidence) SemanticReviewEvidence {
	runes := []rune(evidence.Content)
	prefix := semanticAssessmentBoundaryPrefix(evidence.Content)
	evidence.BoundaryPrefix = prefix
	refs := make(map[string]int, len(runes)+1)
	var annotated strings.Builder
	annotated.Grow(len(evidence.Content) + (len(runes)+1)*16)
	for index := 0; index <= len(runes); index++ {
		ref := prefix + strconv.FormatInt(int64(index), 36)
		refs[ref] = index
		annotated.WriteString("⟦")
		annotated.WriteString(ref)
		annotated.WriteString("⟧")
		if index < len(runes) {
			annotated.WriteRune(runes[index])
		}
	}
	evidence.BoundaryText = annotated.String()
	evidence.BoundaryRefs = refs
	return evidence
}

func semanticAssessmentBoundaryPrefix(content string) string {
	for salt := 0; ; salt++ {
		digest := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%s", salt, content)))
		prefix := "b" + hex.EncodeToString(digest[:4]) + "_"
		if !strings.Contains(content, "⟦"+prefix) {
			return prefix
		}
	}
}

func semanticAssessmentBoundaryOffset(evidence SemanticReviewEvidence, ref string) (int, bool) {
	offset, ok := evidence.BoundaryRefs[strings.TrimSpace(ref)]
	return offset, ok
}

// SemanticAssessmentBoundaryRef returns the request-local reference for one
// already validated Unicode code-point boundary.
func SemanticAssessmentBoundaryRef(evidence SemanticReviewEvidence, offset int) (string, bool) {
	if offset < 0 {
		return "", false
	}
	if evidence.BoundaryPrefix != "" {
		ref := evidence.BoundaryPrefix + strconv.FormatInt(int64(offset), 36)
		if candidate, ok := evidence.BoundaryRefs[ref]; ok && candidate == offset {
			return ref, true
		}
		return "", false
	}
	for ref, candidate := range evidence.BoundaryRefs {
		if candidate == offset {
			return ref, true
		}
	}
	return "", false
}
