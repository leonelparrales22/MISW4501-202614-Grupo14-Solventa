package httpadapter

import (
	"errors"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/evaluation"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// The queue by priority (HU-W2.2-02, screen O5): GET /v1/claims/evaluations
// ordered by Triage 1, 2 and 3 and the time waited, with the indicators of the
// queue and the insured of each claim.

type queueHolderBody struct {
	FullName     string `json:"full_name"`
	DocumentType string `json:"document_type"`
	DocumentID   string `json:"document_id"`
}

type priorityItemBody struct {
	evaluationItemBody
	Holder *queueHolderBody `json:"holder"`
}

type levelCountBody struct {
	Level  int `json:"level"`
	Claims int `json:"claims"`
}

type queueSummaryBody struct {
	Total      int              `json:"total"`
	Matching   int              `json:"matching"`
	Levels     []levelCountBody `json:"levels"`
	Unassessed int              `json:"unassessed"`
	// MeanResolutionDays is null when no claim was decided in the window.
	MeanResolutionDays *float64 `json:"mean_resolution_days"`
	DecidedInWindow    int      `json:"decided_in_window"`
	WindowDays         int      `json:"window_days"`
}

type priorityQueueBody struct {
	Items         []priorityItemBody `json:"items"`
	NextPageToken *string            `json:"next_page_token"`
	Summary       queueSummaryBody   `json:"summary"`
}

// queueFilterOf reads the filters of the query: level (1, 2, 3 or none),
// line (auto or home) and status (one of the queue).
func queueFilterOf(req *http.Request) (evaluation.QueueFilter, bool) {
	query := req.URL.Query()
	var f evaluation.QueueFilter
	switch level := query.Get("level"); level {
	case "":
	case "none":
		f.Level = ports.RankUnassessed
	case "1", "2", "3":
		f.Level, _ = strconv.Atoi(level)
	default:
		return f, false
	}
	switch line := domain.PolicyLine(query.Get("line")); line {
	case "", domain.LineAuto, domain.LineHome:
		f.Line = line
	default:
		return f, false
	}
	if status := domain.Status(query.Get("status")); status != "" {
		if !domain.Triageable(status) {
			return f, false
		}
		f.Status = status
	}
	return f, true
}

func priorityQueue(svc EvaluationReader) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		size := 0
		if raw := req.URL.Query().Get("page_size"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 1 {
				writeError(w, http.StatusBadRequest, "invalid_request", "page_size must be between 1 and 50")
				return
			}
			size = n
		}
		token := req.URL.Query().Get("page_token")
		f, ok := queueFilterOf(req)
		if !ok || len(token) > 512 {
			writeError(w, http.StatusBadRequest, "invalid_request", "a filter or the page token is not valid")
			return
		}
		page, err := svc.PriorityQueue(req.Context(), size, token, f)
		switch {
		case errors.Is(err, evaluation.ErrInvalidQuery):
			writeError(w, http.StatusBadRequest, "invalid_request", "page_size must be between 1 and 50, the filters and the page token valid")
			return
		case err != nil:
			slog.ErrorContext(req.Context(), "could not list the queue by priority", "error", err)
			writeError(w, http.StatusServiceUnavailable, "claims_unavailable", "the claims could not be read; retry")
			return
		}
		writeJSON(w, http.StatusOK, priorityQueueBodyOf(page))
	}
}

func priorityQueueBodyOf(page evaluation.PriorityPage) priorityQueueBody {
	out := priorityQueueBody{Items: make([]priorityItemBody, 0, len(page.Items))}
	for _, item := range page.Items {
		body := priorityItemBody{evaluationItemBody: evaluationItemBody{
			claimSummaryBody:     summaryBody(item.Claim),
			EstimatedAmountMinor: item.Claim.EstimatedAmountMinor,
			Triage:               summaryOfTriage(item.Claim.ID, item.Triage.State, item.Triage.Current),
		}}
		if h := item.Holder; h != nil {
			body.Holder = &queueHolderBody{FullName: h.FullName, DocumentType: h.DocumentType, DocumentID: h.DocumentID}
		}
		out.Items = append(out.Items, body)
	}
	if page.NextPageToken != "" {
		out.NextPageToken = &page.NextPageToken
	}
	s := page.Summary
	out.Summary = queueSummaryBody{Total: s.Total, Matching: s.Matching, Unassessed: s.Unassessed,
		DecidedInWindow: s.Decided, WindowDays: int(evaluation.ResolutionWindow / (24 * time.Hour))}
	for level := 1; level <= 3; level++ {
		out.Summary.Levels = append(out.Summary.Levels, levelCountBody{Level: level, Claims: s.ByLevel[level]})
	}
	if m := s.MeanResolution; m != nil {
		days := math.Round(m.Hours()/24*10) / 10
		out.Summary.MeanResolutionDays = &days
	}
	return out
}
