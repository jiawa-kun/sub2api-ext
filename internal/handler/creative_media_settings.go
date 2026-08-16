package handler

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"sub2api-ext/internal/creative"
)

func creativeMediaPayload(rt creative.MediaRuntime) map[string]any {
	return map[string]any{
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
		writeJSON(w, http.StatusOK, map[string]any{"settings": creativeMediaPayload(h.creative.MediaRuntime())})
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
		writeJSON(w, http.StatusOK, map[string]any{
			"settings": creativeMediaPayload(rt),
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
	if err := h.creative.TestMediaConfig(r.Context(), in); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "媒体存储连通性正常"})
}
