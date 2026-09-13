package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
)

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /profiles/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/profiles/")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"customer_id": id, "risk_profile": "risk_profile_" + id})
	})
	log.Fatal(http.ListenAndServe(":8081", mux))
}
