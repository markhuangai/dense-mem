package observability

import (
	"strings"
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
