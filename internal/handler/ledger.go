package handler

import (
	"net/http"
	"strconv"
	"strings"
	"time"

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
		created := ""
		if !e.CreatedAt.IsZero() {
			created = e.CreatedAt.UTC().Format("2006-01-02 15:04:05")
		}
		items = append(items, map[string]any{
			"id": e.ID, "user_id": e.UserID, "source": e.Source, "source_ref": e.SourceRef,
			"amount": e.Amount, "idempotency_key": e.IdempotencyKey, "status": e.Status,
			"notes": e.Notes, "error": e.Error, "created_at": created,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
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
	from := strings.TrimSpace(q.Get("from"))
	to := strings.TrimSpace(q.Get("to"))
	// Default window: last 7 local days inclusive, so the admin page is not flooded.
	if from == "" && to == "" {
		loc := h.settings.Location()
		now := time.Now().In(loc)
		to = now.Format("2006-01-02")
		from = now.AddDate(0, 0, -6).Format("2006-01-02")
	}
	stats, err := h.store.LedgerStatsBySource(r.Context(), from, to)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	bySource := map[string]map[string]any{}
	totalCount := int64(0)
	totalAmount := 0.0
	for _, s := range stats {
		totalCount += s.Count
		totalAmount += s.Amount
		cur, ok := bySource[s.Source]
		if !ok {
			cur = map[string]any{"source": s.Source, "count": int64(0), "amount": 0.0}
			bySource[s.Source] = cur
		}
		cur["count"] = cur["count"].(int64) + s.Count
		cur["amount"] = cur["amount"].(float64) + s.Amount
	}
	// stable-ish order
	order := []string{
		store.LedgerSourceCheckin, store.LedgerSourceLottery,
		store.LedgerSourceRankReward, store.LedgerSourceTask,
		store.LedgerSourceBackfill, store.LedgerSourceManual,
	}
	summary := make([]map[string]any, 0, len(bySource))
	seen := map[string]bool{}
	for _, src := range order {
		if cur, ok := bySource[src]; ok {
			summary = append(summary, cur)
			seen[src] = true
		}
	}
	for src, cur := range bySource {
		if !seen[src] {
			summary = append(summary, cur)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"from": from, "to": to,
		"total_count": totalCount, "total_amount": totalAmount,
		"by_source": summary,
		"stats":     stats,
	})
}
