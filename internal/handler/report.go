package handler

import (
	"encoding/json"
	"net/http"
	"time"

	"sub2api-ext/internal/report"
)

// reportPayload renders report settings for admin reads.
func reportPayload(rt report.Runtime, stats report.Stats) map[string]any {
	sections := make([]map[string]string, 0, len(report.AllSections()))
	for _, s := range report.AllSections() {
		sections = append(sections, map[string]string{"value": s, "label": report.SectionLabel(s)})
	}
	return map[string]any{
		"enabled":         rt.Enabled,
		"send_at":         rt.SendAt,
		"timezone":        rt.Timezone,
		"cover_day":       rt.CoverDay,
		"cover_label":     report.CoverLabel(rt.CoverDay),
		"sections":        rt.Sections,
		"section_options": sections,
		"cover_options": []map[string]string{
			{"value": report.CoverYesterday, "label": report.CoverLabel(report.CoverYesterday)},
			{"value": report.CoverToday, "label": report.CoverLabel(report.CoverToday)},
		},
		"stats": stats,
	}
}

// AdminGetReportSettings GET /api/admin/report/settings
func (h *Handler) AdminGetReportSettings(w http.ResponseWriter, r *http.Request) {
	if !h.limitAdminRead.Allow("AdminGetReportSettings:" + clientIP(r)) {
		writeErr(w, http.StatusTooManyRequests, "rate limited")
		return
	}
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if _, err := h.requireAdmin(r); err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	if h.report == nil {
		writeErr(w, http.StatusServiceUnavailable, "report module unavailable")
		return
	}
	rt := h.report.Settings().Get()
	writeJSON(w, http.StatusOK, map[string]any{"settings": reportPayload(rt, h.report.Stats())})
}

// AdminUpdateReportSettings PUT/POST /api/admin/report/settings
func (h *Handler) AdminUpdateReportSettings(w http.ResponseWriter, r *http.Request) {
	if !h.limitAdminWrite.Allow("AdminUpdateReportSettings:" + clientIP(r)) {
		writeErr(w, http.StatusTooManyRequests, "rate limited")
		return
	}
	if r.Method != http.MethodPut && r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if _, err := h.requireAdmin(r); err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	if h.report == nil {
		writeErr(w, http.StatusServiceUnavailable, "report module unavailable")
		return
	}
	var in report.UpdateInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	rt, err := h.report.Settings().Update(r.Context(), in)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"settings": reportPayload(rt, h.report.Stats()),
		"message":  "report settings updated",
	})
}

// AdminReportPreview GET /api/admin/report/preview
// Builds the digest without delivering it, so an operator can check the
// content and the numbers before turning the schedule on.
func (h *Handler) AdminReportPreview(w http.ResponseWriter, r *http.Request) {
	if !h.limitAdminRead.Allow("AdminReportPreview:" + clientIP(r)) {
		writeErr(w, http.StatusTooManyRequests, "rate limited")
		return
	}
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if _, err := h.requireAdmin(r); err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	if h.report == nil {
		writeErr(w, http.StatusServiceUnavailable, "report module unavailable")
		return
	}
	d, err := h.report.Preview(r.Context(), time.Now())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"digest":  d,
		"message": d.PlainText(),
	})
}

// AdminReportSend POST /api/admin/report/send
// Sends synchronously so the operator sees the real delivery error.
func (h *Handler) AdminReportSend(w http.ResponseWriter, r *http.Request) {
	if !h.limitAdminWrite.Allow("AdminReportSend:" + clientIP(r)) {
		writeErr(w, http.StatusTooManyRequests, "rate limited")
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if _, err := h.requireAdmin(r); err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	if h.report == nil {
		writeErr(w, http.StatusServiceUnavailable, "report module unavailable")
		return
	}
	d, err := h.report.SendNow(r.Context(), time.Now())
	if err != nil {
		writeErr(w, http.StatusBadGateway, "发送失败："+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"message": "日报已发送",
		"digest":  d,
		"stats":   h.report.Stats(),
	})
}
