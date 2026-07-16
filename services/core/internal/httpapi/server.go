package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
)

const requestIDHeader = "X-Request-ID"

type requestIDContextKey struct{}

type Dependencies struct{}

func NewServer(_ Dependencies) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", health)
	mux.HandleFunc("/api", apiNotFound)
	mux.HandleFunc("/api/", apiNotFound)

	return requestIDMiddleware(mux)
}

func health(writer http.ResponseWriter, _ *http.Request) {
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(struct {
		Status  string `json:"status"`
		Service string `json:"service"`
	}{
		Status:  "ok",
		Service: "paper-hub-api",
	})
}

func apiNotFound(writer http.ResponseWriter, request *http.Request) {
	writeProblem(
		writer,
		request,
		http.StatusNotFound,
		"urn:paper-hub:problem:not-found",
		"Not Found",
		"The requested API resource was not found.",
	)
}

func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requestID, present := headerValue(request.Header, requestIDHeader)
		if !present {
			requestID = newRequestID()
		}

		writer.Header().Set(requestIDHeader, requestID)
		ctx := context.WithValue(request.Context(), requestIDContextKey{}, requestID)
		request = request.WithContext(ctx)

		if present && !validRequestID(requestID) {
			requestID = newRequestID()
			writer.Header().Set(requestIDHeader, requestID)
			request = request.WithContext(
				context.WithValue(request.Context(), requestIDContextKey{}, requestID),
			)
			writeProblem(
				writer,
				request,
				http.StatusBadRequest,
				"urn:paper-hub:problem:invalid-request-id",
				"Bad Request",
				"X-Request-ID must match [A-Za-z0-9._:-]{1,255}.",
			)
			return
		}

		next.ServeHTTP(writer, request)
	})
}

func headerValue(header http.Header, name string) (string, bool) {
	for headerName, values := range header {
		if !strings.EqualFold(headerName, name) {
			continue
		}
		if len(values) == 0 {
			return "", true
		}
		return values[0], true
	}

	return "", false
}

func validRequestID(requestID string) bool {
	if len(requestID) == 0 || len(requestID) > 255 {
		return false
	}

	for index := range len(requestID) {
		character := requestID[index]
		if (character >= 'A' && character <= 'Z') ||
			(character >= 'a' && character <= 'z') ||
			(character >= '0' && character <= '9') ||
			character == '.' ||
			character == '_' ||
			character == ':' ||
			character == '-' {
			continue
		}
		return false
	}

	return true
}

func newRequestID() string {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		panic("generate request ID: " + err.Error())
	}

	return hex.EncodeToString(random)
}

func requestIDFromContext(ctx context.Context) string {
	requestID, _ := ctx.Value(requestIDContextKey{}).(string)
	return requestID
}
