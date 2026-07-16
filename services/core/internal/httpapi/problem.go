package httpapi

import (
	"encoding/json"
	"net/http"
)

type problemDetails struct {
	Type      string `json:"type"`
	Title     string `json:"title"`
	Status    int    `json:"status"`
	Code      string `json:"code"`
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
	code string,
	detail string,
) {
	writer.Header().Set("Content-Type", "application/problem+json")
	writer.WriteHeader(status)

	_ = json.NewEncoder(writer).Encode(problemDetails{
		Type:      problemType,
		Title:     title,
		Status:    status,
		Code:      code,
		Detail:    detail,
		Instance:  request.URL.Path,
		RequestID: requestIDFromContext(request.Context()),
	})
}
