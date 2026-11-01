package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// The identity check of the sign-up (HU-M1.1-01) has two steps, like a real
// provider: read the document, so the person confirms the data printed on it,
// then verify the person with the data they confirmed.
//
// The outcome of a verification is decided by the LAST DIGIT of the document
// number, so every branch of the sign-up can be shown in a demo by typing the
// number:
//
//	…0 → rejected, the person is on a restrictive list
//	…1 → rejected, the document is not readable
//	…2 → rejected, the identity cannot be verified
//	any other → approved
//
// A read never produces a number ending in 0, 1 or 2: the data the stub reads
// from a real photo always verifies, and a rejection needs the number edited
// on purpose.
const (
	outcomeApproved = "approved"
	outcomeRejected = "rejected"

	reasonRestrictedList = "restricted_list"
	reasonUnreadable     = "unreadable"
	reasonUnverifiable   = "unverifiable"

	// maxIdentityBodyBytes bounds a read: two faces of up to 1.5 MB each,
	// in base64.
	maxIdentityBodyBytes = 5 << 20
	maxFieldLength       = 256
)

type documentReadRequest struct {
	DocumentFront string `json:"document_front"`
	DocumentBack  string `json:"document_back"`
}

type documentRead struct {
	ReadID       string `json:"read_id"`
	DocumentType string `json:"document_type"`
	DocumentID   string `json:"document_id"`
	GivenNames   string `json:"given_names"`
	Surnames     string `json:"surnames"`
	BirthDate    string `json:"birth_date"`
}

type verificationRequest struct {
	DocumentID          string `json:"document_id"`
	FullName            string `json:"full_name"`
	BirthDate           string `json:"birth_date"`
	DocumentFrontSHA256 string `json:"document_front_sha256"`
	DocumentBackSHA256  string `json:"document_back_sha256"`
}

type verification struct {
	VerificationID string    `json:"verification_id"`
	Outcome        string    `json:"outcome"`
	Reason         string    `json:"reason,omitempty"`
	CheckedAt      time.Time `json:"checked_at"`
}

var (
	sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)
	birthDate = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
)

func addIdentityRoutes(mux *http.ServeMux, delay time.Duration) {
	mux.HandleFunc("POST /documents/reads", func(w http.ResponseWriter, r *http.Request) {
		sleep(delay)
		var in documentReadRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxIdentityBodyBytes)).Decode(&in); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
			return
		}
		front, errFront := base64.StdEncoding.DecodeString(in.DocumentFront)
		_, errBack := base64.StdEncoding.DecodeString(in.DocumentBack)
		if errFront != nil || errBack != nil || len(front) == 0 || in.DocumentBack == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request",
				"message": "document_front and document_back are required, as base64"})
			return
		}
		read := readDocument(front)
		log.Printf("document read read_id=%s document_id=%s", read.ReadID, read.DocumentID)
		writeJSON(w, http.StatusOK, read)
	})

	mux.HandleFunc("POST /verifications", func(w http.ResponseWriter, r *http.Request) {
		sleep(delay)
		var in verificationRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14)).Decode(&in); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
			return
		}
		if !validVerification(in) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request",
				"message": "document_id, full_name, birth_date (YYYY-MM-DD) and the SHA-256 of both faces are required"})
			return
		}
		v := verify(in, time.Now().UTC())
		log.Printf("verification verification_id=%s outcome=%s reason=%s", v.VerificationID, v.Outcome, v.Reason)
		writeJSON(w, http.StatusOK, v)
	})
}

// readDocument derives the data printed on the document from the image, so
// the same photo always reads the same: a real provider would read the text.
func readDocument(front []byte) documentRead {
	sum := sha256.Sum256(front)
	n := binary.BigEndian.Uint64(sum[:8])

	firstNames := []string{"Carolina", "Andrés Felipe", "Luisa Fernanda", "Mateo", "Valentina", "Santiago"}
	lastNames := []string{"Pérez Ruiz", "Gómez Lara", "Rojas Mejía", "Castro Díaz", "Moreno Ortiz", "Vargas Silva"}

	// Nine digits and a last one between 3 and 9: never a rejecting ending.
	document := fmt.Sprintf("%09d%d", n%1_000_000_000, 3+n/1_000_000_000%7)
	year := 1960 + n/7%45
	month := 1 + n/11%12
	day := 1 + n/13%28
	return documentRead{
		ReadID:       fmt.Sprintf("read-%x", sum[:6]),
		DocumentType: "CC",
		DocumentID:   document,
		GivenNames:   firstNames[n/17%uint64(len(firstNames))],
		Surnames:     lastNames[n/19%uint64(len(lastNames))],
		BirthDate:    fmt.Sprintf("%04d-%02d-%02d", year, month, day),
	}
}

// verify decides by the last digit of the document number (see the top of
// this file). The reason only reaches the platform, never the person.
func verify(in verificationRequest, now time.Time) verification {
	sum := sha256.Sum256([]byte(in.DocumentID + "|" + in.DocumentFrontSHA256))
	v := verification{VerificationID: fmt.Sprintf("kyc-%x", sum[:6]), Outcome: outcomeApproved, CheckedAt: now}
	switch in.DocumentID[len(in.DocumentID)-1] {
	case '0':
		v.Outcome, v.Reason = outcomeRejected, reasonRestrictedList
	case '1':
		v.Outcome, v.Reason = outcomeRejected, reasonUnreadable
	case '2':
		v.Outcome, v.Reason = outcomeRejected, reasonUnverifiable
	}
	return v
}

func validVerification(in verificationRequest) bool {
	doc := strings.TrimSpace(in.DocumentID)
	return doc != "" && len(doc) <= maxFieldLength && strings.TrimSpace(in.FullName) != "" &&
		len(in.FullName) <= maxFieldLength && birthDate.MatchString(in.BirthDate) &&
		sha256Hex.MatchString(in.DocumentFrontSHA256) && sha256Hex.MatchString(in.DocumentBackSHA256)
}

func sleep(delay time.Duration) {
	if delay > 0 {
		time.Sleep(delay)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("could not write the response: %v", err)
	}
}
