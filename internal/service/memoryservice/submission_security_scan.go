package memoryservice

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"html"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"github.com/markhuangai/dense-mem/internal/repository"
)

const (
	SubmissionSecurityErrorEncodedEvidence = "encoded_evidence_not_allowed"
	SubmissionSecurityErrorRejected        = "evidence_security_rejected"

	submissionSecurityMaxSignals           = 8
	submissionSecurityMaxBatchSignals      = 16
	submissionSecurityMaxEncodedCandidates = 8
	submissionSecurityMaxDecodedBytes      = 4 << 10
	submissionSecurityMinBase64TokenLength = 16
	submissionSecurityMinFoldedBase64Part  = 4
	submissionSecurityHiddenControlFlood   = 4
)

var (
	ErrEncodedEvidenceNotAllowed = &SubmissionSecurityError{Code: SubmissionSecurityErrorEncodedEvidence}
	ErrEvidenceSecurityRejected  = &SubmissionSecurityError{Code: SubmissionSecurityErrorRejected}

	base64TokenPattern = regexp.MustCompile(`[-A-Za-z0-9+/_=]{16,}`)
	base64PartPattern  = regexp.MustCompile(`[-A-Za-z0-9+/_=]{4,}`)
	dataURIBase64      = regexp.MustCompile(`(?i)\bdata:[^,[:space:]]{0,256};base64,`)
	pemEnvelope        = regexp.MustCompile(`(?i)-----begin[[:space:]]+[^\r\n-]{1,96}-----`)
	markupPattern      = regexp.MustCompile(`(?is)<!--|<\s*(?:script|iframe|object|embed|meta|svg)\b|\bon[a-z]{3,32}\s*=`)
	controlRolePattern = regexp.MustCompile(`(?im)(?:^|[\r\n])[[:space:]]*(?:system|developer)[[:space:]]*:|<\|[[:space:]]*(?:system|developer)[[:space:]]*\|>|<<[[:space:]]*(?:sys|system|developer)[[:space:]]*>>`)

	secretExtractionPattern    = regexp.MustCompile(`(?is)\b(?:reveal|show|send|dump|print|exfiltrate|return)\b.{0,100}\b(?:system[[:space:]_-]*prompt|hidden[[:space:]_-]*instructions?|environment[[:space:]_-]*variables?|env|api[[:space:]_-]*keys?|credentials?|secrets?|cookies?|tokens?|passwords?|passcodes?|private[[:space:]_-]*keys?|authorization[[:space:]_-]*headers?)\b|\b(?:please[[:space:]_-]+)?output\b[[:space:]_-]+(?:(?:all|the|your|any|raw|secret|hidden|system|environment|api|access|auth|session|credential|cookie|token)[[:space:]_-]+)*(?:system[[:space:]_-]*prompt|hidden[[:space:]_-]*instructions?|environment[[:space:]_-]*variables?|env|api[[:space:]_-]*keys?|credentials?|secrets?|cookies?|tokens?|passwords?|passcodes?|private[[:space:]_-]*keys?|authorization[[:space:]_-]*headers?)\b`)
	toolExfiltrationPattern    = regexp.MustCompile(`(?is)\b(?:use[[:space:]_-]*(?:your[[:space:]_-]*)?tools?|curl|wget|fetch|post|send|upload|exfiltrate|transmit|make[[:space:]_-]*(?:an[[:space:]_-]*)?(?:http[[:space:]_-]*|network[[:space:]_-]*)?request|call[[:space:]_-]*(?:an[[:space:]_-]*)?api)\b.{0,180}(?:https?://|webhook|endpoint|external|environment[[:space:]_-]*variables?|env|api[[:space:]_-]*keys?|credentials?|secrets?|cookies?|tokens?)`)
	toolDirectiveSuffixPattern = regexp.MustCompile(`(?is)(?:\b(?:please|kindly)|\b(?:can|could|would|will|shall|may)[[:space:]]+you|\byou[[:space:]]+(?:must|should|need(?:[[:space:]]+to)?|are[[:space:]]+to)|\b(?:i|we)[[:space:]]+(?:need|want|require)[[:space:]]+you(?:[[:space:]]+to)?|\b(?:try|attempt)[[:space:]]+to|\bassistant[,:]?)\s*$`)
	toolDirectiveStartPattern  = regexp.MustCompile(`(?is)(?:^|[\r\n.!?])[[:space:]>#*\-]*$`)
	identifierHexPattern       = regexp.MustCompile(`(?i)^(?:sha(?:1|224|256|384|512):)?(?:[0-9a-f]{32}|[0-9a-f]{40}|[0-9a-f]{56}|[0-9a-f]{64}|[0-9a-f]{96}|[0-9a-f]{128})$`)
	uuidPattern                = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	ulidPattern                = regexp.MustCompile(`^[0-7][0-9A-HJKMNP-TV-Z]{25}$`)
	overridePattern            = regexp.MustCompile(buildOverridePattern())
)

// SubmissionSecurityError contains a bounded code only. It is safe to expose
// at MCP and HTTP boundaries because it never retains submitted evidence.
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

// SubmissionSecuritySignal identifies a bounded rule and source range. It
// deliberately carries no evidence, decoded text, quote, or hash.
type SubmissionSecuritySignal struct {
	Kind     string
	RuleID   string
	Severity string
	Start    int
	End      int
	Encoded  bool
}

// SubmissionSecurityScan is the deterministic result for one evidence item.
type SubmissionSecurityScan struct {
	Signals          []SubmissionSecuritySignal
	SignalsTruncated bool
}

// SubmissionSecurityBatchSignal identifies the bounded request input that
// produced a deterministic safety signal.
type SubmissionSecurityBatchSignal struct {
	EvidenceIndex int
	Source        string
	SubmissionSecuritySignal
}

// SubmissionSecurityBatchScan is a batch result used to make one atomic
// admission decision before an ingest or lifecycle evidence write is created.
type SubmissionSecurityBatchScan struct {
	Items            []SubmissionSecurityScan
	Signals          []SubmissionSecurityBatchSignal
	EvidenceCount    int
	SignalsTruncated bool
}

type submissionSecurityInput struct {
	content       string
	source        string
	evidenceIndex int
}

const (
	submissionSecuritySourceEvidence = "evidence"
	submissionSecuritySourceProposal = "proposal"
)

func buildOverridePattern() string {
	word := func(value string) string {
		parts := make([]string, 0, len(value))
		for _, r := range value {
			parts = append(parts, regexp.QuoteMeta(string(r)))
		}
		return strings.Join(parts, `[[:space:]_./|:-]{0,3}`)
	}
	between := `[[:space:]_./|,:;:-]{1,64}`
	verb := `(?:` + strings.Join([]string{word("ignore"), word("disregard"), word("forget"), word("override")}, "|") + `)`
	first := `(?:` + strings.Join([]string{word("previous"), word("prior"), word("above"), word("earlier"), word("surrounding")}, "|") + `)`
	second := `(?:` + strings.Join([]string{word("instruction"), word("instructions"), word("rule"), word("rules"), word("prompt"), word("prompts"), word("context"), word("answer")}, "|") + `)`
	return `(?i)\b` + verb + `\b(?:` + between + `(?:all|the))*` + between + first + between + second + `\b`
}

// ScanSubmissionEvidence deterministically examines one evidence payload
// before it is staged, hashed, or sent to a provider. It never decodes more
// than one representation layer and never returns submitted content.
func ScanSubmissionEvidence(content string) (SubmissionSecurityScan, error) {
	signals := make([]SubmissionSecuritySignal, 0, submissionSecurityMaxSignals)
	signals = append(signals, encodedEvidenceSignals(identitySecurityView(content))...)
	signals = append(signals, dangerousSubmissionSignals(identitySecurityView(content), false)...)

	decoded := oneLayerSubmissionDecode(content)
	if decoded.text != content {
		signals = append(signals, encodedEvidenceSignals(decoded)...)
		signals = append(signals, dangerousSubmissionSignals(decoded, true)...)
	}

	signals, truncated := normalizeSubmissionSecuritySignals(signals)
	scan := SubmissionSecurityScan{Signals: signals, SignalsTruncated: truncated}
	if scan.rejectsEncodedEvidence() {
		return scan, ErrEncodedEvidenceNotAllowed
	}
	if len(scan.Signals) > 0 {
		return scan, ErrEvidenceSecurityRejected
	}
	return scan, nil
}

// ScanSubmissionBatch evaluates all evidence to produce a bounded audit
// record and then rejects the whole batch when any item is unsafe.
func ScanSubmissionBatch(contents []string) (SubmissionSecurityBatchScan, error) {
	inputs := make([]submissionSecurityInput, 0, len(contents))
	for index, content := range contents {
		inputs = append(inputs, submissionSecurityInput{
			content:       content,
			source:        submissionSecuritySourceEvidence,
			evidenceIndex: index,
		})
	}
	return scanSubmissionInputs(inputs)
}

func scanSubmissionWithProviderProposal(contents []string, proposal map[string]any) (SubmissionSecurityBatchScan, error) {
	inputs := make([]submissionSecurityInput, 0)
	for index, content := range contents {
		inputs = append(inputs, submissionSecurityInput{
			content:       content,
			source:        submissionSecuritySourceEvidence,
			evidenceIndex: index,
		})
	}
	if len(proposal) == 0 {
		return scanSubmissionInputs(inputs)
	}
	proposalInputs, err := submissionSecurityProposalInputs(proposal)
	if err != nil {
		return SubmissionSecurityBatchScan{Items: make([]SubmissionSecurityScan, len(inputs)), EvidenceCount: len(contents)}, ErrEvidenceSecurityRejected
	}
	inputs = append(inputs, proposalInputs...)
	return scanSubmissionInputs(inputs)
}

func submissionSecurityProposalInputs(proposal map[string]any) ([]submissionSecurityInput, error) {
	encoded, err := json.Marshal(proposal)
	if err != nil {
		return nil, err
	}
	var normalized any
	if err := json.Unmarshal(encoded, &normalized); err != nil {
		return nil, err
	}
	values := make([]string, 0)
	appendSubmissionSecurityProposalValues(normalized, &values)
	inputs := make([]submissionSecurityInput, 0, len(values))
	for _, value := range values {
		inputs = append(inputs, submissionSecurityInput{
			content:       value,
			source:        submissionSecuritySourceProposal,
			evidenceIndex: -1,
		})
	}
	return inputs, nil
}

func appendSubmissionSecurityProposalValues(value any, values *[]string) {
	switch typed := value.(type) {
	case string:
		*values = append(*values, typed)
	case []any:
		for _, item := range typed {
			appendSubmissionSecurityProposalValues(item, values)
		}
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			appendSubmissionSecurityProposalValues(typed[key], values)
		}
	}
}

func scanSubmissionInputs(inputs []submissionSecurityInput) (SubmissionSecurityBatchScan, error) {
	result := SubmissionSecurityBatchScan{Items: make([]SubmissionSecurityScan, len(inputs))}
	var rejection error
	for index, input := range inputs {
		scan, err := ScanSubmissionEvidence(input.content)
		result.Items[index] = scan
		if input.source == submissionSecuritySourceEvidence {
			result.EvidenceCount++
		}
		if scan.SignalsTruncated {
			result.SignalsTruncated = true
		}
		for _, signal := range scan.Signals {
			if len(result.Signals) >= submissionSecurityMaxBatchSignals {
				result.SignalsTruncated = true
				break
			}
			result.Signals = append(result.Signals, SubmissionSecurityBatchSignal{
				EvidenceIndex:            input.evidenceIndex,
				Source:                   input.source,
				SubmissionSecuritySignal: signal,
			})
		}
		if err != nil && rejection == nil {
			rejection = err
		}
		if errors.Is(err, ErrEncodedEvidenceNotAllowed) {
			rejection = ErrEncodedEvidenceNotAllowed
		}
	}
	return result, rejection
}

func (scan SubmissionSecurityScan) rejectsEncodedEvidence() bool {
	for _, signal := range scan.Signals {
		if signal.Encoded {
			return true
		}
	}
	return false
}

func submissionSecurityPassEvent() repository.SecurityEventDraft {
	return repository.SecurityEventDraft{
		EventKind: "deterministic_scan",
		Decision:  "pass",
		Reason:    "deterministic intake scan passed",
	}
}

func submissionSecurityQuarantineEvent(scan SubmissionSecurityScan) repository.SecurityEventDraft {
	return submissionSecurityQuarantineEventForSignals(scan.Signals, scan.SignalsTruncated, nil)
}

func submissionSecurityBatchQuarantineEvent(scan SubmissionSecurityBatchScan) repository.SecurityEventDraft {
	signals := make([]SubmissionSecuritySignal, 0, len(scan.Signals))
	sources := make([]string, 0, len(scan.Signals))
	for _, signal := range scan.Signals {
		signals = append(signals, signal.SubmissionSecuritySignal)
		sources = append(sources, signal.Source)
	}
	return submissionSecurityQuarantineEventForSignals(signals, scan.SignalsTruncated, sources)
}

func submissionSecurityQuarantineEventForSignals(
	scanSignals []SubmissionSecuritySignal,
	signalsTruncated bool,
	sources []string,
) repository.SecurityEventDraft {
	signals := make([]repository.SecuritySignalInput, 0, len(scanSignals))
	for index, signal := range scanSignals {
		metadata := map[string]any{"rule_id": signal.RuleID}
		if index < len(sources) && sources[index] != "" {
			metadata["source"] = sources[index]
		}
		signals = append(signals, repository.SecuritySignalInput{
			Kind:      signal.Kind,
			Severity:  signal.Severity,
			SpanStart: signal.Start,
			SpanEnd:   signal.End,
			Metadata:  metadata,
		})
	}
	return repository.SecurityEventDraft{
		EventKind: "deterministic_scan",
		Decision:  "quarantine",
		Reason:    "deterministic intake scan rejected evidence",
		Signals:   signals,
		Metadata: map[string]any{
			"signals_truncated": signalsTruncated,
		},
	}
}

type sourceSpan struct {
	start int
	end   int
}

type securityView struct {
	text  string
	spans []sourceSpan
}

func identitySecurityView(content string) securityView {
	runes := []rune(content)
	spans := make([]sourceSpan, len(runes))
	for index := range runes {
		spans[index] = sourceSpan{start: index, end: index + 1}
	}
	return securityView{text: content, spans: spans}
}

func normalizedSecurityView(view securityView) securityView {
	if view.text == "" {
		return view
	}
	var text strings.Builder
	spans := make([]sourceSpan, 0, len(view.spans))
	for index, value := range []rune(view.text) {
		normalized := strings.ToLower(norm.NFKC.String(string(value)))
		for _, normalizedRune := range normalized {
			if unicode.Is(unicode.Mn, normalizedRune) || hiddenControlRune(normalizedRune) {
				continue
			}
			text.WriteRune(normalizedRune)
			spans = append(spans, view.spans[index])
		}
	}
	return securityView{text: text.String(), spans: spans}
}

func oneLayerSubmissionDecode(content string) securityView {
	runes := []rune(content)
	var text strings.Builder
	spans := make([]sourceSpan, 0, len(runes))
	appendValue := func(value string, span sourceSpan) {
		for _, decoded := range value {
			text.WriteRune(decoded)
			spans = append(spans, span)
		}
	}
	for index := 0; index < len(runes); {
		if runes[index] == '%' {
			start := index
			bytes := make([]byte, 0, 8)
			for index+2 < len(runes) && runes[index] == '%' && isHexRune(runes[index+1]) && isHexRune(runes[index+2]) {
				value, _ := strconv.ParseUint(string(runes[index+1:index+3]), 16, 8)
				bytes = append(bytes, byte(value))
				index += 3
			}
			if len(bytes) > 0 && utf8.Valid(bytes) {
				appendValue(string(bytes), sourceSpan{start: start, end: index})
				continue
			}
			index = start
		}
		if runes[index] == '\\' && index+3 < len(runes) && runes[index+1] == 'x' && isHexRune(runes[index+2]) && isHexRune(runes[index+3]) {
			value, _ := strconv.ParseUint(string(runes[index+2:index+4]), 16, 8)
			appendValue(string(rune(value)), sourceSpan{start: index, end: index + 4})
			index += 4
			continue
		}
		if runes[index] == '\\' && index+5 < len(runes) && runes[index+1] == 'u' && allHexRunes(runes[index+2:index+6]) {
			value, _ := strconv.ParseUint(string(runes[index+2:index+6]), 16, 16)
			appendValue(string(rune(value)), sourceSpan{start: index, end: index + 6})
			index += 6
			continue
		}
		if runes[index] == '&' {
			if end := htmlEntityEnd(runes, index); end > index {
				encoded := string(runes[index:end])
				if decoded := html.UnescapeString(encoded); decoded != encoded {
					appendValue(decoded, sourceSpan{start: index, end: end})
					index = end
					continue
				}
			}
		}
		appendValue(string(runes[index]), sourceSpan{start: index, end: index + 1})
		index++
	}
	return securityView{text: text.String(), spans: spans}
}

func isHexRune(value rune) bool {
	return value >= '0' && value <= '9' || value >= 'a' && value <= 'f' || value >= 'A' && value <= 'F'
}

func allHexRunes(values []rune) bool {
	for _, value := range values {
		if !isHexRune(value) {
			return false
		}
	}
	return true
}

func htmlEntityEnd(runes []rune, start int) int {
	limit := start + 32
	if limit > len(runes) {
		limit = len(runes)
	}
	for index := start + 1; index < limit; index++ {
		if runes[index] == ';' {
			return index + 1
		}
		if unicode.IsSpace(runes[index]) || runes[index] == '&' {
			break
		}
	}
	return start
}

func encodedEvidenceSignals(view securityView) []SubmissionSecuritySignal {
	if view.text == "" {
		return nil
	}
	signals := make([]SubmissionSecuritySignal, 0, 2)
	addMatch := func(pattern *regexp.Regexp, ruleID string) {
		if location := pattern.FindStringIndex(view.text); location != nil {
			if signal, ok := signalForLocation(view, location, "obfuscated_instruction", ruleID, "critical", true); ok {
				signals = append(signals, signal)
			}
		}
	}
	addMatch(dataURIBase64, "data_uri_base64")
	addMatch(pemEnvelope, "pem_envelope")
	if signal, ok := jwtSignal(view); ok {
		signals = append(signals, signal)
	}

	candidateCount := 0
	for _, location := range base64TokenPattern.FindAllStringIndex(view.text, -1) {
		candidate := view.text[location[0]:location[1]]
		if commonIdentifierCandidate(candidate) {
			continue
		}
		if looksLikeNaturalEvidenceToken(candidate) {
			continue
		}
		candidateCount++
		if candidateCount > submissionSecurityMaxEncodedCandidates {
			if signal, ok := signalForLocation(view, location, "obfuscated_instruction", "encoded_candidate_budget", "critical", true); ok {
				signals = append(signals, signal)
			}
			break
		}
		if isBase64EncodedShape(candidate) && base64DecodedByteLength(candidate) > submissionSecurityMaxDecodedBytes {
			if signal, ok := signalForLocation(view, location, "obfuscated_instruction", "oversized_base64", "critical", true); ok {
				signals = append(signals, signal)
			}
			continue
		}
		if encodedCandidateRejected(candidate) {
			if signal, ok := signalForLocation(view, location, "obfuscated_instruction", "base64_candidate", "critical", true); ok {
				signals = append(signals, signal)
			}
		}
	}
	if location := foldedBase64Location(view.text); location != nil {
		if signal, ok := signalForLocation(view, location, "obfuscated_instruction", "folded_base64", "critical", true); ok {
			signals = append(signals, signal)
		}
	}
	return signals
}

func jwtSignal(view securityView) (SubmissionSecuritySignal, bool) {
	for _, token := range strings.Fields(view.text) {
		trimmed := strings.Trim(token, "`'\"()[]{}<>,;:")
		parts := strings.Split(trimmed, ".")
		if len(parts) != 3 {
			continue
		}
		if len(parts[0]) < 8 || len(parts[1]) < 8 || len(parts[2]) < 8 ||
			!isBase64URLPart(parts[0]) || !isBase64URLPart(parts[1]) || !isBase64URLPart(parts[2]) ||
			!isJWTJSONObject(parts[0], true) || !isJWTJSONObject(parts[1], false) {
			continue
		}
		if index := strings.Index(view.text, trimmed); index >= 0 {
			return signalForLocation(view, []int{index, index + len(trimmed)}, "obfuscated_instruction", "jwt", "critical", true)
		}
	}
	return SubmissionSecuritySignal{}, false
}

func isJWTJSONObject(part string, requireAlgorithm bool) bool {
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(part, "="))
	if err != nil || len(decoded) == 0 || len(decoded) > submissionSecurityMaxDecodedBytes {
		return false
	}
	var value map[string]any
	if err := json.Unmarshal(decoded, &value); err != nil || len(value) == 0 {
		return false
	}
	if !requireAlgorithm {
		return true
	}
	algorithm, ok := value["alg"].(string)
	return ok && strings.TrimSpace(algorithm) != ""
}

func isBase64URLPart(value string) bool {
	_, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(value, "="))
	return err == nil
}

func encodedCandidateRejected(encoded string) bool {
	if opaqueIdentifierCandidate(encoded) {
		decoded, ok := decodeBase64(encoded)
		if ok && len(decoded) > 0 && len(decoded) <= submissionSecurityMaxDecodedBytes {
			// Opaque provider identifiers can share the Base64URL alphabet with
			// encoded instructions. Inspect the bounded decoded content before
			// allowing the identifier exemption.
			if len(dangerousSubmissionSignals(identitySecurityView(string(decoded)), true)) > 0 {
				return true
			}
		}
		return false
	}
	if !isBase64EncodedShape(encoded) || base64DecodedByteLength(encoded) > submissionSecurityMaxDecodedBytes {
		return isBase64EncodedShape(encoded)
	}
	decoded, ok := decodeBase64(encoded)
	if !ok || len(decoded) == 0 || len(decoded) > submissionSecurityMaxDecodedBytes {
		return false
	}
	if strings.ContainsAny(encoded, "=+/_-") || hasBinaryMagic(decoded) {
		return true
	}
	if printablePercentage(decoded) >= 85 {
		return true
	}
	// High entropy by itself is not evidence of an encoded payload: opaque
	// order references and provider IDs often have the same alphabet. Require
	// a meaningful printable decode before applying the entropy signal.
	return len(encoded) >= 24 && shannonEntropy(encoded) >= 3.8 && printablePercentage(decoded) >= 60
}

func opaqueIdentifierCandidate(value string) bool {
	if len(value) < submissionSecurityMinBase64TokenLength || strings.ContainsAny(value, "=+/") {
		return false
	}
	parts := strings.FieldsFunc(value, func(r rune) bool { return r == '-' || r == '_' })
	if len(parts) < 2 || strings.HasPrefix(value, "-") || strings.HasPrefix(value, "_") || strings.HasSuffix(value, "-") || strings.HasSuffix(value, "_") {
		return false
	}
	hasDigit := false
	for _, part := range parts {
		if part == "" {
			return false
		}
		for _, r := range part {
			switch {
			case r >= '0' && r <= '9':
				hasDigit = true
			case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z':
			default:
				return false
			}
		}
	}
	return hasDigit
}

func decodeBase64(encoded string) ([]byte, bool) {
	trimmed := strings.TrimRight(encoded, "=")
	for _, decoder := range []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	} {
		value, err := decoder.DecodeString(encoded)
		if err == nil {
			return value, true
		}
		value, err = decoder.DecodeString(trimmed)
		if err == nil {
			return value, true
		}
	}
	return nil, false
}

func foldedBase64Location(content string) []int {
	matches := base64PartPattern.FindAllStringIndex(content, -1)
	if len(matches) < 2 {
		return nil
	}
	parts := make([]string, 0, len(matches))
	start, end := -1, -1
	flush := func() []int {
		if len(parts) < 2 {
			parts = parts[:0]
			start, end = -1, -1
			return nil
		}
		joined := strings.Join(parts, "")
		parts = parts[:0]
		location := []int{start, end}
		start, end = -1, -1
		if encodedCandidateRejected(joined) {
			return location
		}
		return nil
	}
	previousEnd := -1
	for _, match := range matches {
		if previousEnd >= 0 && !foldedBase64Delimiter(content[previousEnd:match[0]]) {
			if location := flush(); location != nil {
				return location
			}
		}
		part := content[match[0]:match[1]]
		if len(part) >= submissionSecurityMinFoldedBase64Part && !commonIdentifierCandidate(part) && !looksLikeNaturalEvidenceToken(part) {
			if len(parts) == 0 {
				start = match[0]
			}
			parts = append(parts, part)
			end = match[1]
		} else if location := flush(); location != nil {
			return location
		}
		previousEnd = match[1]
	}
	return flush()
}

func foldedBase64Delimiter(value string) bool {
	if strings.TrimSpace(value) == "" {
		return true
	}
	for _, r := range value {
		if unicode.IsSpace(r) || strings.ContainsRune("`'\"(),;:[]{}<>", r) {
			continue
		}
		return false
	}
	return true
}

func commonIdentifierCandidate(value string) bool {
	return uuidPattern.MatchString(value) || ulidPattern.MatchString(value) || identifierHexPattern.MatchString(value)
}

func isBase64EncodedShape(encoded string) bool {
	if len(encoded) < submissionSecurityMinBase64TokenLength || !base64TokenPattern.MatchString(encoded) || !isBase64TokenPart(encoded) {
		return false
	}
	trimmed := strings.TrimRight(encoded, "=")
	paddingLength := len(encoded) - len(trimmed)
	return trimmed != "" && paddingLength <= 2 && !strings.Contains(trimmed, "=") && len(trimmed)%4 != 1
}

func isBase64TokenPart(value string) bool {
	for _, value := range value {
		if value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z' || value >= '0' && value <= '9' || strings.ContainsRune("+/_=-", value) {
			continue
		}
		return false
	}
	return true
}

func base64DecodedByteLength(encoded string) int {
	return len(strings.TrimRight(encoded, "=")) * 3 / 4
}

func printablePercentage(decoded []byte) int {
	if len(decoded) == 0 {
		return 0
	}
	printable := 0
	for _, value := range decoded {
		if value == '\n' || value == '\r' || value == '\t' || value >= 0x20 && value <= 0x7e {
			printable++
		}
	}
	return printable * 100 / len(decoded)
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

func base64CharacterClassCount(value string) int {
	hasUpper, hasLower, hasDigit, hasSymbol := false, false, false, false
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
	count := 0
	for _, exists := range []bool{hasUpper, hasLower, hasDigit, hasSymbol} {
		if exists {
			count++
		}
	}
	return count
}

func shannonEntropy(value string) float64 {
	if value == "" {
		return 0
	}
	counts := make(map[rune]int)
	for _, r := range value {
		counts[r]++
	}
	length := float64(len([]rune(value)))
	entropy := 0.0
	for _, count := range counts {
		probability := float64(count) / length
		entropy -= probability * math.Log2(probability)
	}
	return entropy
}

func looksLikeNaturalEvidenceToken(value string) bool {
	parts := strings.FieldsFunc(value, func(r rune) bool { return r == '/' || r == '-' || r == '_' })
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
		switch {
		case value >= 'a' && value <= 'z':
			if lowerPrefixUpperSuffix {
				return false
			}
			hasLower = true
		case index == 0 && value >= 'A' && value <= 'Z':
		case index > 0 && hasLower && value >= 'A' && value <= 'Z':
			lowerPrefixUpperSuffix = true
		default:
			return false
		}
	}
	return true
}

func looksLikeTechnicalEvidenceToken(value string) bool {
	if value == "" || len(value) > 16 {
		return false
	}
	hasUpper, hasLower, hasDigit := false, false, false
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
	return hasDigit || hasUpper && hasLower
}

func dangerousSubmissionSignals(view securityView, decoded bool) []SubmissionSecuritySignal {
	if view.text == "" {
		return nil
	}
	signals := hiddenControlFloodSignals(view, decoded)
	normalized := normalizedSecurityView(view)
	for _, pattern := range []struct {
		pattern  *regexp.Regexp
		kind     string
		rule     string
		severity string
	}{
		{markupPattern, "hidden_control_markup", "control_markup", "high"},
		{controlRolePattern, "role_control_spoofing", "control_role", "critical"},
		{overridePattern, "instruction_override", "instruction_override", "critical"},
		{secretExtractionPattern, "prompt_secret_extraction", "prompt_secret_extraction", "critical"},
		{toolExfiltrationPattern, "tool_exfiltration", "tool_exfiltration", "critical"},
	} {
		for _, location := range pattern.pattern.FindAllStringIndex(normalized.text, -1) {
			if pattern.rule == "tool_exfiltration" && !toolExfiltrationIsDirective(normalized.text, location[0]) {
				continue
			}
			kind := pattern.kind
			if decoded && kind == "instruction_override" {
				kind = "obfuscated_instruction"
			}
			if signal, ok := signalForLocation(normalized, location, kind, ruleID(decoded, pattern.rule), pattern.severity, false); ok {
				signals = append(signals, signal)
			}
		}
	}
	return signals
}

func hiddenControlFloodSignals(view securityView, decoded bool) []SubmissionSecuritySignal {
	runes := []rune(view.text)
	start, count := -1, 0
	for index, value := range runes {
		if hiddenControlRune(value) {
			if start < 0 {
				start = index
			}
			count++
			continue
		}
		if signal, ok := hiddenControlFloodSignal(view, start, index, count, decoded); ok {
			return []SubmissionSecuritySignal{signal}
		}
		start, count = -1, 0
	}
	if signal, ok := hiddenControlFloodSignal(view, start, len(runes), count, decoded); ok {
		return []SubmissionSecuritySignal{signal}
	}
	return nil
}

func hiddenControlFloodSignal(view securityView, start, end, count int, decoded bool) (SubmissionSecuritySignal, bool) {
	if start < 0 || count < submissionSecurityHiddenControlFlood || end <= start || end > len(view.spans) {
		return SubmissionSecuritySignal{}, false
	}
	return signalForSourceSpan(
		view,
		sourceSpan{start: view.spans[start].start, end: view.spans[end-1].end},
		"hidden_control_markup",
		ruleID(decoded, "hidden_unicode_control_flood"),
		"critical",
		false,
	)
}

func toolExfiltrationIsDirective(text string, start int) bool {
	if start < 0 || start > len(text) {
		return false
	}
	prefix := strings.TrimRightFunc(text[:start], func(value rune) bool {
		return unicode.IsSpace(value) || strings.ContainsRune("\\\"'`([{<", value)
	})
	if toolDirectiveStartPattern.MatchString(prefix) || toolDirectiveSuffixPattern.MatchString(prefix) {
		return true
	}
	return false
}

func hiddenControlRune(value rune) bool {
	return value == '\u200b' || value == '\u200c' || value == '\u200d' || value == '\u200e' || value == '\u200f' ||
		value == '\u2060' || value >= '\u202a' && value <= '\u202e' || value >= '\u2066' && value <= '\u2069' ||
		unicode.IsControl(value) && value != '\n' && value != '\r' && value != '\t'
}

func ruleID(decoded bool, base string) string {
	if decoded {
		return "decoded_" + base
	}
	return base
}

func signalForLocation(view securityView, location []int, kind, ruleID, severity string, encoded bool) (SubmissionSecuritySignal, bool) {
	startRune := utf8.RuneCountInString(view.text[:location[0]])
	endRune := utf8.RuneCountInString(view.text[:location[1]])
	if startRune < 0 || endRune <= startRune || startRune >= len(view.spans) {
		return SubmissionSecuritySignal{}, false
	}
	if endRune > len(view.spans) {
		endRune = len(view.spans)
	}
	return signalForSourceSpan(view, sourceSpan{start: view.spans[startRune].start, end: view.spans[endRune-1].end}, kind, ruleID, severity, encoded)
}

func signalForSourceSpan(view securityView, span sourceSpan, kind, ruleID, severity string, encoded bool) (SubmissionSecuritySignal, bool) {
	if len(view.spans) == 0 {
		return SubmissionSecuritySignal{}, false
	}
	if span.start < 0 {
		span.start = 0
	}
	if span.end > view.spans[len(view.spans)-1].end {
		span.end = view.spans[len(view.spans)-1].end
	}
	if span.end <= span.start {
		return SubmissionSecuritySignal{}, false
	}
	return SubmissionSecuritySignal{Kind: kind, RuleID: ruleID, Severity: severity, Start: span.start, End: span.end, Encoded: encoded}, true
}

func normalizeSubmissionSecuritySignals(signals []SubmissionSecuritySignal) ([]SubmissionSecuritySignal, bool) {
	if len(signals) == 0 {
		return []SubmissionSecuritySignal{}, false
	}
	sort.Slice(signals, func(left, right int) bool {
		if signals[left].Start != signals[right].Start {
			return signals[left].Start < signals[right].Start
		}
		if signals[left].End != signals[right].End {
			return signals[left].End < signals[right].End
		}
		if signals[left].Kind != signals[right].Kind {
			return signals[left].Kind < signals[right].Kind
		}
		return signals[left].RuleID < signals[right].RuleID
	})
	out := make([]SubmissionSecuritySignal, 0, minInt(len(signals), submissionSecurityMaxSignals))
	seen := make(map[string]struct{}, len(signals))
	truncated := false
	for _, signal := range signals {
		if signal.End <= signal.Start || strings.TrimSpace(signal.Kind) == "" || strings.TrimSpace(signal.RuleID) == "" {
			continue
		}
		key := strconv.Itoa(signal.Start) + ":" + strconv.Itoa(signal.End) + ":" + signal.Kind + ":" + signal.RuleID
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		if len(out) >= submissionSecurityMaxSignals {
			truncated = true
			continue
		}
		out = append(out, signal)
	}
	return out, truncated
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}
