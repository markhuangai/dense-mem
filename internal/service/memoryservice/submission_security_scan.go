package memoryservice

import (
	"encoding/base64"
	"html"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

const (
	securityScanPolicyHash                 = "sha256:40ea2a210d8f6e510738e76445250a1f1aaefb9089ef2fddb222d1ac46ada223"
	SubmissionSecurityErrorEncodedEvidence = "encoded_evidence_not_allowed"
	SubmissionSecurityErrorRejected        = "evidence_security_rejected"

	submissionSecurityMaxEncodedCandidates = 8
	submissionSecurityMaxDecodedBytes      = 4 << 10
	submissionSecurityMinBase64TokenLength = 16
	submissionSecurityMinFoldedBase64Part  = 4
)

var (
	ErrEncodedEvidenceNotAllowed = &SubmissionSecurityError{Code: SubmissionSecurityErrorEncodedEvidence}
	ErrEvidenceSecurityRejected  = &SubmissionSecurityError{Code: SubmissionSecurityErrorRejected}

	base64TokenPattern          = regexp.MustCompile(`[-A-Za-z0-9+/_=]{16,}`)
	percentEscape               = regexp.MustCompile(`%[0-9A-Fa-f]{2}`)
	hexEscape                   = regexp.MustCompile(`\\x[0-9A-Fa-f]{2}`)
	jsonEscape                  = regexp.MustCompile(`\\u[0-9A-Fa-f]{4}`)
	dangerousRole               = regexp.MustCompile(`(?i)\b(?:system|developer|assistant|user|tool|function)\s*:`)
	dangerousOverride           = regexp.MustCompile(`(?i)\b(?:ignore|disregard|forget|override)\s+(?:all\s+)?(?:previous|prior|above|earlier)\s+(?:instructions?|rules?|prompts?)\b`)
	dangerousImperativeOverride = regexp.MustCompile(`(?i)(?:^|[.!?\n]\s*)(?:please\s+)?(?:ignore|disregard|forget|override)\b`)
	dangerousSecret             = regexp.MustCompile(`(?i)\b(?:reveal|show|send|dump|print|exfiltrate)\b.{0,80}\b(?:system\s+prompt|hidden\s+instructions?|environment\s+variables?|env(?:ironment)?|api[_ -]?keys?|credentials?|secrets?)\b`)
	dangerousExfil              = regexp.MustCompile(`(?i)\b(?:curl|wget|fetch|post|send|upload|exfiltrate)\b.{0,120}(?:https?://|webhook|api\s*(?:call|endpoint)|environment\s+variables?|credentials?|secrets?)`)
	dangerousMarkup             = regexp.MustCompile(`(?is)<!--|<\s*(?:script|iframe|object|embed|meta)\b`)
)

// SubmissionSecurityError intentionally contains no evidence text. It is safe
// to pass through bounded public error handling without leaking a rejected
// payload or its decoded representation.
type SubmissionSecurityError struct {
	Code string
}

func (e *SubmissionSecurityError) Error() string {
	if e == nil || strings.TrimSpace(e.Code) == "" {
		return SubmissionSecurityErrorRejected
	}
	return e.Code
}

func (e *SubmissionSecurityError) Is(target error) bool {
	other, ok := target.(*SubmissionSecurityError)
	return ok && e != nil && e.Code == other.Code
}

type SubmissionSecuritySignal struct {
	Kind  string
	Start int
	End   int
}

type SubmissionSecurityScan struct {
	Signals []SubmissionSecuritySignal
}

// ScanSubmissionEvidence performs deterministic, bounded inspection before a
// submission is allocated, idempotency is recorded, or evidence is staged.
// It does not decode recursively and it never returns rejected text.
func ScanSubmissionEvidence(content string) (SubmissionSecurityScan, error) {
	if err := rejectEncodedEvidence(content); err != nil {
		return SubmissionSecurityScan{}, err
	}
	view := normalizedSubmissionScanView(content)
	if signal, found := dangerousUnicodeSignal(content); found {
		return SubmissionSecurityScan{}, securityReject(signal)
	}
	if signal, found := dangerousSubmissionPattern(view.text, view.original, dangerousMarkup, "hidden_control_markup"); found {
		return SubmissionSecurityScan{}, securityReject(signal)
	}
	for _, pattern := range []struct {
		pattern *regexp.Regexp
		kind    string
	}{
		{dangerousOverride, "instruction_override"},
		{dangerousImperativeOverride, "instruction_override"},
		{dangerousRole, "role_control_spoofing"},
		{dangerousSecret, "prompt_secret_extraction"},
		{dangerousExfil, "tool_exfiltration"},
	} {
		if signal, found := dangerousSubmissionPattern(view.text, view.original, pattern.pattern, pattern.kind); found {
			return SubmissionSecurityScan{}, securityReject(signal)
		}
	}

	decoded, decodedMap := oneLayerSubmissionDecode(content)
	if decoded != content {
		if err := rejectEncodedEvidence(decoded); err != nil {
			return SubmissionSecurityScan{}, err
		}
		if signal, found := dangerousUnicodeSignal(decoded); found {
			return SubmissionSecurityScan{}, securityReject(mapDecodedSignal(signal, decodedMap, len([]rune(content))))
		}
		decodedView := normalizedSubmissionScanView(decoded)
		for _, pattern := range []struct {
			pattern *regexp.Regexp
			kind    string
		}{
			{dangerousMarkup, "hidden_control_markup"},
			{dangerousOverride, "obfuscated_instruction"},
			{dangerousImperativeOverride, "obfuscated_instruction"},
			{dangerousRole, "role_control_spoofing"},
			{dangerousSecret, "prompt_secret_extraction"},
			{dangerousExfil, "tool_exfiltration"},
		} {
			if signal, found := dangerousSubmissionPattern(decodedView.text, decodedView.original, pattern.pattern, pattern.kind); found {
				original := mapDecodedSignal(signal, decodedMap, len([]rune(content)))
				return SubmissionSecurityScan{}, securityReject(original)
			}
		}
	}
	return SubmissionSecurityScan{Signals: []SubmissionSecuritySignal{}}, nil
}

func securityReject(signal SubmissionSecuritySignal) error {
	if signal.Kind == "encoded_evidence" {
		return ErrEncodedEvidenceNotAllowed
	}
	return ErrEvidenceSecurityRejected
}

func dangerousUnicodeSignal(content string) (SubmissionSecuritySignal, bool) {
	for index, value := range []rune(content) {
		if value == '\u200b' || value == '\u200c' || value == '\u200d' || value == '\u200e' || value == '\u200f' ||
			value == '\u2060' || (value >= '\u202a' && value <= '\u202e') || (value >= '\u2066' && value <= '\u2069') ||
			(unicode.IsControl(value) && value != '\n' && value != '\r' && value != '\t') {
			return SubmissionSecuritySignal{Kind: "hidden_control_markup", Start: index, End: index + 1}, true
		}
	}
	return SubmissionSecuritySignal{}, false
}

func rejectEncodedEvidence(content string) error {
	lower := strings.ToLower(content)
	if strings.Contains(lower, "data:") && strings.Contains(lower, ";base64,") {
		return ErrEncodedEvidenceNotAllowed
	}
	if strings.Contains(lower, "-----begin ") && strings.Contains(lower, "-----") {
		return ErrEncodedEvidenceNotAllowed
	}
	if looksLikeJWT(content) {
		return ErrEncodedEvidenceNotAllowed
	}

	encodedCandidateCount := 0
	candidates := base64TokenPattern.FindAllStringIndex(content, -1)
	for _, candidate := range candidates {
		encoded := content[candidate[0]:candidate[1]]
		if looksLikeNaturalEvidenceToken(encoded) {
			continue
		}
		encodedCandidateCount++
		if encodedCandidateCount > submissionSecurityMaxEncodedCandidates {
			return ErrEncodedEvidenceNotAllowed
		}
		if isEncodedEvidenceCandidate(encoded) {
			return ErrEncodedEvidenceNotAllowed
		}
	}
	for _, encoded := range foldedBase64Candidates(content) {
		if isEncodedEvidenceCandidate(encoded) {
			return ErrEncodedEvidenceNotAllowed
		}
	}
	return nil
}

func foldedBase64Candidates(content string) []string {
	candidates := make([]string, 0, 1)
	parts := make([]string, 0, 4)
	flush := func() {
		joined := strings.Join(parts, "")
		if len(parts) < 2 || len(joined) < submissionSecurityMinBase64TokenLength {
			parts = parts[:0]
			return
		}
		candidates = append(candidates, joined)
		parts = parts[:0]
	}
	for _, part := range strings.Fields(content) {
		trimmed := trimBase64FragmentDelimiters(part)
		if len(trimmed) >= submissionSecurityMinFoldedBase64Part && isBase64TokenPart(trimmed) && !looksLikeNaturalEvidenceToken(trimmed) {
			parts = append(parts, trimmed)
			continue
		}
		flush()
	}
	flush()
	return candidates
}

func trimBase64FragmentDelimiters(value string) string {
	return strings.Trim(strings.TrimSpace(value), "`'\"()[]{}<>,;:.!?")
}

func isBase64TokenPart(value string) bool {
	for _, runeValue := range value {
		if (runeValue >= 'A' && runeValue <= 'Z') || (runeValue >= 'a' && runeValue <= 'z') ||
			(runeValue >= '0' && runeValue <= '9') || strings.ContainsRune("+/=_-", runeValue) {
			continue
		}
		return false
	}
	return true
}

func looksLikeJWT(content string) bool {
	for _, token := range strings.Fields(content) {
		parts := strings.Split(strings.Trim(token, "`'\"()[]{}<>,;:"), ".")
		if len(parts) != 3 {
			continue
		}
		valid := true
		for _, part := range parts {
			if len(part) < 8 || !isBase64URLPart(part) {
				valid = false
				break
			}
		}
		if valid {
			return true
		}
	}
	return false
}

func isBase64URLPart(value string) bool {
	_, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(value, "="))
	return err == nil
}

func isHighConfidenceBase64(encoded string) bool {
	if len(encoded) < submissionSecurityMinBase64TokenLength || !isBase64EncodedShape(encoded) || base64DecodedByteLength(encoded) > submissionSecurityMaxDecodedBytes {
		return false
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		decoded, err = base64.RawStdEncoding.DecodeString(strings.TrimRight(encoded, "="))
	}
	if err != nil {
		decoded, err = base64.RawURLEncoding.DecodeString(strings.TrimRight(encoded, "="))
	}
	if err != nil || len(decoded) == 0 || len(decoded) > submissionSecurityMaxDecodedBytes {
		return false
	}
	if strings.ContainsAny(encoded, "=+") || hasBinaryMagic(decoded) {
		return true
	}
	if looksLikeNaturalEvidenceToken(encoded) {
		return false
	}
	if base64CharacterClassCount(encoded) >= 3 {
		return true
	}
	printable := 0
	for _, value := range decoded {
		if value == '\n' || value == '\r' || value == '\t' || (value >= 0x20 && value <= 0x7e) {
			printable++
		}
	}
	return printable*100 >= len(decoded)*85
}

func isEncodedEvidenceCandidate(encoded string) bool {
	return isOversizedBase64(encoded) || isHighConfidenceBase64(encoded)
}

func isOversizedBase64(encoded string) bool {
	return !looksLikeNaturalEvidenceToken(encoded) && isBase64EncodedShape(encoded) && base64DecodedByteLength(encoded) > submissionSecurityMaxDecodedBytes
}

func isBase64EncodedShape(encoded string) bool {
	if encoded == "" || !isBase64TokenPart(encoded) {
		return false
	}
	trimmed := strings.TrimRight(encoded, "=")
	paddingLength := len(encoded) - len(trimmed)
	if trimmed == "" || paddingLength > 2 || strings.Contains(trimmed, "=") {
		return false
	}
	return len(trimmed)%4 != 1
}

func base64DecodedByteLength(encoded string) int {
	return len(strings.TrimRight(encoded, "=")) * 3 / 4
}

func looksLikeNaturalEvidenceToken(value string) bool {
	parts := strings.FieldsFunc(value, func(r rune) bool {
		return r == '/' || r == '-' || r == '_'
	})
	if len(parts) == 0 {
		return false
	}
	if len(parts) == 1 {
		return looksLikeNaturalEvidenceWord(parts[0])
	}
	hasNaturalWord := false
	for _, part := range parts {
		if looksLikeNaturalEvidenceWord(part) {
			hasNaturalWord = true
			continue
		}
		if !looksLikeTechnicalEvidenceToken(part) {
			return false
		}
	}
	return hasNaturalWord
}

func looksLikeNaturalEvidenceWord(value string) bool {
	if value == "" {
		return false
	}
	hasLower := false
	lowerPrefixUpperSuffix := false
	for index, value := range value {
		if value >= 'a' && value <= 'z' {
			if lowerPrefixUpperSuffix {
				return false
			}
			hasLower = true
			continue
		}
		if index == 0 && value >= 'A' && value <= 'Z' {
			continue
		}
		if index > 0 && hasLower && value >= 'A' && value <= 'Z' {
			lowerPrefixUpperSuffix = true
			continue
		}
		return false
	}
	return true
}

func looksLikeTechnicalEvidenceToken(value string) bool {
	if value == "" || len(value) > 16 {
		return false
	}
	hasUpper := false
	hasLower := false
	hasDigit := false
	for _, value := range value {
		switch {
		case value >= 'A' && value <= 'Z':
			hasUpper = true
		case value >= 'a' && value <= 'z':
			hasLower = true
		case value >= '0' && value <= '9':
			hasDigit = true
		default:
			return false
		}
	}
	return hasDigit || (hasUpper && hasLower)
}

func base64CharacterClassCount(value string) int {
	hasUpper := false
	hasLower := false
	hasDigit := false
	hasSymbol := false
	for _, value := range value {
		switch {
		case value >= 'A' && value <= 'Z':
			hasUpper = true
		case value >= 'a' && value <= 'z':
			hasLower = true
		case value >= '0' && value <= '9':
			hasDigit = true
		default:
			hasSymbol = true
		}
	}
	classes := 0
	for _, present := range []bool{hasUpper, hasLower, hasDigit, hasSymbol} {
		if present {
			classes++
		}
	}
	return classes
}

func hasBinaryMagic(decoded []byte) bool {
	for _, magic := range [][]byte{
		{0x1f, 0x8b}, {0x89, 0x50, 0x4e, 0x47}, {0x50, 0x4b, 0x03, 0x04}, {0x7f, 0x45, 0x4c, 0x46},
	} {
		if len(decoded) >= len(magic) && string(decoded[:len(magic)]) == string(magic) {
			return true
		}
	}
	return false
}

type submissionScanView struct {
	text     string
	original []int
}

func normalizedSubmissionScanView(content string) submissionScanView {
	originalRunes := []rune(content)
	var out []rune
	indices := make([]int, 0, len(originalRunes))
	for index, value := range originalRunes {
		normalized := strings.ToLower(norm.NFKC.String(string(value)))
		for _, normalizedRune := range normalized {
			out = append(out, normalizedRune)
			indices = append(indices, index)
		}
	}
	return submissionScanView{text: string(out), original: indices}
}

func dangerousSubmissionPattern(text string, original []int, pattern *regexp.Regexp, kind string) (SubmissionSecuritySignal, bool) {
	location := pattern.FindStringIndex(text)
	if location == nil {
		return SubmissionSecuritySignal{}, false
	}
	startRune := len([]rune(text[:location[0]]))
	endRune := len([]rune(text[:location[1]]))
	if startRune < 0 || endRune <= startRune || startRune >= len(original) {
		return SubmissionSecuritySignal{}, false
	}
	end := original[minSubmissionSecurityInt(endRune-1, len(original)-1)] + 1
	return SubmissionSecuritySignal{Kind: kind, Start: original[startRune], End: end}, true
}

func oneLayerSubmissionDecode(content string) (string, []int) {
	decoded := content
	if percentEscape.MatchString(decoded) {
		if value, err := decodePercentOnce(decoded); err == nil {
			decoded = value
		}
	}
	if htmlDecoded := html.UnescapeString(decoded); htmlDecoded != decoded {
		decoded = htmlDecoded
	}
	if hexEscape.MatchString(decoded) || jsonEscape.MatchString(decoded) {
		decoded = decodeEscapesOnce(decoded)
	}
	if decoded != content {
		// One decoding layer can change rune counts. Rejected writes deliberately
		// expose no spans, so returning a whole-input internal span is safer than
		// manufacturing an inaccurate one from a transformed representation.
		return decoded, nil
	}
	runes := []rune(content)
	mapping := make([]int, len(runes))
	for index := range runes {
		mapping[index] = index
	}
	return decoded, mapping
}

func decodePercentOnce(value string) (string, error) {
	return url.PathUnescape(value)
}

func decodeEscapesOnce(value string) string {
	value = hexEscape.ReplaceAllStringFunc(value, func(match string) string {
		parsed, err := strconv.ParseUint(match[2:], 16, 8)
		if err != nil {
			return match
		}
		return string(rune(parsed))
	})
	return jsonEscape.ReplaceAllStringFunc(value, func(match string) string {
		parsed, err := strconv.ParseUint(match[2:], 16, 16)
		if err != nil {
			return match
		}
		return string(rune(parsed))
	})
}

func mapDecodedSignal(signal SubmissionSecuritySignal, mapping []int, originalRuneCount int) SubmissionSecuritySignal {
	if len(mapping) == 0 || signal.Start < 0 || signal.End > len(mapping) {
		return SubmissionSecuritySignal{Kind: signal.Kind, Start: 0, End: originalRuneCount}
	}
	start := signal.Start
	end := signal.End
	if end <= start {
		return SubmissionSecuritySignal{Kind: signal.Kind, Start: 0, End: originalRuneCount}
	}
	return SubmissionSecuritySignal{Kind: signal.Kind, Start: mapping[start], End: mapping[end-1] + 1}
}

func minSubmissionSecurityInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}
