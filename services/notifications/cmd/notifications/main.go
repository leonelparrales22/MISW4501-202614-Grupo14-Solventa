// Package main is the entry point of the notifications service.
// For now it only exposes GET /health (ADR-02).
package main

import (
	"log"
	"net/http"
	"os"

	"github.com/go-chi/chi/v5"
)

func main() {
	r := chi.NewRouter()
	r.Get("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	addr := ":" + getenv("PORT", "8080")
	log.Printf("notifications listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, r))
}

func getenv(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
