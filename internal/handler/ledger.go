package handler

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"sub2api-ext/internal/store"
)

func ledgerFilterFromRequest(r *http.Request) store.LedgerFilter {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	uid, _ := strconv.ParseInt(q.Get("user_id"), 10, 64)
	return store.LedgerFilter{
		Source: strings.TrimSpace(q.Get("source")),
		Status: strings.TrimSpace(q.Get("status")),
		UserID: uid,
		From:   strings.TrimSpace(q.Get("from")),
		To:     strings.TrimSpace(q.Get("to")),
		Limit:  limit,
		Offset: offset,
	}
}

func ledgerEntryJSON(e store.LedgerEntry) map[string]any {
	created := ""
	if !e.CreatedAt.IsZero() {
		created = e.CreatedAt.UTC().Format("2006-01-02 15:04:05")
	}
	return map[string]any{
		"id": e.ID, "user_id": e.UserID, "source": e.Source, "source_ref": e.SourceRef,
		"amount": e.Amount, "idempotency_key": e.IdempotencyKey, "status": e.Status,
		"notes": e.Notes, "error": e.Error, "created_at": created,
	}
}

func ledgerSourceLabel(src string) string {
	switch src {
	case store.LedgerSourceCheckin:
		return "签到"
	case store.LedgerSourceLottery:
		return "抽奖"
	case store.LedgerSourceRankReward:
		return "排行发奖"
	case store.LedgerSourceTask:
		return "任务"
	case store.LedgerSourceInactiveReclaim:
		return "闲置额度回收"
	case store.LedgerSourceRedistribution:
		return "活跃用户回流奖励"
	case store.LedgerSourceRedistributionCompensation:
		return "回流补偿"
	case store.LedgerSourceBackfill:
		return "回填"
	case store.LedgerSourceManual:
		return "手工"
	default:
		return src
	}
}

func ledgerStatusLabel(st string) string {
	switch st {
	case store.LedgerStatusSuccess:
		return "成功"
	case store.LedgerStatusFailed:
		return "失败"
	case store.LedgerStatusSkipped:
		return "跳过"
	default:
		return st
	}
}

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
	f := ledgerFilterFromRequest(r)
	if f.Limit <= 0 {
		f.Limit = 20
	}
	if f.Limit > 200 {
		f.Limit = 200
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	total, err := h.store.CountLedger(r.Context(), f)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	list, err := h.store.ListLedger(r.Context(), f)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]map[string]any, 0, len(list))
	for _, e := range list {
		items = append(items, ledgerEntryJSON(e))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items, "count": len(items),
		"total": total, "limit": f.Limit, "offset": f.Offset,
	})
}

// AdminExportLedger GET /api/admin/ledger/export
// Downloads filtered ledger rows as CSV (max 5000).
func (h *Handler) AdminExportLedger(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if _, err := h.requireAdmin(r); err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	if !h.limitAdminRead.Allow("AdminExportLedger:" + clientIP(r)) {
		writeErr(w, http.StatusTooManyRequests, "rate limited")
		return
	}
	f := ledgerFilterFromRequest(r)
	f.Offset = 0
	if f.Limit <= 0 || f.Limit > 5000 {
		f.Limit = 5000
	}
	list, err := h.store.ListLedger(r.Context(), f)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	name := fmt.Sprintf("ledger_%s.csv", time.Now().Format("20060102_150405"))
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	// UTF-8 BOM for Excel
	if _, err := w.Write([]byte{0xEF, 0xBB, 0xBF}); err != nil {
		return
	}
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"时间", "用户ID", "来源", "来源编码", "金额", "状态", "状态编码", "幂等Key", "来源引用", "备注", "错误"})
	for _, e := range list {
		created := ""
		if !e.CreatedAt.IsZero() {
			created = e.CreatedAt.UTC().Format("2006-01-02 15:04:05")
		}
		_ = cw.Write([]string{
			created,
			strconv.FormatInt(e.UserID, 10),
			ledgerSourceLabel(e.Source),
			e.Source,
			strconv.FormatFloat(e.Amount, 'f', 4, 64),
			ledgerStatusLabel(e.Status),
			e.Status,
			e.IdempotencyKey,
			e.SourceRef,
			e.Notes,
			e.Error,
		})
	}
	cw.Flush()
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
	order := []string{
		store.LedgerSourceCheckin, store.LedgerSourceLottery,
		store.LedgerSourceRankReward, store.LedgerSourceTask,
		store.LedgerSourceInactiveReclaim, store.LedgerSourceRedistribution,
		store.LedgerSourceRedistributionCompensation,
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

// MeLedger GET /api/me/ledger — current user's own reward ledger (read-only).
func (h *Handler) MeLedger(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.limitStatus.Allow("me-ledger:" + clientIP(r)) {
		writeErr(w, http.StatusTooManyRequests, "rate limited")
		return
	}
	token := extractToken(r)
	if token == "" {
		writeErr(w, http.StatusUnauthorized, "missing token")
		return
	}
	user, err := h.client.ResolveUser(r.Context(), token, clientMetaFromRequest(r))
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "invalid token: "+err.Error())
		return
	}

	f := ledgerFilterFromRequest(r)
	// Always pin to the authenticated user; ignore client-supplied user_id.
	f.UserID = user.ID
	if f.Limit <= 0 {
		f.Limit = 50
	}
	if f.Limit > 100 {
		f.Limit = 100
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	// Default window: last 30 local days inclusive.
	if f.From == "" && f.To == "" {
		loc := h.settings.Location()
		now := time.Now().In(loc)
		f.To = now.Format("2006-01-02")
		f.From = now.AddDate(0, 0, -29).Format("2006-01-02")
	}

	total, err := h.store.CountLedger(r.Context(), f)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	list, err := h.store.ListLedger(r.Context(), f)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	sum, err := h.store.SummarizeLedgerByStatus(r.Context(), f)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	items := make([]map[string]any, 0, len(list))
	for _, e := range list {
		row := ledgerEntryJSON(e)
		row["source_label"] = ledgerSourceLabel(e.Source)
		row["status_label"] = ledgerStatusLabel(e.Status)
		delete(row, "idempotency_key")
		items = append(items, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user_id": user.ID,
		"from":    f.From, "to": f.To,
		"items": items, "count": len(items),
		"total": total, "limit": f.Limit, "offset": f.Offset,
		"success_amount": sum.SuccessAmount,
		"success_count":  sum.SuccessCount,
		"failed_count":   sum.FailedCount,
	})
}
