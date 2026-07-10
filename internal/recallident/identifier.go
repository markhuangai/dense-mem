package recallident

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

var (
	prRefPattern         = regexp.MustCompile(`(?i)\b(?:pr_number|pull_request_number|pull_request|issue_number|gh_issue|pr|pull request|pull|issue|gh)\s*[:#_-]?\s*([0-9]{1,8})\b`)
	hashPattern          = regexp.MustCompile(`(?i)\b[0-9a-f]{7,40}\b`)
	versionPattern       = regexp.MustCompile(`(?i)\bv[0-9]+(?:\.[0-9]+){1,4}(?:[-+][0-9a-z._-]+)?\b`)
	scopePattern         = regexp.MustCompile(`(?i)\b[a-z][a-z0-9_-]+:[a-z][a-z0-9_-]+\b`)
	rankingEntityPattern = regexp.MustCompile(`(?i)\b(?:account|bug|issue|job|project|queue|service|task|ticket)\s*[:#]?\s*([a-z][a-z0-9]*(?:-[a-z0-9]+)*-[0-9]+)\b`)
	candidatePattern     = regexp.MustCompile(`[A-Za-z0-9._@+#:-]*(?:[\\/][A-Za-z0-9._@+#:-]+)+|\.?[A-Za-z0-9][A-Za-z0-9._-]*\.[A-Za-z][A-Za-z0-9]{0,8}|[A-Za-z][A-Za-z0-9]*_[A-Za-z0-9_]+|[A-Za-z][A-Za-z0-9]*-[A-Za-z0-9][A-Za-z0-9-]*|[A-Za-z][A-Za-z0-9]*[A-Z][A-Za-z0-9]*`)
	surroundingCutset    = " \t\r\n\"'`“”‘’()[]{}<>,;!?|"
)

// Extract returns stable, normalized identifier tokens from code-like text.
func Extract(text string) []string {
	collector := newCollector()
	addPatternMatches(collector, strings.ToLower(text), prRefPattern, func(match []string) []string {
		if len(match) < 2 {
			return nil
		}
		return []string{"pr-" + match[1], "#" + match[1]}
	})
	addWholePatternMatches(collector, text, hashPattern)
	addWholePatternMatches(collector, text, versionPattern)
	addWholePatternMatches(collector, text, scopePattern)
	for _, raw := range candidatePattern.FindAllString(text, -1) {
		token := Normalize(raw)
		if looksLikeIdentifier(raw, token) {
			collector.add(token)
		}
	}
	return collector.items()
}

// BuildRecallText joins searchable fields and appends extracted identifiers.
func BuildRecallText(parts ...string) (string, []string) {
	joined := strings.Join(nonEmpty(parts), " ")
	tokens := Extract(joined)
	if len(tokens) == 0 {
		return joined, tokens
	}
	return strings.TrimSpace(joined + " identifiers " + strings.Join(tokens, " ")), tokens
}

// BuildFragmentRecallText includes provenance fields that clients may not place in content.
func BuildFragmentRecallText(content, source, idempotencyKey string, labels []string, metadata map[string]any, extra ...string) (string, []string) {
	parts := []string{content, source, idempotencyKey}
	parts = append(parts, labels...)
	parts = append(parts, scalarMetadataStrings(metadata)...)
	parts = append(parts, extra...)
	return BuildRecallText(parts...)
}

// MergeTokens combines token slices without reordering the first occurrence.
func MergeTokens(groups ...[]string) []string {
	collector := newCollector()
	for _, group := range groups {
		for _, token := range group {
			collector.add(token)
		}
	}
	return collector.items()
}

// Overlap reports how many query tokens appear in the candidate tokens.
func Overlap(queryTokens, candidateTokens []string) int {
	if len(queryTokens) == 0 || len(candidateTokens) == 0 {
		return 0
	}
	candidateSet := make(map[string]struct{}, len(candidateTokens))
	for _, token := range candidateTokens {
		candidateSet[token] = struct{}{}
	}
	count := 0
	for _, token := range queryTokens {
		if _, ok := candidateSet[token]; ok {
			count++
		}
	}
	return count
}

// OverlapText extracts identifiers from text fields and counts token overlap.
func OverlapText(queryTokens []string, parts ...string) int {
	return Overlap(queryTokens, Extract(strings.Join(nonEmpty(parts), " ")))
}

// HardGateAnchors returns the subset of identifiers precise enough to remove
// candidates that do not contain them.
func HardGateAnchors(tokens []string) []string {
	collector := newCollector()
	prRefs := map[string]struct{}{}
	for _, token := range tokens {
		if number, ok := prNumber(token); ok {
			prRefs[number] = struct{}{}
		}
	}
	for _, token := range tokens {
		token = Normalize(token)
		if number, ok := hashNumberRef(token); ok {
			if _, hasPRRef := prRefs[number]; hasPRRef {
				continue
			}
		}
		if isHardGateAnchor(token) {
			collector.add(token)
		}
	}
	return collector.items()
}

// RankingAnchors returns high-precision ordering anchors without treating every hyphenated scientific term as an identifier.
func RankingAnchors(text string) []string {
	tokens := Extract(text)
	collector := newCollector()
	for _, token := range HardGateAnchors(tokens) {
		collector.add(token)
	}
	for _, match := range rankingEntityPattern.FindAllStringSubmatch(text, -1) {
		if len(match) > 1 {
			collector.add(match[1])
		}
	}
	for _, token := range tokens {
		if isRankingOnlyFileAnchor(token) {
			collector.add(token)
		}
	}
	return collector.items()
}

// SatisfiesAnchors reports whether a candidate contains every supplied anchor.
func SatisfiesAnchors(anchors []string, parts ...string) bool {
	if len(anchors) == 0 {
		return true
	}
	candidateTokens := Extract(strings.Join(nonEmpty(parts), " "))
	candidateSet := make(map[string]struct{}, len(candidateTokens))
	for _, token := range candidateTokens {
		candidateSet[token] = struct{}{}
	}
	for _, anchor := range anchors {
		if _, ok := candidateSet[Normalize(anchor)]; !ok {
			return false
		}
	}
	return true
}

// StrongAnchors returns exact-match identifiers that should gate identifier-heavy recall.
func StrongAnchors(tokens []string) []string {
	collector := newCollector()
	prRefs := map[string]struct{}{}
	for _, token := range tokens {
		if number, ok := prNumber(token); ok {
			prRefs[number] = struct{}{}
		}
	}
	for _, token := range tokens {
		token = Normalize(token)
		if number, ok := hashNumberRef(token); ok {
			if _, hasPRRef := prRefs[number]; hasPRRef {
				continue
			}
		}
		if isStrongAnchor(token) {
			collector.add(token)
		}
	}
	return collector.items()
}

// SatisfiesStrongAnchors reports whether a candidate contains every strong query anchor.
func SatisfiesStrongAnchors(queryTokens []string, parts ...string) bool {
	return SatisfiesAnchors(StrongAnchors(queryTokens), parts...)
}

// Normalize canonicalizes one identifier token for comparison.
func Normalize(token string) string {
	token = strings.TrimSpace(token)
	token = strings.Trim(token, surroundingCutset)
	token = strings.TrimRight(token, ".")
	token = strings.ReplaceAll(token, "\\", "/")
	token = strings.ToLower(token)
	token = strings.TrimPrefix(token, "./")
	for strings.Contains(token, "//") {
		token = strings.ReplaceAll(token, "//", "/")
	}
	return token
}

func addPatternMatches(c *collector, text string, pattern *regexp.Regexp, f func([]string) []string) {
	for _, match := range pattern.FindAllStringSubmatch(text, -1) {
		for _, token := range f(match) {
			c.add(token)
		}
	}
}

func addWholePatternMatches(c *collector, text string, pattern *regexp.Regexp) {
	for _, match := range pattern.FindAllString(text, -1) {
		c.add(Normalize(match))
	}
}

func looksLikeIdentifier(raw, token string) bool {
	if token == "" || len(token) < 2 {
		return false
	}
	if strings.HasPrefix(token, "#") {
		return true
	}
	if strings.Contains(token, "/") || strings.Contains(token, "_") || strings.Contains(token, ":") {
		return true
	}
	if strings.HasPrefix(token, ".") || strings.Contains(token, ".") {
		return true
	}
	if strings.HasPrefix(token, "v") && hasDigit(token) {
		return true
	}
	if strings.Contains(token, "-") && hasDigit(token) {
		return true
	}
	return hasCamelBoundary(raw) && len(token) >= 5
}

func isStrongAnchor(token string) bool {
	if token == "" {
		return false
	}
	if _, ok := prNumber(token); ok {
		return true
	}
	if _, ok := hashNumberRef(token); ok {
		return true
	}
	if hashPattern.MatchString(token) || versionPattern.MatchString(token) {
		return true
	}
	if strings.Contains(token, "/") || strings.Contains(token, ":") || strings.Contains(token, "_") {
		return true
	}
	if strings.HasPrefix(token, ".") || strings.Contains(token, ".") {
		return true
	}
	return strings.Contains(token, "-") && hasDigit(token)
}

func isHardGateAnchor(token string) bool {
	if token == "" {
		return false
	}
	if _, ok := prNumber(token); ok {
		return true
	}
	if _, ok := hashNumberRef(token); ok {
		return true
	}
	if hashPattern.MatchString(token) || versionPattern.MatchString(token) || scopePattern.MatchString(token) {
		return true
	}
	if strings.Contains(token, "_") {
		return true
	}
	if isFileLikeAnchor(token) {
		return true
	}
	if strings.Contains(token, "/") {
		return isPathLikeAnchor(token)
	}
	return false
}

func isPathLikeAnchor(token string) bool {
	parts := strings.Split(token, "/")
	if len(parts) < 2 {
		return false
	}
	first := strings.TrimPrefix(parts[0], ".")
	if projectPathPrefixes[first] {
		return true
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		if strings.Contains(part, "_") || isFileLikeAnchor(part) {
			return true
		}
	}
	return false
}

func isFileLikeAnchor(token string) bool {
	token = strings.TrimPrefix(token, ".")
	idx := strings.LastIndex(token, ".")
	if idx < 0 || idx == len(token)-1 {
		return false
	}
	return fileLikeExtensions[token[idx+1:]]
}

func isRankingOnlyFileAnchor(token string) bool {
	token = strings.TrimPrefix(token, ".")
	idx := strings.LastIndex(token, ".")
	if idx < 0 || idx == len(token)-1 {
		return false
	}
	return rankingOnlyFileExtensions[token[idx+1:]]
}

var projectPathPrefixes = map[string]bool{
	"bugfix": true, "chore": true, "cmd": true, "codex": true, "docs": true,
	"exp": true, "feature": true, "fix": true, "github": true, "gitlab": true,
	"hotfix": true, "internal": true, "origin": true, "pkg": true, "refs": true,
	"release": true, "src": true, "test": true, "tests": true, "web": true,
}

var fileLikeExtensions = map[string]bool{
	"bash": true, "c": true, "cc": true, "cfg": true, "conf": true,
	"cpp": true, "cs": true, "css": true, "env": true, "go": true,
	"h": true, "hpp": true, "html": true, "java": true, "js": true,
	"json": true, "jsx": true, "kt": true, "md": true, "php": true,
	"py": true, "rb": true, "rs": true, "scss": true, "sh": true,
	"sql": true, "toml": true, "ts": true, "tsx": true, "xml": true,
	"yaml": true, "yml": true,
}

var rankingOnlyFileExtensions = map[string]bool{
	"db": true, "exe": true,
}

func prNumber(token string) (string, bool) {
	if !strings.HasPrefix(token, "pr-") {
		return "", false
	}
	number := strings.TrimPrefix(token, "pr-")
	if number == "" || !allDigits(number) {
		return "", false
	}
	return number, true
}

func hashNumberRef(token string) (string, bool) {
	if !strings.HasPrefix(token, "#") {
		return "", false
	}
	number := strings.TrimPrefix(token, "#")
	if number == "" || !allDigits(number) {
		return "", false
	}
	return number, true
}

func hasDigit(value string) bool {
	for _, r := range value {
		if unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

func allDigits(value string) bool {
	for _, r := range value {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return value != ""
}

func hasCamelBoundary(value string) bool {
	prevLower := false
	for _, r := range value {
		if unicode.IsUpper(r) && prevLower {
			return true
		}
		prevLower = unicode.IsLower(r)
	}
	return false
}

func nonEmpty(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func scalarMetadataStrings(metadata map[string]any) []string {
	if len(metadata) == 0 {
		return nil
	}
	keys := make([]string, 0, len(metadata))
	for key := range metadata {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(metadata)*2)
	for _, key := range keys {
		out = append(out, key)
		out = appendScalarValue(out, metadata[key])
	}
	return out
}

func appendScalarValue(out []string, value any) []string {
	switch v := value.(type) {
	case nil:
		return out
	case string:
		return append(out, v)
	case fmt.Stringer:
		return append(out, v.String())
	case bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		return append(out, fmt.Sprint(v))
	case []string:
		return append(out, v...)
	case []any:
		for _, item := range v {
			out = appendScalarValue(out, item)
		}
	default:
		if encoded, err := json.Marshal(v); err == nil {
			out = append(out, string(encoded))
		}
	}
	return out
}

type collector struct {
	seen  map[string]struct{}
	order []string
}

func newCollector() *collector {
	return &collector{seen: map[string]struct{}{}}
}

func (c *collector) add(token string) {
	token = Normalize(token)
	if token == "" {
		return
	}
	if _, ok := c.seen[token]; ok {
		return
	}
	c.seen[token] = struct{}{}
	c.order = append(c.order, token)
}

func (c *collector) items() []string {
	return append([]string(nil), c.order...)
}
