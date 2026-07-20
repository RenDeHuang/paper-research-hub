package nlmcatalog

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"
)

func decodeStrictJSON(payload []byte, destination any) error {
	if !utf8.Valid(payload) {
		return errors.New("JSON contains invalid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := consumeStrictJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("JSON contains multiple values")
		}
		return fmt.Errorf("decode trailing JSON: %w", err)
	}
	if err := json.Unmarshal(payload, destination); err != nil {
		return err
	}
	return nil
}

func consumeStrictJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}

	switch delimiter {
	case '{':
		keys := make(map[string]struct{})
		for decoder.More() {
			token, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := token.(string)
			if !ok {
				return errors.New("JSON object key is not a string")
			}
			if _, duplicate := keys[key]; duplicate {
				return errors.New("JSON contains a duplicate object key")
			}
			keys[key] = struct{}{}
			if err := consumeStrictJSONValue(decoder); err != nil {
				return err
			}
		}
		return consumeClosingDelimiter(decoder, '}')
	case '[':
		for decoder.More() {
			if err := consumeStrictJSONValue(decoder); err != nil {
				return err
			}
		}
		return consumeClosingDelimiter(decoder, ']')
	default:
		return errors.New("JSON contains an unexpected delimiter")
	}
}

func consumeClosingDelimiter(decoder *json.Decoder, want json.Delim) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if token != want {
		return errors.New("JSON contains a mismatched delimiter")
	}
	return nil
}
