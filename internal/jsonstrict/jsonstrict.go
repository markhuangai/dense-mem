package jsonstrict

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

const maximumJSONNestingDepth = 100

func Decode(reader io.Reader, destination any, maximumBytes int64) error {
	if reader == nil {
		return fmt.Errorf("JSON reader is required")
	}
	if destination == nil {
		return fmt.Errorf("JSON destination is required")
	}
	if maximumBytes <= 0 {
		return fmt.Errorf("JSON byte limit must be positive")
	}

	data, err := io.ReadAll(io.LimitReader(reader, maximumBytes+1))
	if err != nil {
		return err
	}
	if int64(len(data)) > maximumBytes {
		return fmt.Errorf("JSON input exceeds %d bytes", maximumBytes)
	}
	if err := RejectDuplicateFields(data); err != nil {
		return err
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err != nil {
			return err
		}
		return fmt.Errorf("input must contain one JSON value")
	}
	return nil
}

func RejectDuplicateFields(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := walkJSONValue(decoder, 0); err != nil {
		return err
	}
	if token, err := decoder.Token(); err != io.EOF {
		if err != nil {
			return err
		}
		return fmt.Errorf("unexpected trailing JSON token %v", token)
	}
	return nil
}

func walkJSONValue(decoder *json.Decoder, depth int) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	if depth >= maximumJSONNestingDepth {
		return fmt.Errorf("JSON nesting depth exceeds %d", maximumJSONNestingDepth)
	}

	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("JSON object key is not a string")
			}
			if _, exists := seen[key]; exists {
				return fmt.Errorf("duplicate JSON field %q", key)
			}
			seen[key] = struct{}{}
			if err := walkJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim('}') {
			return fmt.Errorf("malformed JSON object")
		}
	case '[':
		for decoder.More() {
			if err := walkJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim(']') {
			return fmt.Errorf("malformed JSON array")
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
	}
	return nil
}
