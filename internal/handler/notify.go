package handler

import (
	"encoding/json"
	"net/http"
	"time"

	"sub2api-ext/internal/notify"
)

// notifyPayload renders settings for admin reads with credentials masked.
func notifyPayload(rt notify.Runtime, stats notify.Stats) map[string]any {
	channels := make([]map[string]string, 0, 3)
	for _, c := range notify.SupportedChannels() {
		channels = append(channels, map[string]string{"value": c, "label": notify.ChannelLabel(c)})
	}
	events := make([]map[string]string, 0, 4)
	for _, e := range notify.AllTypes() {
		events = append(events, map[string]string{"value": e, "label": notify.TypeLabel(e)})
	}
	return map[string]any{
		"enabled":           rt.Enabled,
		"channel":           rt.Channel,
		"target_masked":     notify.MaskTarget(rt.Channel, rt.Target),
		"target_configured": rt.Target != "",
		"extra":             rt.Extra,
		"secret_masked":     notify.MaskSecret(rt.Secret),
		"secret_configured": rt.Secret != "",
		"events":            rt.Events,
		"min_level":         rt.MinLevel,
		"channel_options":   channels,
		"event_options":     events,
		"stats":             stats,
	}
}

// AdminGetNotifySettings GET /api/admin/notify/settings
func (h *Handler) AdminGetNotifySettings(w http.ResponseWriter, r *http.Request) {
	if !h.limitAdminRead.Allow("AdminGetNotifySettings:" + clientIP(r)) {
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
	if h.notifier == nil {
		writeErr(w, http.StatusServiceUnavailable, "notify module unavailable")
		return
	}
	rt := h.notifier.Settings().Get()
	writeJSON(w, http.StatusOK, map[string]any{"settings": notifyPayload(rt, h.notifier.Stats())})
}

// AdminUpdateNotifySettings PUT/POST /api/admin/notify/settings
func (h *Handler) AdminUpdateNotifySettings(w http.ResponseWriter, r *http.Request) {
	if !h.limitAdminWrite.Allow("AdminUpdateNotifySettings:" + clientIP(r)) {
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
	if h.notifier == nil {
		writeErr(w, http.StatusServiceUnavailable, "notify module unavailable")
		return
	}
	var in notify.UpdateInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	rt, err := h.notifier.Settings().Update(r.Context(), in)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"settings": notifyPayload(rt, h.notifier.Stats()),
		"message":  "notify settings updated",
	})
}

// AdminNotifyTest POST /api/admin/notify/test
// Sends synchronously so the operator sees the real delivery error.
func (h *Handler) AdminNotifyTest(w http.ResponseWriter, r *http.Request) {
	if !h.limitAdminWrite.Allow("AdminNotifyTest:" + clientIP(r)) {
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
	if h.notifier == nil {
		writeErr(w, http.StatusServiceUnavailable, "notify module unavailable")
		return
	}
	rt := h.notifier.Settings().Get()
	ev := notify.Event{
		Type:  notify.TypeTest,
		Level: notify.LevelInfo,
		Title: "Sub2API 扩展测试通知",
		Text:  "如果你收到这条消息，说明通知渠道配置正确。",
		Fields: []notify.Field{
			{Key: "渠道", Value: notify.ChannelLabel(rt.Channel)},
			{Key: "最低级别", Value: notify.LevelLabel(rt.MinLevel)},
		},
		Time: time.Now(),
	}
	if err := h.notifier.Send(r.Context(), rt, ev); err != nil {
		writeErr(w, http.StatusBadGateway, "发送失败："+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"message": "测试通知已发送",
		"stats":   h.notifier.Stats(),
	})
}
