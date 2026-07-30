package handler

import (
	"net/http"
	"strconv"
	"strings"

	"sub2api-ext/internal/store"
)

// AdminListLedger GET /api/admin/ledger
func (h *Handler) AdminListLedger(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if _, err := h.requireAdmin(r); err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	if !h.limitAdminRead.Allow("AdminListLedger:" + clientIP(r)) {
		writeErr(w, http.StatusTooManyRequests, "rate limited")
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	uid, _ := strconv.ParseInt(q.Get("user_id"), 10, 64)
	list, err := h.store.ListLedger(r.Context(), store.LedgerFilter{
		Source: strings.TrimSpace(q.Get("source")),
		UserID: uid,
		From:   strings.TrimSpace(q.Get("from")),
		To:     strings.TrimSpace(q.Get("to")),
		Limit:  limit,
		Offset: offset,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]map[string]any, 0, len(list))
	for _, e := range list {
		items = append(items, map[string]any{
			"id": e.ID, "user_id": e.UserID, "source": e.Source, "source_ref": e.SourceRef,
			"amount": e.Amount, "idempotency_key": e.IdempotencyKey, "status": e.Status,
			"notes": e.Notes, "error": e.Error, "created_at": e.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// AdminLedgerStats GET /api/admin/ledger/stats
func (h *Handler) AdminLedgerStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if _, err := h.requireAdmin(r); err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	q := r.URL.Query()
	stats, err := h.store.LedgerStatsBySource(r.Context(), q.Get("from"), q.Get("to"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"stats": stats})
}
