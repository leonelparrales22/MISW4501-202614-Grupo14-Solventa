package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

// The document check of an endorsement (HU-W1.4-02, decision D-5 a): before an
// operations analyst applies new beneficiaries, origination asks whether the
// identity document of each new one is valid in the national registry
// (Registraduría). A real provider answers from the registry; the stub decides
// by the number, so both outcomes can be shown in a demo by typing it:
//
//	a number ending in 000 → not_found, the document is not in the registry
//	any other              → valid
//
// It knows nothing about who asks: the answer depends only on the document.
const (
	documentValid    = "valid"
	documentNotFound = "not_found"

	maxDocumentCheckBodyBytes = 1 << 12
	maxDocumentNumberLength   = 32
)

// documentTypes are the identity documents the registry answers for: the
// Colombian ones and the passport, the catalog of the change requests of
// HU-W1.4-01.
var documentTypes = map[string]bool{"CC": true, "TI": true, "RC": true, "CE": true, "PA": true}

type documentCheckRequest struct {
	DocumentType   string `json:"document_type"`
	DocumentNumber string `json:"document_number"`
	FullName       string `json:"full_name"`
}

type documentCheck struct {
	CheckID   string    `json:"check_id"`
	Status    string    `json:"status"`
	CheckedAt time.Time `json:"checked_at"`
}

func addDocumentCheckRoutes(mux *http.ServeMux, delay time.Duration) {
	mux.HandleFunc("POST /document-checks", func(w http.ResponseWriter, r *http.Request) {
		sleep(delay)
		var in documentCheckRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxDocumentCheckBodyBytes)).Decode(&in); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
			return
		}
		if !validDocumentCheck(in) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request",
				"message": "document_type (CC, TI, RC, CE or PA), document_number and full_name are required"})
			return
		}
		c := checkDocument(in, time.Now().UTC())
		// One line per check: the evidence of the validation in the logs. The
		// number and the name of the person never reach the log.
		log.Printf("document check check_id=%s document_type=%s status=%s", c.CheckID, in.DocumentType, c.Status)
		writeJSON(w, http.StatusOK, c)
	})
}

// checkDocument decides by the end of the number (see the top of this file).
// The same document always gets the same check id.
func checkDocument(in documentCheckRequest, now time.Time) documentCheck {
	number := strings.TrimSpace(in.DocumentNumber)
	sum := sha256.Sum256([]byte(in.DocumentType + "|" + strings.ToUpper(number)))
	c := documentCheck{CheckID: fmt.Sprintf("doc-%x", sum[:6]), Status: documentValid, CheckedAt: now}
	if strings.HasSuffix(number, "000") {
		c.Status = documentNotFound
	}
	return c
}

func validDocumentCheck(in documentCheckRequest) bool {
	number := strings.TrimSpace(in.DocumentNumber)
	name := strings.TrimSpace(in.FullName)
	return documentTypes[in.DocumentType] && number != "" && len(number) <= maxDocumentNumberLength &&
		name != "" && len(name) <= maxFieldLength
}
