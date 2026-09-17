package observability

import (
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

type credentialDecodedByte struct {
	value byte
	end   int
}

func decodedCredentialPrefixDetailed(text, variant string, allowPercentEncoding bool) (int, bool, bool) {
	raw := rawCredentialPrefix(text, credentialRawByteLimit(len(variant)))
	return matchCredentialLayers(raw, variant, allowPercentEncoding, true, false)
}

func reverseDecodedCredentialPrefixDetailed(text, variant string) (int, bool, bool) {
	raw := rawCredentialPrefix(text, credentialRawByteLimit(len(variant)))
	return matchCredentialLayers(raw, variant, true, true, true)
}

const maxCredentialDecodeLayers = 4

func matchCredentialLayers(input []credentialDecodedByte, variant string, allowPercentEncoding, allowUnicodeEncoding, percentFirst bool) (int, bool, bool) {
	decoded := input
	for layer := 0; layer < maxCredentialDecodeLayers; layer++ {
		if consumed, ok := matchDecodedCredentialPrefix(decoded, variant); ok {
			return consumed, true, false
		}
		before := decoded
		if percentFirst && allowPercentEncoding {
			decoded = decodeCredentialPercentLayer(decoded)
			if consumed, ok := matchDecodedCredentialPrefix(decoded, variant); ok {
				return consumed, true, false
			}
		}
		if allowUnicodeEncoding {
			decoded = decodeCredentialEscapeLayer(decoded)
			if consumed, ok := matchDecodedCredentialPrefix(decoded, variant); ok {
				return consumed, true, false
			}
		}
		if !percentFirst && allowPercentEncoding {
			decoded = decodeCredentialPercentLayer(decoded)
			if consumed, ok := matchDecodedCredentialPrefix(decoded, variant); ok {
				return consumed, true, false
			}
		}
		if credentialDecodedBytesEqual(before, decoded) {
			return 0, false, false
		}
	}
	if credentialDecodedLayerChanges(decoded, allowPercentEncoding, allowUnicodeEncoding, percentFirst) {
		return 0, false, true
	}
	return 0, false, false
}

func credentialDecodedLayerChanges(input []credentialDecodedByte, allowPercentEncoding, allowUnicodeEncoding, percentFirst bool) bool {
	decoded := input
	if percentFirst && allowPercentEncoding {
		next := decodeCredentialPercentLayer(decoded)
		if !credentialDecodedBytesEqual(decoded, next) {
			return true
		}
		decoded = next
	}
	if allowUnicodeEncoding {
		next := decodeCredentialEscapeLayer(decoded)
		if !credentialDecodedBytesEqual(decoded, next) {
			return true
		}
		decoded = next
	}
	if !percentFirst && allowPercentEncoding {
		next := decodeCredentialPercentLayer(decoded)
		if !credentialDecodedBytesEqual(decoded, next) {
			return true
		}
	}
	return false
}

func credentialDecodedBytesEqual(left, right []credentialDecodedByte) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].value != right[index].value || left[index].end != right[index].end {
			return false
		}
	}
	return true
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
			current = decodeCredentialCandidatePercent(current)
			roundChanged = roundChanged || !credentialCandidateBuffersEqual(before, current)
			status = credentialCandidatePrefixStatusFor(current, variant, limit, allowPercentEncoding, allowUnicodeEncoding)
			if status == credentialCandidateMatch {
				return true
			}
			if status == credentialCandidateNoMatch && !allowUnicodeEncoding {
				return false
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
			current = decodeCredentialCandidatePercent(current)
			roundChanged = roundChanged || !credentialCandidateBuffersEqual(before, current)
			status = credentialCandidatePrefixStatusFor(current, variant, limit, allowPercentEncoding, allowUnicodeEncoding)
			if status == credentialCandidateMatch {
				return true
			}
			if status == credentialCandidateNoMatch {
				return false
			}
		}
		if !roundChanged && !current.truncated {
			return false
		}
		if layer == maxCredentialDecodeLayers-1 {
			return roundChanged || current.truncated
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

func credentialRawByteLimit(variantLength int) int {
	const maxEncodingExpansionPerLayer = 10
	maxInt := int(^uint(0) >> 1)
	maxEncodingExpansion := 1
	for layer := 0; layer < maxCredentialDecodeLayers; layer++ {
		if maxEncodingExpansion > maxInt/maxEncodingExpansionPerLayer {
			return maxInt
		}
		maxEncodingExpansion *= maxEncodingExpansionPerLayer
	}
	if variantLength > maxInt/maxEncodingExpansion {
		return maxInt
	}
	return variantLength * maxEncodingExpansion
}

func rawCredentialPrefix(text string, maxBytes int) []credentialDecodedByte {
	if maxBytes <= 0 {
		return nil
	}
	capacity := maxBytes
	if capacity > len(text) {
		capacity = len(text)
	}
	raw := make([]credentialDecodedByte, 0, capacity)
	for textIndex := 0; textIndex < len(text) && len(raw) < maxBytes; {
		_, size := utf8.DecodeRuneInString(text[textIndex:])
		if size == 0 {
			break
		}
		for index := 0; index < size && len(raw) < maxBytes; index++ {
			raw = append(raw, credentialDecodedByte{value: text[textIndex+index], end: textIndex + size})
		}
		textIndex += size
	}
	return raw
}

func decodeCredentialPercentLayer(input []credentialDecodedByte) []credentialDecodedByte {
	decoded := make([]credentialDecodedByte, 0, len(input))
	for index := 0; index < len(input); {
		if input[index].value == '%' && index+2 < len(input) && isHexDigit(input[index+1].value) && isHexDigit(input[index+2].value) {
			decoded = append(decoded, credentialDecodedByte{
				value: hexByte(input[index+1].value, input[index+2].value),
				end:   input[index+2].end,
			})
			index += 3
			continue
		}
		if input[index].value == '+' {
			decoded = append(decoded, credentialDecodedByte{value: ' ', end: input[index].end})
			index++
			continue
		}
		decoded = append(decoded, input[index])
		index++
	}
	return decoded
}

func decodeCredentialEscapeLayer(input []credentialDecodedByte) []credentialDecodedByte {
	if len(input) == 0 {
		return nil
	}
	raw := make([]byte, len(input))
	for index, value := range input {
		raw[index] = value.value
	}
	text := string(raw)
	decoded := make([]credentialDecodedByte, 0, len(input))
	for index := 0; index < len(input); {
		if escaped, consumed, ok := decodeGoByteEscape(text[index:]); ok {
			decoded = append(decoded, credentialDecodedByte{value: escaped, end: input[index+consumed-1].end})
			index += consumed
			continue
		}
		if escaped, consumed, ok := decodeEscapedRune(text[index:]); ok {
			var encoded [utf8.UTFMax]byte
			encodedSize := utf8.EncodeRune(encoded[:], escaped)
			end := input[index+consumed-1].end
			for encodedIndex := 0; encodedIndex < encodedSize; encodedIndex++ {
				decoded = append(decoded, credentialDecodedByte{value: encoded[encodedIndex], end: end})
			}
			index += consumed
			continue
		}
		_, size := utf8.DecodeRuneInString(text[index:])
		if size == 0 {
			break
		}
		for encodedIndex := 0; encodedIndex < size; encodedIndex++ {
			decoded = append(decoded, input[index+encodedIndex])
		}
		index += size
	}
	return decoded
}

func matchDecodedCredentialPrefix(decoded []credentialDecodedByte, variant string) (int, bool) {
	if len(decoded) < len(variant) {
		return 0, false
	}
	for index := 0; index < len(variant); index++ {
		if decoded[index].value != variant[index] {
			return 0, false
		}
	}
	return decoded[len(variant)-1].end, true
}

func credentialTextContainsVariant(text string, variants []credentialVariant) bool {
	contains, _ := credentialTextContainsVariantDetailed(text, variants)
	return contains
}

func credentialTextContainsVariantDetailed(text string, variants []credentialVariant) (bool, bool) {
	for index := 0; index < len(text); {
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
	exhausted := false
	for _, variant := range variants {
		consumed, ok, variantExhausted := credentialPrefixDetailed(text, variant)
		if ok {
			return variant, consumed, true, false
		}
		exhausted = exhausted || variantExhausted
	}
	return credentialVariant{}, 0, false, exhausted
}
