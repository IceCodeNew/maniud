// Package jsonstrict reads bounded JSON without duplicate keys and decodes
// values without unknown fields.
package jsonstrict

import (
	"bytes"
	"encoding/json"
	"io"
)

// Decode reads at most maximumBytes and decodes one complete JSON value into target.
func Decode(reader io.Reader, maximumBytes int64, target any) bool {
	value, valid := Read(reader, maximumBytes)
	if !valid {
		return false
	}

	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.DisallowUnknownFields()

	if decoder.Decode(target) != nil {
		return false
	}

	return decoder.Decode(&struct{}{}) == io.EOF
}

// Read returns one complete, bounded JSON value whose objects have unique keys.
// It leaves field validation to the caller that owns the decoded schema.
func Read(reader io.Reader, maximumBytes int64) ([]byte, bool) {
	value, err := io.ReadAll(io.LimitReader(reader, maximumBytes+1))
	if err != nil || int64(len(value)) > maximumBytes || !uniqueKeys(value) {
		return nil, false
	}

	return value, true
}

func uniqueKeys(value []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(value))
	if !consumeValue(decoder) {
		return false
	}

	_, err := decoder.Token()

	return err == io.EOF
}

func consumeValue(decoder *json.Decoder) bool {
	token, err := decoder.Token()
	if err != nil {
		return false
	}

	delimiter, composite := token.(json.Delim)
	if !composite {
		return true
	}

	if delimiter == '[' {
		return consumeArray(decoder)
	}

	return consumeObject(decoder)
}

func consumeArray(decoder *json.Decoder) bool {
	for decoder.More() {
		if !consumeValue(decoder) {
			return false
		}
	}

	return consumeClosing(decoder, ']')
}

func consumeObject(decoder *json.Decoder) bool {
	keys := make(map[string]struct{})

	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return false
		}

		key, _ := keyToken.(string)

		if _, duplicate := keys[key]; duplicate {
			return false
		}

		keys[key] = struct{}{}

		if !consumeValue(decoder) {
			return false
		}
	}

	return consumeClosing(decoder, '}')
}

func consumeClosing(decoder *json.Decoder, expected json.Delim) bool {
	closing, err := decoder.Token()
	if err != nil {
		return false
	}

	closingDelimiter, valid := closing.(json.Delim)

	return valid && closingDelimiter == expected
}
