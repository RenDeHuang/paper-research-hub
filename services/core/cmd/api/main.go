package main

import (
	"log"
	"net/http"
	"os"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/httpapi"
)

func main() {
	addr := os.Getenv("API_ADDR")
	if addr == "" {
		addr = ":8080"
	}

	log.Fatal(http.ListenAndServe(addr, httpapi.NewServer(httpapi.Dependencies{})))
}
