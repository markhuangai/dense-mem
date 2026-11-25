package observability

import (
	"bytes"
	"math/bits"
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
		if length == 2 {
			if decoded, ok := credentialShortEscape(value.value); ok {
				stream.markChanged()
				return credentialDecodedByte{value: decoded, end: value.end}, true
			}
			if !strings.ContainsRune("uxU0123", rune(value.value)) {
				stream.unreadSource([]credentialDecodedByte{value})
				return first, true
			}
		}
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
	if len(values) > len(stream.unread)-stream.unreadLen {
		panic("credential decoder unread buffer exhausted")
	}
	copy(stream.unread[len(values):], stream.unread[:stream.unreadLen])
	copy(stream.unread[:], values)
	stream.unreadLen += len(values)
}

func (stream *credentialDecodeStream) markChanged() {
	if stream.changed != nil {
		*stream.changed = true
	}
}

func credentialMatchStreamPrefix(text, variant string, kinds []credentialStreamKind) (int, bool, bool) {
	var streams [credentialStreamMaxPasses + 1]credentialDecodeStream
	var changed [credentialStreamMaxPasses + 1]bool
	stream := credentialStream(&streams, text, kinds, &changed)
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

func credentialStream(streams *[credentialStreamMaxPasses + 1]credentialDecodeStream, text string, kinds []credentialStreamKind, changed *[credentialStreamMaxPasses + 1]bool) *credentialDecodeStream {
	streams[0] = credentialDecodeStream{kind: credentialStreamRaw, text: text}
	for index, kind := range kinds {
		streams[index+1] = credentialDecodeStream{kind: kind, source: &streams[index], changed: &changed[index+1]}
	}
	return &streams[len(kinds)]
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
			if text[index] != variant[index] {
				var decoded byte
				switch text[index] {
				case '%':
					decoded = hexByte(text[index+1], text[index+2])
				case '+':
					decoded = ' '
				case '\\':
					escaped, _, _, ok := decodeCredentialCandidateEscape([]byte(text[index:min(len(text), index+12)]))
					if !ok {
						return false
					}
					decoded = escaped[0]
				}
				// Literal mismatches cannot change in later decoding layers.
				if decoded != variant[index] && !credentialDecodedByteMayChange(decoded, true, true) {
					return false
				}
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
	prefixLimit := min(len(current.bytes), max(limit, credentialLiteralCandidateLimit))
	if current.length > prefixLimit {
		current.length = prefixLimit
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
	return bytes.Equal(left.bytes[:left.length], right.bytes[:right.length])
}

func credentialCandidateAppend(candidate *credentialCandidateBuffer, values ...byte) bool {
	written := copy(candidate.bytes[candidate.length:], values)
	candidate.length += written
	if written != len(values) {
		candidate.truncated = true
		return false
	}
	return true
}

func decodeCredentialCandidatePercent(input credentialCandidateBuffer) credentialCandidateBuffer {
	var decoded credentialCandidateBuffer
	for index := 0; index < input.length; {
		next := bytes.IndexAny(input.bytes[index:input.length], "%+")
		if next < 0 {
			credentialCandidateAppend(&decoded, input.bytes[index:input.length]...)
			break
		}
		if !credentialCandidateAppend(&decoded, input.bytes[index:index+next]...) {
			break
		}
		index += next
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
		next := bytes.IndexByte(input.bytes[index:input.length], '\\')
		if next < 0 {
			credentialCandidateAppend(&decoded, input.bytes[index:input.length]...)
			break
		}
		if !credentialCandidateAppend(&decoded, input.bytes[index:index+next]...) {
			break
		}
		index += next
		if input.bytes[index] == '\\' {
			if encoded, encodedSize, consumed, ok := decodeCredentialCandidateEscape(input.bytes[index:input.length]); ok {
				if input.truncated && consumed == 6 && input.bytes[index+1] == 'u' && input.length-index < 12 {
					high := hexByte(input.bytes[index+2], input.bytes[index+3])
					if high >= 0xD8 && high <= 0xDB {
						// The unread tail may complete the pair, so the full matcher must decide.
						decoded.truncated = true
						return decoded
					}
				}
				if !credentialCandidateAppend(&decoded, encoded[:encodedSize]...) {
					return decoded
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

func credentialShortEscape(value byte) (byte, bool) {
	switch value {
	case '"', '\\', '\'', '/':
		return value, true
	case 'a':
		return '\a', true
	case 'b':
		return '\b', true
	case 'f':
		return '\f', true
	case 'n':
		return '\n', true
	case 'r':
		return '\r', true
	case 't':
		return '\t', true
	case 'v':
		return '\v', true
	default:
		return 0, false
	}
}

func decodeCredentialCandidateEscape(text []byte) ([utf8.UTFMax]byte, int, int, bool) {
	var encoded [utf8.UTFMax]byte
	if len(text) < 2 || text[0] != '\\' {
		return encoded, 0, 0, false
	}
	if decoded, ok := credentialShortEscape(text[1]); ok {
		encoded[0] = decoded
		return encoded, 1, 2, true
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

type credentialVariantIndex struct {
	variants        []credentialVariant
	starts          [256]bool
	literal         [256][]int
	decoded         [256][]int
	decodable       []int
	singleDecoder   bool
	markerCollision bool
}

var emptyCredentialVariantIndex credentialVariantIndex

func newCredentialVariantIndex(variants []credentialVariant) *credentialVariantIndex {
	if len(variants) == 0 {
		return &emptyCredentialVariantIndex
	}
	index := &credentialVariantIndex{variants: variants}
	for ordinal, variant := range variants {
		first := variant.text[0]
		index.starts[first] = true
		if !variant.allowPercentEncoding && !variant.allowUnicodeEncoding {
			index.literal[first] = append(index.literal[first], ordinal)
			continue
		}
		index.decoded[first] = append(index.decoded[first], ordinal)
		index.decodable = append(index.decodable, ordinal)
		index.singleDecoder = index.singleDecoder || !variant.allowPercentEncoding || !variant.allowUnicodeEncoding
	}
	if len(index.decodable) > 0 {
		index.starts['%'], index.starts['\\'], index.starts['+'] = true, true, true
	}
	index.markerCollision, _ = index.contains(CredentialProtectionRedacted, nil, false)
	return index
}

func (variants *credentialVariantIndex) contains(text string, budget *credentialSnapshotBudget, checkDepth bool) (bool, bool) {
	if len(variants.variants) == 0 {
		return false, false
	}
	for index := 0; index < len(text); {
		if !variants.starts[text[index]] {
			index++
			continue
		}
		_, _, ok, exhausted := variants.matchAt(text[index:], budget)
		if budget != nil && budget.matchWorkExceeded {
			return false, false
		}
		if ok {
			return true, false
		}
		if checkDepth && exhausted {
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

func (variants *credentialVariantIndex) matchAt(text string, budget *credentialSnapshotBudget) (credentialVariant, int, bool, bool) {
	first := text[0]
	longest := len(variants.variants)
	longestConsumed := 0
	exhausted := false
	match := func(ordinals []int) {
		for _, ordinal := range ordinals {
			variant := variants.variants[ordinal]
			var consumed int
			var ok, variantExhausted bool
			if variant.allowPercentEncoding || variant.allowUnicodeEncoding {
				consumed, ok, variantExhausted = encodedCredentialPrefixDetailed(text, variant.text, variant.allowPercentEncoding, variant.allowUnicodeEncoding, budget)
				if budget != nil && budget.matchWorkExceeded {
					return
				}
			} else {
				consumed, ok, variantExhausted = credentialPrefixDetailed(text, variant)
			}
			if ok && (consumed > longestConsumed || consumed == longestConsumed && ordinal < longest) {
				longest = ordinal
				longestConsumed = consumed
			}
			exhausted = exhausted || variantExhausted
		}
	}
	match(variants.literal[first])
	if len(variants.decodable) == 0 || !credentialDecodedByteMayChange(first, true, true) || !credentialEncodingPrefixIsValid(text) {
		match(variants.decoded[first])
	} else if variants.singleDecoder {
		match(variants.decodable)
	} else {
		starts, unresolved := credentialReachableStarts(text)
		if unresolved {
			match(variants.decodable)
		} else {
			for block, values := range starts {
				for values != 0 && (budget == nil || !budget.matchWorkExceeded) {
					value := bits.TrailingZeros64(values)
					match(variants.decoded[block*64+value])
					values &= values - 1
				}
			}
		}
	}
	if longestConsumed > 0 {
		return variants.variants[longest], longestConsumed, true, false
	}
	return credentialVariant{}, 0, false, exhausted
}

func credentialReachableStarts(text string) (starts [4]uint64, unresolved bool) {
	add := func(value byte) { starts[value/64] |= uint64(1) << (value % 64) }
	first := text[0]
	add(first)
	switch first {
	case '+':
		add(' ')
		return starts, false
	case '%':
		if decoded := hexByte(text[1], text[2]); !credentialDecodedByteMayChange(decoded, true, true) {
			add(decoded)
			return starts, false
		}
	case '\\':
		if decoded, ok := credentialShortEscape(text[1]); ok && decoded != '\\' {
			add(decoded)
			return starts, false
		}
	}
	if decoded, known, depth := credentialLeadingShortEscape(text); known {
		add('\\')
		add(decoded)
		return starts, depth
	}
	var streams [credentialStreamMaxPasses + 1]credentialDecodeStream
	var changed [credentialStreamMaxPasses + 1]bool
	for _, order := range [][2]credentialStreamKind{
		{credentialStreamEscapes, credentialStreamPercent},
		{credentialStreamPercent, credentialStreamEscapes},
	} {
		var kinds [credentialStreamMaxPasses]credentialStreamKind
		previous := credentialDecodedByte{value: first, end: 1}
		for pass := range kinds {
			kinds[pass] = order[pass%len(order)]
			value, ok := credentialStream(&streams, text, kinds[:pass+1], &changed).next()
			if !ok {
				break
			}
			if pass >= 2*maxCredentialDecodeLayers {
				// A fifth-layer change can still require a fail-closed result.
				if value != previous {
					return starts, true
				}
			} else {
				add(value.value)
			}
			if !credentialDecodedByteMayChange(value.value, true, true) {
				break
			}
			previous = value
		}
	}
	return starts, false
}

func credentialLeadingShortEscape(text string) (byte, bool, bool) {
	// Quoting wire JSON repeatedly produces runs of literal or percent-encoded backslashes followed by a short escape.
	width := 1
	if text[0] == '%' {
		if hexByte(text[1], text[2]) != '\\' {
			return 0, false, false
		}
		width = 3
	}
	count := 0
	for len(text) >= width {
		if width == 1 && text[0] != '\\' || width == 3 && (text[0] != '%' || !isHexDigit(text[1]) || !isHexDigit(text[2]) || hexByte(text[1], text[2]) != '\\') {
			break
		}
		count++
		if count > 1<<(maxCredentialDecodeLayers+1) {
			return '\\', true, true
		}
		text = text[width:]
	}
	if text == "" {
		return 0, false, false
	}
	end := text[0]
	if width == 3 && end == '%' && len(text) >= 3 && isHexDigit(text[1]) && isHexDigit(text[2]) {
		end = hexByte(text[1], text[2])
	}
	if _, short := credentialShortEscape(end); !short || end == '\\' {
		return 0, false, false
	}
	layers := maxCredentialDecodeLayers
	if width == 3 {
		// The escapes-first order needs a round to turn the percent tokens into backslashes.
		layers--
	}
	for layer := 0; layer <= layers; layer++ {
		next := count / 2
		if count%2 != 0 {
			if decoded, ok := credentialShortEscape(end); ok {
				end = decoded
			} else {
				next++
			}
		}
		if next == count {
			return '\\', true, false
		}
		if layer == layers {
			return '\\', true, true
		}
		if next == 0 {
			return end, true, false
		}
		count = next
	}
	return 0, false, false
}

func credentialSnapshotContainsGoFormattedVariantWithBudget(value any, variants *credentialVariantIndex, budget *credentialSnapshotBudget) (bool, bool) {
	if len(variants.variants) == 0 {
		return false, false
	}
	switch typed := value.(type) {
	case float32:
		return variants.contains(strconv.FormatFloat(float64(typed), 'g', -1, 32), budget, true)
	case float64:
		return variants.contains(strconv.FormatFloat(typed, 'g', -1, 64), budget, true)
	case string:
		forms := [2]string{strconv.Quote(typed), strconv.QuoteToASCII(typed)}
		for i, quoted := range forms {
			if i > 0 && quoted == forms[0] {
				continue
			}
			contains, exhausted := variants.contains(quoted, budget, true)
			if exhausted || contains || budget.matchWorkExceeded {
				return contains, exhausted
			}
		}
	case map[string]any:
		for key, child := range typed {
			forms := [2]string{strconv.Quote(key), strconv.QuoteToASCII(key)}
			for i, quoted := range forms {
				if i > 0 && quoted == forms[0] {
					continue
				}
				contains, exhausted := variants.contains(quoted, budget, true)
				if exhausted || contains || budget.matchWorkExceeded {
					return contains, exhausted
				}
			}
			contains, exhausted := credentialSnapshotContainsGoFormattedVariantWithBudget(child, variants, budget)
			if exhausted || contains || budget.matchWorkExceeded {
				return contains, exhausted
			}
		}
	case []any:
		for _, child := range typed {
			contains, exhausted := credentialSnapshotContainsGoFormattedVariantWithBudget(child, variants, budget)
			if exhausted || contains || budget.matchWorkExceeded {
				return contains, exhausted
			}
		}
	}
	return false, false
}

func credentialSnapshotContainsURLVariantWithBudget(value any, variants *credentialVariantIndex, budget *credentialSnapshotBudget) bool {
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

func credentialTextContainsURLSerializedVariantWithBudget(text string, variants *credentialVariantIndex, budget *credentialSnapshotBudget) bool {
	forms := [3]string{url.QueryEscape(text), url.PathEscape(text), userInfoEscape(text)}
	for i, encoded := range forms {
		if i > 0 && encoded == forms[0] || i == 2 && encoded == forms[1] {
			continue
		}
		if contains, _ := variants.contains(encoded, budget, false); contains {
			return true
		}
		if budget.matchWorkExceeded {
			return false
		}
	}
	return false
}

func credentialTextContainsCompleteGoQuotedVariantWithBudget(text string, variants *credentialVariantIndex, budget *credentialSnapshotBudget) bool {
	forms := [2]string{strconv.Quote(text), strconv.QuoteToASCII(text)}
	for i, quoted := range forms {
		if i > 0 && quoted == forms[0] {
			continue
		}
		if contains, _ := variants.contains(quoted, budget, false); contains {
			return true
		}
		if budget.matchWorkExceeded {
			return false
		}
	}
	return false
}
