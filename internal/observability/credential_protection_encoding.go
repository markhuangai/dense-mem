package observability

import (
	"net/url"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

type credentialDecodedByte struct {
	value byte
	end   int
}

func decodedCredentialPrefixDetailed(text, variant string, allowPercentEncoding bool) (int, bool, bool) {
	return matchCredentialLayersStreaming(text, variant, allowPercentEncoding, true, false)
}

func reverseDecodedCredentialPrefixDetailed(text, variant string) (int, bool, bool) {
	return matchCredentialLayersStreaming(text, variant, true, true, true)
}

const maxCredentialDecodeLayers = 4

const credentialStreamMaxPasses = maxCredentialDecodeLayers*2 + 2

type credentialStreamKind uint8

const (
	credentialStreamRaw credentialStreamKind = iota
	credentialStreamPercent
	credentialStreamEscapes
)

type credentialDecodeStream struct {
	kind       credentialStreamKind
	text       string
	index      int
	source     *credentialDecodeStream
	pending    [12]credentialDecodedByte
	pendingLen int
	unread     [16]credentialDecodedByte
	unreadLen  int
	changed    *bool
}

func (stream *credentialDecodeStream) next() (credentialDecodedByte, bool) {
	if stream.pendingLen > 0 {
		value := stream.pending[0]
		copy(stream.pending[:stream.pendingLen-1], stream.pending[1:stream.pendingLen])
		stream.pendingLen--
		return value, true
	}
	if stream.kind == credentialStreamRaw {
		if stream.index >= len(stream.text) {
			return credentialDecodedByte{}, false
		}
		value := credentialDecodedByte{value: stream.text[stream.index], end: stream.index + 1}
		stream.index++
		return value, true
	}

	first, ok := stream.readSource()
	if !ok {
		return credentialDecodedByte{}, false
	}
	if stream.kind == credentialStreamPercent {
		if first.value == '%' {
			second, secondOK := stream.readSource()
			if !secondOK {
				return first, true
			}
			third, thirdOK := stream.readSource()
			if !thirdOK {
				stream.unreadSource([]credentialDecodedByte{second})
				return first, true
			}
			if isHexDigit(second.value) && isHexDigit(third.value) {
				stream.markChanged()
				return credentialDecodedByte{
					value: hexByte(second.value, third.value),
					end:   third.end,
				}, true
			}
			stream.unreadSource([]credentialDecodedByte{second, third})
		}
		if first.value == '+' {
			stream.markChanged()
			return credentialDecodedByte{value: ' ', end: first.end}, true
		}
		return first, true
	}

	if first.value != '\\' {
		return first, true
	}
	var encodedInput [12]byte
	var ends [12]int
	encodedInput[0] = '\\'
	ends[0] = first.end
	length := 1
	for length < len(encodedInput) {
		value, valueOK := stream.readSource()
		if !valueOK {
			break
		}
		encodedInput[length] = value.value
		ends[length] = value.end
		length++
	}
	encoded, encodedSize, consumed, decodedOK := decodeCredentialCandidateEscape(encodedInput[:length])
	if !decodedOK {
		var unread [11]credentialDecodedByte
		for index := 1; index < length; index++ {
			unread[index-1] = credentialDecodedByte{
				value: encodedInput[index],
				end:   ends[index],
			}
		}
		stream.unreadSource(unread[:length-1])
		return first, true
	}
	stream.markChanged()
	end := ends[consumed-1]
	stream.pendingLen = 0
	for index := 0; index < encodedSize; index++ {
		stream.pending[stream.pendingLen] = credentialDecodedByte{value: encoded[index], end: end}
		stream.pendingLen++
	}
	var unread [11]credentialDecodedByte
	for index := consumed; index < length; index++ {
		unread[index-consumed] = credentialDecodedByte{
			value: encodedInput[index],
			end:   ends[index],
		}
	}
	stream.unreadSource(unread[:length-consumed])
	return stream.next()
}

func (stream *credentialDecodeStream) readSource() (credentialDecodedByte, bool) {
	if stream.unreadLen > 0 {
		value := stream.unread[0]
		copy(stream.unread[:stream.unreadLen-1], stream.unread[1:stream.unreadLen])
		stream.unreadLen--
		return value, true
	}
	return stream.source.next()
}

func (stream *credentialDecodeStream) unreadSource(values []credentialDecodedByte) {
	if len(values) == 0 {
		return
	}
	if stream.unreadLen != 0 || len(values) > len(stream.unread) {
		panic("credential decoder unread buffer exhausted")
	}
	copy(stream.unread[:], values)
	stream.unreadLen = len(values)
}

func (stream *credentialDecodeStream) markChanged() {
	if stream.changed != nil {
		*stream.changed = true
	}
}

func credentialMatchStreamPrefix(text, variant string, kinds []credentialStreamKind) (int, bool, bool) {
	var streams [credentialStreamMaxPasses + 1]credentialDecodeStream
	var changed [credentialStreamMaxPasses + 1]bool
	streams[0] = credentialDecodeStream{kind: credentialStreamRaw, text: text}
	for index, kind := range kinds {
		streams[index+1] = credentialDecodeStream{
			kind:    kind,
			source:  &streams[index],
			changed: &changed[index+1],
		}
	}
	stream := &streams[len(kinds)]
	consumed := 0
	for index := 0; index < len(variant); index++ {
		value, ok := stream.next()
		if !ok || value.value != variant[index] {
			return 0, false, len(kinds) > 0 && changed[len(kinds)]
		}
		consumed = value.end
	}
	return consumed, true, len(kinds) > 0 && changed[len(kinds)]
}

func matchCredentialLayersStreaming(text, variant string, allowPercentEncoding, allowUnicodeEncoding, percentFirst bool) (int, bool, bool) {
	if consumed, ok, _ := credentialMatchStreamPrefix(text, variant, nil); ok {
		return consumed, true, false
	}
	var roundKinds [2]credentialStreamKind
	perRound := 0
	if percentFirst {
		if allowPercentEncoding {
			roundKinds[perRound] = credentialStreamPercent
			perRound++
		}
		if allowUnicodeEncoding {
			roundKinds[perRound] = credentialStreamEscapes
			perRound++
		}
	} else {
		if allowUnicodeEncoding {
			roundKinds[perRound] = credentialStreamEscapes
			perRound++
		}
		if allowPercentEncoding {
			roundKinds[perRound] = credentialStreamPercent
			perRound++
		}
	}
	if perRound == 0 {
		return 0, false, false
	}

	var kinds [credentialStreamMaxPasses]credentialStreamKind
	depth := 0
	for round := 0; round < maxCredentialDecodeLayers; round++ {
		roundChanged := false
		for index := 0; index < perRound; index++ {
			kinds[depth] = roundKinds[index]
			depth++
			consumed, ok, changed := credentialMatchStreamPrefix(text, variant, kinds[:depth])
			if ok {
				return consumed, true, false
			}
			roundChanged = roundChanged || changed
		}
		if !roundChanged {
			return 0, false, false
		}
	}
	for index := 0; index < perRound; index++ {
		kinds[depth] = roundKinds[index]
		depth++
		_, _, changed := credentialMatchStreamPrefix(text, variant, kinds[:depth])
		if changed {
			return 0, false, true
		}
	}
	return 0, false, false
}

const credentialLiteralCandidateLimit = 64

func credentialLiteralCandidate(text, variant string) bool {
	limit := len(variant)
	if limit > credentialLiteralCandidateLimit {
		limit = credentialLiteralCandidateLimit
	}
	if limit > len(text) {
		limit = len(text)
	}
	for index := 0; index < limit; index++ {
		switch text[index] {
		case '%', '\\', '+':
			if !credentialEncodingPrefixIsValid(text[index:]) {
				return false
			}
			if text[index] == '+' && !strings.Contains(variant, " ") {
				return false
			}
			return true
		}
		if text[index] != variant[index] {
			return false
		}
	}
	return true
}

func credentialDecodedLiteralCandidate(text, variant string, allowPercentEncoding, allowUnicodeEncoding bool) bool {
	limit := len(variant)
	if limit > credentialCandidateBufferLimit {
		limit = credentialCandidateBufferLimit
	}
	if limit == 0 {
		return true
	}
	if credentialDecodedLiteralCandidateOrder(text, variant, limit, allowPercentEncoding, allowUnicodeEncoding, false) {
		return true
	}
	return allowPercentEncoding && allowUnicodeEncoding && credentialDecodedLiteralCandidateOrder(text, variant, limit, true, true, true)
}

const credentialCandidateBufferLimit = credentialLiteralCandidateLimit * 10

func credentialMatchWorkLimit(maxBytes int) int {
	const workPerByte = credentialLiteralCandidateLimit * 64
	const maxMatchWork = 8 << 20
	maxInt := int(^uint(0) >> 1)
	if maxBytes > maxInt/workPerByte {
		return maxMatchWork
	}
	limit := maxBytes * workPerByte
	if limit > maxMatchWork {
		return maxMatchWork
	}
	return limit
}

func credentialMatchWorkEstimate(text string, variants []credentialVariant) int {
	work := 0
	maxInt := int(^uint(0) >> 1)
	for _, variant := range variants {
		if !variant.allowPercentEncoding && !variant.allowUnicodeEncoding {
			continue
		}
		if !credentialLiteralCandidate(text, variant.text) {
			continue
		}
		if !credentialDecodedLiteralCandidate(text, variant.text, variant.allowPercentEncoding, variant.allowUnicodeEncoding) {
			continue
		}
		if len(variant.text) > maxInt-work {
			return maxInt
		}
		work += len(variant.text)
	}
	return work
}

type credentialCandidateBuffer struct {
	bytes     [credentialCandidateBufferLimit]byte
	length    int
	truncated bool
}

type credentialCandidatePrefixStatus uint8

const (
	credentialCandidateNoMatch credentialCandidatePrefixStatus = iota
	credentialCandidateMayChange
	credentialCandidateMatch
)

func credentialDecodedLiteralCandidateOrder(text, variant string, limit int, allowPercentEncoding, allowUnicodeEncoding, percentFirst bool) bool {
	current := credentialCandidateBuffer{length: len(text)}
	if current.length > len(current.bytes) {
		current.length = len(current.bytes)
		current.truncated = true
	}
	copy(current.bytes[:current.length], text[:current.length])
	status := credentialCandidatePrefixStatusFor(current, variant, limit, allowPercentEncoding, allowUnicodeEncoding)
	if status == credentialCandidateMatch {
		return true
	}
	if status == credentialCandidateNoMatch {
		return false
	}
	for layer := 0; layer < maxCredentialDecodeLayers; layer++ {
		roundChanged := false
		before := current
		if percentFirst && allowPercentEncoding {
			previousStatus := status
			current = decodeCredentialCandidatePercent(current)
			roundChanged = roundChanged || !credentialCandidateBuffersEqual(before, current)
			status = credentialCandidatePrefixStatusFor(current, variant, limit, allowPercentEncoding, allowUnicodeEncoding)
			if status == credentialCandidateMatch {
				return true
			}
			if status == credentialCandidateNoMatch && !allowUnicodeEncoding {
				return false
			}
			if status == credentialCandidateNoMatch && current.truncated && previousStatus == credentialCandidateMayChange && !credentialCandidateBuffersEqual(before, current) {
				return true
			}
		}
		if allowUnicodeEncoding {
			before = current
			current = decodeCredentialCandidateEscapes(current)
			roundChanged = roundChanged || !credentialCandidateBuffersEqual(before, current)
			status = credentialCandidatePrefixStatusFor(current, variant, limit, allowPercentEncoding, allowUnicodeEncoding)
			if status == credentialCandidateMatch {
				return true
			}
			if status == credentialCandidateNoMatch && (!allowPercentEncoding || percentFirst) {
				return false
			}
		}
		if !percentFirst && allowPercentEncoding {
			before = current
			previousStatus := status
			current = decodeCredentialCandidatePercent(current)
			roundChanged = roundChanged || !credentialCandidateBuffersEqual(before, current)
			status = credentialCandidatePrefixStatusFor(current, variant, limit, allowPercentEncoding, allowUnicodeEncoding)
			if status == credentialCandidateMatch {
				return true
			}
			if status == credentialCandidateNoMatch && !current.truncated {
				return false
			}
			if status == credentialCandidateNoMatch && current.truncated && previousStatus == credentialCandidateMayChange && !credentialCandidateBuffersEqual(before, current) {
				return true
			}
		}
		if !roundChanged && !current.truncated {
			return false
		}
		if layer == maxCredentialDecodeLayers-1 {
			return status != credentialCandidateNoMatch && (roundChanged || current.truncated)
		}
	}
	return current.truncated
}

func credentialCandidatePrefixStatusFor(candidate credentialCandidateBuffer, variant string, limit int, allowPercentEncoding, allowUnicodeEncoding bool) credentialCandidatePrefixStatus {
	prefixMayChange := false
	for index := 0; index < candidate.length && index < limit; index++ {
		prefixMayChange = prefixMayChange || credentialDecodedByteMayChange(candidate.bytes[index], allowPercentEncoding, allowUnicodeEncoding)
		if candidate.bytes[index] != variant[index] {
			if prefixMayChange {
				return credentialCandidateMayChange
			}
			return credentialCandidateNoMatch
		}
	}
	if candidate.length < limit {
		if candidate.truncated {
			return credentialCandidateMayChange
		}
		return credentialCandidateNoMatch
	}
	return credentialCandidateMatch
}

func credentialCandidateBuffersEqual(left, right credentialCandidateBuffer) bool {
	if left.length != right.length || left.truncated != right.truncated {
		return false
	}
	for index := 0; index < left.length; index++ {
		if left.bytes[index] != right.bytes[index] {
			return false
		}
	}
	return true
}

func credentialCandidateAppend(candidate *credentialCandidateBuffer, value byte) bool {
	if candidate.length >= len(candidate.bytes) {
		candidate.truncated = true
		return false
	}
	candidate.bytes[candidate.length] = value
	candidate.length++
	return true
}

func decodeCredentialCandidatePercent(input credentialCandidateBuffer) credentialCandidateBuffer {
	var decoded credentialCandidateBuffer
	for index := 0; index < input.length; {
		if input.bytes[index] == '%' && index+2 < input.length && isHexDigit(input.bytes[index+1]) && isHexDigit(input.bytes[index+2]) {
			if !credentialCandidateAppend(&decoded, hexByte(input.bytes[index+1], input.bytes[index+2])) {
				break
			}
			index += 3
			continue
		}
		if input.bytes[index] == '+' {
			if !credentialCandidateAppend(&decoded, ' ') {
				break
			}
			index++
			continue
		}
		if !credentialCandidateAppend(&decoded, input.bytes[index]) {
			break
		}
		index++
	}
	decoded.truncated = decoded.truncated || input.truncated
	return decoded
}

func decodeCredentialCandidateEscapes(input credentialCandidateBuffer) credentialCandidateBuffer {
	var decoded credentialCandidateBuffer
	for index := 0; index < input.length; {
		if input.bytes[index] == '\\' {
			if encoded, encodedSize, consumed, ok := decodeCredentialCandidateEscape(input.bytes[index:input.length]); ok {
				for encodedIndex := 0; encodedIndex < encodedSize; encodedIndex++ {
					if !credentialCandidateAppend(&decoded, encoded[encodedIndex]) {
						decoded.truncated = true
						return decoded
					}
				}
				index += consumed
				continue
			}
		}
		if !credentialCandidateAppend(&decoded, input.bytes[index]) {
			break
		}
		index++
	}
	decoded.truncated = decoded.truncated || input.truncated
	return decoded
}

func decodeCredentialCandidateEscape(text []byte) ([utf8.UTFMax]byte, int, int, bool) {
	var encoded [utf8.UTFMax]byte
	if len(text) < 2 || text[0] != '\\' {
		return encoded, 0, 0, false
	}
	if text[1] == 'x' {
		if len(text) < 4 || !isHexDigit(text[2]) || !isHexDigit(text[3]) {
			return encoded, 0, 0, false
		}
		encoded[0] = hexByte(text[2], text[3])
		return encoded, 1, 4, true
	}
	if text[1] >= '0' && text[1] <= '7' {
		if len(text) < 4 || text[1] > '3' || text[2] < '0' || text[2] > '7' || text[3] < '0' || text[3] > '7' {
			return encoded, 0, 0, false
		}
		encoded[0] = (text[1]-'0')<<6 | (text[2]-'0')<<3 | (text[3] - '0')
		return encoded, 1, 4, true
	}
	var escaped rune
	consumed := 2
	switch text[1] {
	case '"', '\\', '\'', '/':
		escaped = rune(text[1])
	case 'a':
		escaped = '\a'
	case 'b':
		escaped = '\b'
	case 'f':
		escaped = '\f'
	case 'n':
		escaped = '\n'
	case 'r':
		escaped = '\r'
	case 't':
		escaped = '\t'
	case 'v':
		escaped = '\v'
	case 'u', 'U':
		digits := 4
		if text[1] == 'U' {
			digits = 8
		}
		if len(text) < 2+digits {
			return encoded, 0, 0, false
		}
		for index := 0; index < digits; index++ {
			if !isHexDigit(text[2+index]) {
				return encoded, 0, 0, false
			}
			escaped = escaped<<4 | rune(hexDigit(text[2+index]))
		}
		consumed += digits
		if text[1] == 'U' && escaped > utf8.MaxRune || escaped >= 0xD800 && escaped <= 0xDFFF && text[1] == 'U' {
			return encoded, 0, 0, false
		}
		if escaped >= 0xD800 && escaped <= 0xDBFF {
			if len(text) >= consumed+6 && text[consumed] == '\\' && text[consumed+1] == 'u' {
				var low rune
				for index := 0; index < 4; index++ {
					if !isHexDigit(text[consumed+2+index]) {
						return encoded, 0, 0, false
					}
					low = low<<4 | rune(hexDigit(text[consumed+2+index]))
				}
				if low >= 0xDC00 && low <= 0xDFFF {
					escaped = utf16.DecodeRune(escaped, low)
					consumed += 6
				} else {
					escaped = utf8.RuneError
				}
			} else {
				escaped = utf8.RuneError
			}
		} else if escaped >= 0xDC00 && escaped <= 0xDFFF {
			escaped = utf8.RuneError
		}
	default:
		return encoded, 0, 0, false
	}
	encodedSize := utf8.EncodeRune(encoded[:], escaped)
	return encoded, encodedSize, consumed, true
}

func credentialDecodedByteMayChange(value byte, allowPercentEncoding, allowUnicodeEncoding bool) bool {
	switch value {
	case '%', '+':
		return allowPercentEncoding
	case '\\':
		return allowUnicodeEncoding
	default:
		return false
	}
}

func credentialEncodingPrefixIsValid(text string) bool {
	if len(text) < 2 {
		return false
	}
	switch text[0] {
	case '%':
		return len(text) >= 3 && isHexDigit(text[1]) && isHexDigit(text[2])
	case '+':
		return true
	case '\\':
		switch text[1] {
		case 'x':
			return len(text) >= 4 && isHexDigit(text[2]) && isHexDigit(text[3])
		case 'u':
			return len(text) >= 6 && isHexDigit(text[2]) && isHexDigit(text[3]) && isHexDigit(text[4]) && isHexDigit(text[5])
		case 'U':
			return len(text) >= 10 && isHexDigit(text[2]) && isHexDigit(text[3]) && isHexDigit(text[4]) && isHexDigit(text[5]) && isHexDigit(text[6]) && isHexDigit(text[7]) && isHexDigit(text[8]) && isHexDigit(text[9])
		case '0', '1', '2', '3':
			return len(text) >= 4 && text[2] >= '0' && text[2] <= '7' && text[3] >= '0' && text[3] <= '7'
		case '"', '\\', '\'', '/', 'a', 'b', 'f', 'n', 'r', 't', 'v':
			return true
		default:
			return false
		}
	default:
		return false
	}
}

func credentialTextContainsVariant(text string, variants []credentialVariant) bool {
	contains, _ := credentialTextContainsVariantDetailed(text, variants)
	return contains
}

func credentialTextContainsVariantDetailed(text string, variants []credentialVariant) (bool, bool) {
	return credentialTextContainsVariantDetailedWithBudget(text, variants, nil)
}

func credentialTextContainsVariantDetailedWithBudget(text string, variants []credentialVariant, budget *credentialSnapshotBudget) (bool, bool) {
	for index := 0; index < len(text); {
		if budget != nil && !budget.reserveMatchWork(credentialMatchWorkEstimate(text[index:], variants)) {
			return false, false
		}
		if _, _, ok, exhausted := credentialMatchAtDetailed(text[index:], variants); ok {
			return true, false
		} else if exhausted {
			return false, true
		}
		_, size := utf8.DecodeRuneInString(text[index:])
		if size == 0 {
			return true, false
		}
		index += size
	}
	return false, false
}

func credentialMatchAt(text string, variants []credentialVariant) (credentialVariant, int, bool) {
	variant, consumed, matched, _ := credentialMatchAtDetailed(text, variants)
	return variant, consumed, matched
}

func credentialMatchAtDetailed(text string, variants []credentialVariant) (credentialVariant, int, bool, bool) {
	var longest credentialVariant
	longestConsumed := 0
	exhausted := false
	for _, variant := range variants {
		consumed, ok, variantExhausted := credentialPrefixDetailed(text, variant)
		if ok && consumed > longestConsumed {
			longest = variant
			longestConsumed = consumed
		}
		exhausted = exhausted || variantExhausted
	}
	if longestConsumed > 0 {
		return longest, longestConsumed, true, false
	}
	return credentialVariant{}, 0, false, exhausted
}

func credentialSnapshotContainsGoFormattedVariantWithBudget(value any, variants []credentialVariant, budget *credentialSnapshotBudget) (bool, bool) {
	switch typed := value.(type) {
	case float32:
		return credentialTextContainsVariantDetailedWithBudget(strconv.FormatFloat(float64(typed), 'g', -1, 32), variants, budget)
	case float64:
		return credentialTextContainsVariantDetailedWithBudget(strconv.FormatFloat(typed, 'g', -1, 64), variants, budget)
	case string:
		for _, quoted := range []string{strconv.Quote(typed), strconv.QuoteToASCII(typed)} {
			contains, exhausted := credentialTextContainsVariantDetailedWithBudget(quoted, variants, budget)
			if exhausted || contains {
				return contains, exhausted
			}
		}
	case map[string]any:
		for key, child := range typed {
			for _, quoted := range []string{strconv.Quote(key), strconv.QuoteToASCII(key)} {
				contains, exhausted := credentialTextContainsVariantDetailedWithBudget(quoted, variants, budget)
				if exhausted || contains {
					return contains, exhausted
				}
			}
			contains, exhausted := credentialSnapshotContainsGoFormattedVariantWithBudget(child, variants, budget)
			if exhausted || contains {
				return contains, exhausted
			}
		}
	case []any:
		for _, child := range typed {
			contains, exhausted := credentialSnapshotContainsGoFormattedVariantWithBudget(child, variants, budget)
			if exhausted || contains {
				return contains, exhausted
			}
		}
	}
	return false, false
}

func credentialSnapshotContainsURLVariantWithBudget(value any, variants []credentialVariant, budget *credentialSnapshotBudget) bool {
	switch typed := value.(type) {
	case string:
		return credentialTextContainsURLSerializedVariantWithBudget(typed, variants, budget)
	case map[string]any:
		for key, child := range typed {
			if credentialSnapshotContainsURLVariantWithBudget(key, variants, budget) {
				return true
			}
			if credentialSnapshotContainsURLVariantWithBudget(child, variants, budget) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if credentialSnapshotContainsURLVariantWithBudget(child, variants, budget) {
				return true
			}
		}
	}
	return false
}

func credentialTextContainsURLSerializedVariantWithBudget(text string, variants []credentialVariant, budget *credentialSnapshotBudget) bool {
	for _, encoded := range []string{url.QueryEscape(text), url.PathEscape(text), userInfoEscape(text)} {
		if credentialURLTextContainsVariantWithBudget(encoded, variants, budget) {
			return true
		}
	}
	return false
}

func credentialTextContainsCompleteGoQuotedVariantWithBudget(text string, variants []credentialVariant, budget *credentialSnapshotBudget) bool {
	for _, quoted := range []string{strconv.Quote(text), strconv.QuoteToASCII(text)} {
		if credentialURLTextContainsVariantWithBudget(quoted, variants, budget) {
			return true
		}
	}
	return false
}

func credentialURLTextContainsVariantWithBudget(text string, variants []credentialVariant, budget *credentialSnapshotBudget) bool {
	for index := 0; index < len(text); {
		if budget != nil && !budget.reserveMatchWork(credentialMatchWorkEstimate(text[index:], variants)) {
			return false
		}
		_, _, contains, _ := credentialMatchAtDetailed(text[index:], variants)
		if contains {
			return true
		}
		_, size := utf8.DecodeRuneInString(text[index:])
		if size == 0 {
			return false
		}
		index += size
	}
	return false
}
