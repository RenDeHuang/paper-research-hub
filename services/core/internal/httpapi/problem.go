package httpapi

import (
	"encoding/json"
	"net/http"
)

type problemDetails struct {
	Type      string `json:"type"`
	Title     string `json:"title"`
	Status    int    `json:"status"`
	Detail    string `json:"detail"`
	Instance  string `json:"instance"`
	RequestID string `json:"request_id"`
}

func writeProblem(
	writer http.ResponseWriter,
	request *http.Request,
	status int,
	problemType string,
	title string,
	detail string,
) {
	writer.Header().Set("Content-Type", "application/problem+json")
	writer.WriteHeader(status)

	_ = json.NewEncoder(writer).Encode(problemDetails{
		Type:      problemType,
		Title:     title,
		Status:    status,
		Detail:    detail,
		Instance:  request.URL.Path,
		RequestID: requestIDFromContext(request.Context()),
	})
}
