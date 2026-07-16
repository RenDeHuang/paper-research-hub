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
		requestID := request.Header.Get(requestIDHeader)
		if strings.TrimSpace(requestID) == "" {
			requestID = newRequestID()
		}

		writer.Header().Set(requestIDHeader, requestID)
		ctx := context.WithValue(request.Context(), requestIDContextKey{}, requestID)
		next.ServeHTTP(writer, request.WithContext(ctx))
	})
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
