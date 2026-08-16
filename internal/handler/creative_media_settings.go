package handler

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"sub2api-ext/internal/creative"
)

func creativeMediaPayload(rt creative.MediaRuntime, svc *creative.Service) map[string]any {
	out := map[string]any{
		"driver":              rt.Driver,
		"local_root":          rt.LocalVideoRoot,
		"webdav_url":          rt.WebDAVURL,
		"webdav_username":     rt.WebDAVUsername,
		"webdav_root":         rt.WebDAVRoot,
		"local_fallback":      rt.LocalFallback,
		"password_configured": strings.TrimSpace(rt.WebDAVPassword) != "",
		"password_masked":     creative.MaskMediaSecret(rt.WebDAVPassword),
		"driver_options":      creative.MediaDriverOptions(),
	}
	if svc != nil {
		if h := svc.LastMediaHealth(); h != nil {
			out["health"] = h
		}
		if a := svc.LastMediaAudit(); a != nil {
			out["audit_summary"] = map[string]any{
				"scanned":       a.Scanned,
				"present":       a.Present,
				"missing":       a.Missing,
				"size_mismatch": a.SizeMismatch,
				"checked_at":    a.CheckedAt,
				"duration_ms":   a.DurationMS,
				"source":        a.Source,
				"health_ok":     a.Health.OK,
			}
		}
	}
	return out
}

// AdminCreativeMediaSettings GET/PUT /api/admin/creative/media-settings
func (h *Handler) AdminCreativeMediaSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if !h.limitAdminRead.Allow("AdminCreativeMediaSettings:" + clientIP(r)) {
			writeErr(w, http.StatusTooManyRequests, "rate limited")
			return
		}
		if _, err := h.requireAdmin(r); err != nil {
			writeErr(w, http.StatusUnauthorized, err.Error())
			return
		}
		if h.creative == nil {
			writeErr(w, http.StatusServiceUnavailable, "creative module unavailable")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"settings": creativeMediaPayload(h.creative.MediaRuntime(), h.creative)})
	case http.MethodPut, http.MethodPost:
		if !h.limitAdminWrite.Allow("AdminCreativeMediaSettings:" + clientIP(r)) {
			writeErr(w, http.StatusTooManyRequests, "rate limited")
			return
		}
		if _, err := h.requireAdmin(r); err != nil {
			writeErr(w, http.StatusUnauthorized, err.Error())
			return
		}
		if h.creative == nil {
			writeErr(w, http.StatusServiceUnavailable, "creative module unavailable")
			return
		}
		var in creative.MediaUpdateInput
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
			return
		}
		rt, err := h.creative.UpdateMediaConfig(r.Context(), in)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		// refresh health after successful save
		health, _ := h.creative.CheckMediaHealth(r.Context())
		writeJSON(w, http.StatusOK, map[string]any{
			"settings": creativeMediaPayload(rt, h.creative),
			"health":   health,
			"message":  "媒体存储配置已更新",
		})
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// AdminCreativeMediaSettingsTest POST /api/admin/creative/media-settings/test
func (h *Handler) AdminCreativeMediaSettingsTest(w http.ResponseWriter, r *http.Request) {
	if !h.limitAdminWrite.Allow("AdminCreativeMediaSettingsTest:" + clientIP(r)) {
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
	if h.creative == nil {
		writeErr(w, http.StatusServiceUnavailable, "creative module unavailable")
		return
	}
	var in creative.MediaUpdateInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil && err != io.EOF {
		writeErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	// If body empty/partial, test current applied config via health; else test form payload.
	if in.Driver == nil && in.WebDAVURL == nil && in.WebDAVUsername == nil && in.WebDAVPassword == nil && in.WebDAVRoot == nil && in.LocalFallback == nil && in.PasswordClear == nil {
		health, err := h.creative.CheckMediaHealth(r.Context())
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "health": health, "message": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "health": health, "message": health.Message})
		return
	}
	if err := h.creative.TestMediaConfig(r.Context(), in); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "媒体存储连通性正常"})
}

// AdminCreativeMediaHealth GET/POST /api/admin/creative/media-settings/health
func (h *Handler) AdminCreativeMediaHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if r.Method == http.MethodGet {
		if !h.limitAdminRead.Allow("AdminCreativeMediaHealth:" + clientIP(r)) {
			writeErr(w, http.StatusTooManyRequests, "rate limited")
			return
		}
	} else if !h.limitAdminWrite.Allow("AdminCreativeMediaHealth:" + clientIP(r)) {
		writeErr(w, http.StatusTooManyRequests, "rate limited")
		return
	}
	if _, err := h.requireAdmin(r); err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	if h.creative == nil {
		writeErr(w, http.StatusServiceUnavailable, "creative module unavailable")
		return
	}
	if r.Method == http.MethodGet {
		if h := h.creative.LastMediaHealth(); h != nil {
			writeJSON(w, http.StatusOK, map[string]any{"health": h, "cached": true})
			return
		}
	}
	health, err := h.creative.CheckMediaHealth(r.Context())
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "health": health, "message": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "health": health, "message": health.Message})
}

// AdminCreativeMediaAudit GET/POST /api/admin/creative/media-settings/audit
// GET returns last cached audit; POST runs a live scan.
func (h *Handler) AdminCreativeMediaAudit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if r.Method == http.MethodGet {
		if !h.limitAdminRead.Allow("AdminCreativeMediaAudit:" + clientIP(r)) {
			writeErr(w, http.StatusTooManyRequests, "rate limited")
			return
		}
	} else if !h.limitAdminWrite.Allow("AdminCreativeMediaAudit:" + clientIP(r)) {
		writeErr(w, http.StatusTooManyRequests, "rate limited")
		return
	}
	if _, err := h.requireAdmin(r); err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	if h.creative == nil {
		writeErr(w, http.StatusServiceUnavailable, "creative module unavailable")
		return
	}
	if r.Method == http.MethodGet {
		if a := h.creative.LastMediaAudit(); a != nil {
			writeJSON(w, http.StatusOK, map[string]any{"audit": a, "cached": true})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"audit": nil, "cached": true, "message": "尚未执行过巡检"})
		return
	}
	report, err := h.creative.AuditMissingMedia(r.Context())
	if err != nil && report.Scanned == 0 && !report.Health.OK {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "audit": report, "message": err.Error()})
		return
	}
	msg := "巡检完成"
	if report.Missing > 0 {
		msg = "巡检完成，发现缺失媒体"
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": report.Missing == 0 && report.Health.OK, "audit": report, "message": msg})
}
