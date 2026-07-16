package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewServerReturnsHTTPHandler(t *testing.T) {
	t.Parallel()

	var handler http.Handler = NewServer(Dependencies{})
	if handler == nil {
		t.Fatal("NewServer returned a nil handler")
	}
}

func TestHealth(t *testing.T) {
	t.Parallel()

	response := serve(t, http.MethodGet, "/health", "")

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if contentType := response.Header().Get("Content-Type"); contentType != "application/json" {
		t.Fatalf("Content-Type = %q, want %q", contentType, "application/json")
	}
	if requestID := response.Header().Get("X-Request-ID"); requestID == "" {
		t.Fatal("X-Request-ID is empty")
	}

	var body struct {
		Status  string `json:"status"`
		Service string `json:"service"`
	}
	decodeJSON(t, response, &body)

	if body.Status != "ok" {
		t.Errorf("status = %q, want %q", body.Status, "ok")
	}
	if body.Service != "paper-hub-api" {
		t.Errorf("service = %q, want %q", body.Service, "paper-hub-api")
	}
}

func TestRequestIDReusesNonBlankIncomingValue(t *testing.T) {
	t.Parallel()

	const incomingRequestID = "request-from-client"
	response := serve(t, http.MethodGet, "/health", incomingRequestID)

	if requestID := response.Header().Get("X-Request-ID"); requestID != incomingRequestID {
		t.Fatalf("X-Request-ID = %q, want %q", requestID, incomingRequestID)
	}
}

func TestRequestIDDoesNotReuseBlankIncomingValue(t *testing.T) {
	t.Parallel()

	response := serve(t, http.MethodGet, "/health", " \t ")

	requestID := response.Header().Get("X-Request-ID")
	if strings.TrimSpace(requestID) == "" {
		t.Fatal("X-Request-ID is blank")
	}
	if requestID == " \t " {
		t.Fatal("blank incoming X-Request-ID was reused")
	}
}

func TestUnmatchedAPIRouteReturnsProblemDetails(t *testing.T) {
	t.Parallel()

	response := serve(t, http.MethodGet, "/api/v1/not-a-real-resource", "")

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNotFound)
	}
	if contentType := response.Header().Get("Content-Type"); contentType != "application/problem+json" {
		t.Fatalf("Content-Type = %q, want %q", contentType, "application/problem+json")
	}

	var problem struct {
		Type      string `json:"type"`
		Title     string `json:"title"`
		Status    int    `json:"status"`
		Detail    string `json:"detail"`
		Instance  string `json:"instance"`
		RequestID string `json:"request_id"`
	}
	decodeJSON(t, response, &problem)

	if problem.Type == "" {
		t.Error("type is empty")
	}
	if problem.Title != "Not Found" {
		t.Errorf("title = %q, want %q", problem.Title, "Not Found")
	}
	if problem.Status != http.StatusNotFound {
		t.Errorf("status = %d, want %d", problem.Status, http.StatusNotFound)
	}
	if problem.Detail != "The requested API resource was not found." {
		t.Errorf("detail = %q, want generic not-found detail", problem.Detail)
	}
	if problem.Instance != "/api/v1/not-a-real-resource" {
		t.Errorf("instance = %q, want request path", problem.Instance)
	}
	if problem.RequestID == "" {
		t.Error("request_id is empty")
	}
	if headerRequestID := response.Header().Get("X-Request-ID"); problem.RequestID != headerRequestID {
		t.Errorf("request_id = %q, want response header value %q", problem.RequestID, headerRequestID)
	}

	body := response.Body.String()
	for _, internalMarker := range []string{"panic", "stack trace", "goroutine", "runtime/"} {
		if strings.Contains(strings.ToLower(body), internalMarker) {
			t.Errorf("problem response exposes internal marker %q: %s", internalMarker, body)
		}
	}
}

func TestAPIRootReturnsProblemDetailsWithoutRedirect(t *testing.T) {
	t.Parallel()

	response := serve(t, http.MethodGet, "/api", "")

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNotFound)
	}
	if contentType := response.Header().Get("Content-Type"); contentType != "application/problem+json" {
		t.Fatalf("Content-Type = %q, want %q", contentType, "application/problem+json")
	}

	var problem struct {
		Type      string `json:"type"`
		Title     string `json:"title"`
		Status    int    `json:"status"`
		Detail    string `json:"detail"`
		Instance  string `json:"instance"`
		RequestID string `json:"request_id"`
	}
	decodeJSON(t, response, &problem)

	if problem.Type == "" {
		t.Error("type is empty")
	}
	if problem.Title != "Not Found" {
		t.Errorf("title = %q, want %q", problem.Title, "Not Found")
	}
	if problem.Status != http.StatusNotFound {
		t.Errorf("status = %d, want %d", problem.Status, http.StatusNotFound)
	}
	if problem.Detail != "The requested API resource was not found." {
		t.Errorf("detail = %q, want generic not-found detail", problem.Detail)
	}
	if problem.Instance != "/api" {
		t.Errorf("instance = %q, want %q", problem.Instance, "/api")
	}
	if problem.RequestID == "" {
		t.Error("request_id is empty")
	}
	if headerRequestID := response.Header().Get("X-Request-ID"); problem.RequestID != headerRequestID {
		t.Errorf("request_id = %q, want response header value %q", problem.RequestID, headerRequestID)
	}
}

func serve(t *testing.T, method, target, requestID string) *httptest.ResponseRecorder {
	t.Helper()

	request := httptest.NewRequest(method, target, nil)
	if requestID != "" {
		request.Header.Set("X-Request-ID", requestID)
	}
	response := httptest.NewRecorder()

	NewServer(Dependencies{}).ServeHTTP(response, request)

	return response
}

func decodeJSON(t *testing.T, response *httptest.ResponseRecorder, destination any) {
	t.Helper()

	decoder := json.NewDecoder(response.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		t.Fatalf("decode response JSON: %v", err)
	}
}
