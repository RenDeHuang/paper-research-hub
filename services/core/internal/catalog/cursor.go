package catalog

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	cursorVersion       = 1
	minimumCursorSecret = 32
)

type cursorCodec struct {
	secret []byte
}

type cursorPayload struct {
	Version    int    `json:"v"`
	Generation string `json:"generation"`
	Resource   string `json:"resource"`
	FilterHash string `json:"filter_hash"`
	Sort       string `json:"sort"`
	State      string `json:"state,omitempty"`
	Value      string `json:"value,omitempty"`
	Key        string `json:"key,omitempty"`
	Ordinal    int    `json:"ordinal,omitempty"`
}

func newCursorCodec(secret []byte) (cursorCodec, error) {
	if len(secret) < minimumCursorSecret {
		return cursorCodec{}, fmt.Errorf(
			"cursor signing secret must contain at least %d bytes",
			minimumCursorSecret,
		)
	}
	return cursorCodec{secret: append([]byte(nil), secret...)}, nil
}

func (codec cursorCodec) Encode(payload cursorPayload) (string, error) {
	payload.Version = cursorVersion
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode cursor payload: %w", err)
	}

	payloadPart := base64.RawURLEncoding.EncodeToString(encoded)
	signature := codec.signature(payloadPart)
	signaturePart := base64.RawURLEncoding.EncodeToString(signature)
	return payloadPart + "." + signaturePart, nil
}

func (codec cursorCodec) Decode(value string) (cursorPayload, error) {
	parts := strings.Split(value, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return cursorPayload{}, ErrInvalidCursor
	}

	payloadBytes, err := decodeCanonicalBase64(parts[0])
	if err != nil {
		return cursorPayload{}, ErrInvalidCursor
	}
	signature, err := decodeCanonicalBase64(parts[1])
	if err != nil {
		return cursorPayload{}, ErrInvalidCursor
	}
	if !hmac.Equal(signature, codec.signature(parts[0])) {
		return cursorPayload{}, ErrInvalidCursor
	}

	var payload cursorPayload
	decoder := json.NewDecoder(strings.NewReader(string(payloadBytes)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return cursorPayload{}, ErrInvalidCursor
	}
	if payload.Version != cursorVersion ||
		payload.Generation == "" ||
		payload.Resource == "" ||
		payload.FilterHash == "" ||
		payload.Sort == "" {
		return cursorPayload{}, ErrInvalidCursor
	}
	return payload, nil
}

func (codec cursorCodec) signature(payloadPart string) []byte {
	mac := hmac.New(sha256.New, codec.secret)
	_, _ = mac.Write([]byte(payloadPart))
	return mac.Sum(nil)
}

func decodeCanonicalBase64(value string) ([]byte, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil, err
	}
	if base64.RawURLEncoding.EncodeToString(decoded) != value {
		return nil, errors.New("non-canonical base64url")
	}
	return decoded, nil
}

func filterHash(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode cursor filter context: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}
