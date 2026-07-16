package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,255}$`)

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

func TestRequestIDAccepts255AllowedCharacters(t *testing.T) {
	t.Parallel()

	requestID := strings.Repeat("A", 255)
	response := serve(t, http.MethodGet, "/health", requestID)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if actual := response.Header().Get("X-Request-ID"); actual != requestID {
		t.Fatalf("X-Request-ID = %q, want 255-character client value", actual)
	}
}

func TestRequestIDRejects256Characters(t *testing.T) {
	t.Parallel()

	assertInvalidRequestID(t, strings.Repeat("A", 256))
}

func TestRequestIDRejectsBlankOrIllegalCharacters(t *testing.T) {
	t.Parallel()

	for _, requestID := range []string{
		" \t ",
		"request id",
		"request/id",
		"请求",
	} {
		requestID := requestID
		t.Run(requestID, func(t *testing.T) {
			t.Parallel()
			assertInvalidRequestID(t, requestID)
		})
	}
}

func TestRequestIDRejectsPresentEmptyHeader(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest(http.MethodGet, "/health", nil)
	request.Header["X-Request-ID"] = []string{""}
	response := httptest.NewRecorder()

	NewServer(Dependencies{}).ServeHTTP(response, request)

	assertInvalidRequestIDResponse(t, response, "")
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

func assertInvalidRequestID(t *testing.T, incomingRequestID string) {
	t.Helper()

	response := serve(t, http.MethodGet, "/health", incomingRequestID)
	assertInvalidRequestIDResponse(t, response, incomingRequestID)
}

func assertInvalidRequestIDResponse(
	t *testing.T,
	response *httptest.ResponseRecorder,
	incomingRequestID string,
) {
	t.Helper()

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
	}
	if contentType := response.Header().Get("Content-Type"); contentType != "application/problem+json" {
		t.Fatalf("Content-Type = %q, want %q", contentType, "application/problem+json")
	}

	responseRequestID := response.Header().Get("X-Request-ID")
	if !requestIDPattern.MatchString(responseRequestID) {
		t.Fatalf("response X-Request-ID = %q, want pattern %s", responseRequestID, requestIDPattern)
	}
	if responseRequestID == incomingRequestID {
		t.Fatalf("response reused invalid client X-Request-ID %q", incomingRequestID)
	}

	var problem problemDetails
	decodeJSON(t, response, &problem)
	if problem.Type != "urn:paper-hub:problem:invalid-request-id" {
		t.Errorf("type = %q, want invalid-request-id problem", problem.Type)
	}
	if problem.Title != "Bad Request" {
		t.Errorf("title = %q, want %q", problem.Title, "Bad Request")
	}
	if problem.Status != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", problem.Status, http.StatusBadRequest)
	}
	if problem.Detail != "X-Request-ID must match [A-Za-z0-9._:-]{1,255}." {
		t.Errorf("detail = %q, want Request ID format requirement", problem.Detail)
	}
	if problem.Instance != "/health" {
		t.Errorf("instance = %q, want %q", problem.Instance, "/health")
	}
	if problem.RequestID != responseRequestID {
		t.Errorf("request_id = %q, want response header value %q", problem.RequestID, responseRequestID)
	}
}

func decodeJSON(t *testing.T, response *httptest.ResponseRecorder, destination any) {
	t.Helper()

	decoder := json.NewDecoder(response.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		t.Fatalf("decode response JSON: %v", err)
	}
}
