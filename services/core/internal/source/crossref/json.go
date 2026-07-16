package crossref

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
)

type DuplicateJSONKeyError struct {
	Path string
	Key  string
}

func (err *DuplicateJSONKeyError) Error() string {
	return fmt.Sprintf("duplicate JSON object key %q at %s", err.Key, err.Path)
}

func validateUniqueJSONObjects(payload []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := validateJSONValue(decoder, "$"); err != nil {
		return err
	}

	if token, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err != nil {
			return fmt.Errorf("decode trailing JSON token: %w", err)
		}
		return fmt.Errorf("JSON must contain exactly one value; found trailing token %v", token)
	}
	return nil
}

func validateJSONValue(decoder *json.Decoder, path string) error {
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("decode JSON token at %s: %w", path, err)
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return nil
	}

	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return fmt.Errorf("decode JSON object key at %s: %w", path, err)
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("JSON object key at %s is not a string", path)
			}
			if _, duplicate := seen[key]; duplicate {
				return &DuplicateJSONKeyError{
					Path: path,
					Key:  key,
				}
			}
			seen[key] = struct{}{}
			if err := validateJSONValue(decoder, jsonObjectPath(path, key)); err != nil {
				return err
			}
		}
		return consumeJSONDelimiter(decoder, '}', path)
	case '[':
		index := 0
		for decoder.More() {
			if err := validateJSONValue(
				decoder,
				path+"["+strconv.Itoa(index)+"]",
			); err != nil {
				return err
			}
			index++
		}
		return consumeJSONDelimiter(decoder, ']', path)
	default:
		return fmt.Errorf("unexpected JSON delimiter %q at %s", delimiter, path)
	}
}

func consumeJSONDelimiter(
	decoder *json.Decoder,
	want json.Delim,
	path string,
) error {
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("decode closing JSON delimiter at %s: %w", path, err)
	}
	got, ok := token.(json.Delim)
	if !ok || got != want {
		return fmt.Errorf("closing JSON delimiter at %s = %v, want %q", path, token, want)
	}
	return nil
}

func jsonObjectPath(parent, key string) string {
	encoded, _ := json.Marshal(key)
	return parent + "[" + string(encoded) + "]"
}
