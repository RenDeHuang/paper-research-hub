package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/catalog"
)

const requestIDHeader = "X-Request-ID"

type requestIDContextKey struct{}

type Dependencies struct {
	Catalog            *catalog.Repository
	CORSAllowedOrigins []string
}

func NewServer(dependencies Dependencies) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", getOnly(health))
	registerCatalogRoutes(mux, dependencies.Catalog)
	mux.HandleFunc("/", exactRoot(getOnly(discovery)))
	mux.HandleFunc("/api", apiNotFound)
	mux.HandleFunc("/api/", apiNotFound)

	return corsMiddleware(dependencies.CORSAllowedOrigins, requestIDMiddleware(mux))
}

func corsMiddleware(allowedOrigins []string, next http.Handler) http.Handler {
	allowed := make(map[string]struct{}, len(allowedOrigins))
	for _, origin := range allowedOrigins {
		allowed[origin] = struct{}{}
	}

	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		origin := request.Header.Get("Origin")
		if origin == "" {
			next.ServeHTTP(writer, request)
			return
		}

		writer.Header().Add("Vary", "Origin")
		if _, ok := allowed[origin]; !ok {
			next.ServeHTTP(writer, request)
			return
		}

		writer.Header().Set("Access-Control-Allow-Origin", origin)
		writer.Header().Set(
			"Access-Control-Expose-Headers",
			requestIDHeader+", "+catalogGenerationHeader,
		)

		if request.Method == http.MethodOptions &&
			request.Header.Get("Access-Control-Request-Method") != "" {
			writer.Header().Add("Vary", "Access-Control-Request-Method")
			writer.Header().Add("Vary", "Access-Control-Request-Headers")
			writer.Header().Set("Access-Control-Allow-Methods", http.MethodGet)
			writer.Header().Set("Access-Control-Allow-Headers", requestIDHeader)
			writer.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(writer, request)
	})
}

func health(writer http.ResponseWriter, _ *http.Request) {
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(struct {
		Status  string `json:"status"`
		Service string `json:"service"`
	}{
		Status:  "ok",
		Service: "medpaperhub-api",
	})
}

func discovery(writer http.ResponseWriter, _ *http.Request) {
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(struct {
		Service    string `json:"service"`
		Status     string `json:"status"`
		APIVersion string `json:"api_version"`
		Health     string `json:"health"`
	}{
		Service:    "medpaperhub-api",
		Status:     "ok",
		APIVersion: "v1",
		Health:     "/health",
	})
}

func exactRoot(next http.HandlerFunc) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/" {
			http.NotFound(writer, request)
			return
		}
		next(writer, request)
	}
}

func apiNotFound(writer http.ResponseWriter, request *http.Request) {
	writeProblem(
		writer,
		request,
		http.StatusNotFound,
		"urn:paper-hub:problem:not-found",
		"Not Found",
		"route_not_found",
		"The requested API resource was not found.",
	)
}

func getOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			writer.Header().Set("Allow", http.MethodGet)
			writeProblem(
				writer,
				request,
				http.StatusMethodNotAllowed,
				"urn:paper-hub:problem:method-not-allowed",
				"Method Not Allowed",
				"method_not_allowed",
				"The requested resource only supports GET.",
			)
			return
		}
		next(writer, request)
	}
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
				"invalid_request_id",
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
