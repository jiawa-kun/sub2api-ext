package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"sub2api-ext/internal/lottery"
	"sub2api-ext/internal/modules"
	"sub2api-ext/internal/notify"
	"sub2api-ext/internal/patrol"
	"sub2api-ext/internal/report"
	"sub2api-ext/internal/settings"
)

const moduleEnabledSettingPrefix = "module_enabled_"

func moduleEnabledSettingKey(id string) string { return moduleEnabledSettingPrefix + id }

func normalizeModuleID(id string) string {
	return strings.TrimSpace(strings.ToLower(id))
}

func isKnownModuleID(id string) bool {
	id = normalizeModuleID(id)
	for _, m := range modules.Builtin() {
		if m.ID == id && m.Status == "active" {
			return true
		}
	}
	return false
}

func (h *Handler) moduleEnabled(ctx context.Context, id string) bool {
	id = normalizeModuleID(id)
	switch id {
	case "checkin":
		return h.settings.Get().Enabled
	case "tasks":
		return h.tasks != nil && h.tasks.Get().Enabled
	case "lottery":
		return h.lottery != nil && h.lottery.Get().Enabled
	case "account-patrol":
		return h.patrol != nil && h.patrol.Settings().Get().Enabled
	case "notify":
		return h.notifier != nil && h.notifier.Settings().Get().Enabled
	case "daily-report":
		return h.report != nil && h.report.Settings().Get().Enabled
	case "redistribution":
		return h.redistribution != nil && h.redistribution.Settings().Get().Enabled
	case "creative":
		if h.creative == nil {
			return false
		}
	case "ranking", "ledger":
		// fall through to the generic persisted module switch below
	default:
		if !isKnownModuleID(id) {
			return false
		}
	}
	v, ok, err := h.store.GetSetting(ctx, moduleEnabledSettingKey(id))
	if err != nil || !ok {
		return true
	}
	b, err := strconv.ParseBool(strings.TrimSpace(v))
	if err != nil {
		return true
	}
	return b
}

func (h *Handler) requireModuleEnabled(w http.ResponseWriter, r *http.Request, id string) bool {
	if h.moduleEnabled(r.Context(), id) {
		return true
	}
	writeErr(w, http.StatusServiceUnavailable, "module disabled: "+id)
	return false
}

func (h *Handler) activeModuleIDs(ctx context.Context) []string {
	mods := modules.Builtin()
	out := make([]string, 0, len(mods))
	for _, m := range mods {
		if m.Status == "active" && h.moduleEnabled(ctx, m.ID) {
			out = append(out, m.ID)
		}
	}
	return out
}

func (h *Handler) moduleStatusList(ctx context.Context) []map[string]any {
	mods := modules.Builtin()
	out := make([]map[string]any, 0, len(mods))
	for _, m := range mods {
		if m.Status != "active" {
			continue
		}
		out = append(out, map[string]any{
			"id":          m.ID,
			"name":        m.Name,
			"description": m.Description,
			"user_path":   m.UserPath,
			"admin_path":  m.AdminPath,
			"api_base":    m.APIBase,
			"status":      m.Status,
			"tags":        m.Tags,
			"enabled":     h.moduleEnabled(ctx, m.ID),
		})
	}
	return out
}

func (h *Handler) setModuleEnabled(ctx context.Context, r *http.Request, id string, enabled bool) error {
	id = normalizeModuleID(id)
	if !isKnownModuleID(id) {
		return fmt.Errorf("unknown module: %s", id)
	}
	switch id {
	case "checkin":
		_, err := h.settings.Update(ctx, settings.UpdateInput{Enabled: &enabled})
		return err
	case "tasks":
		if h.tasks == nil {
			return fmt.Errorf("tasks module unavailable")
		}
		rt := h.tasks.Get()
		rt.Enabled = enabled
		return h.tasks.Save(ctx, rt)
	case "lottery":
		if h.lottery == nil {
			return fmt.Errorf("lottery module unavailable")
		}
		_, err := h.lottery.Update(ctx, lottery.UpdateInput{Enabled: &enabled})
		return err
	case "account-patrol":
		if h.patrol == nil {
			return fmt.Errorf("patrol module unavailable")
		}
		if enabled && h.cfg.Security.SensitiveWriteRequireAPIKey && !h.isServerAdminCredential(r) && h.effectiveAdminCred() != "" {
			return fmt.Errorf("启用巡检属于敏感配置，请使用服务端 Admin API Key（x-api-key）确认")
		}
		_, err := h.patrol.Settings().Update(ctx, patrol.UpdateInput{Enabled: &enabled})
		return err
	case "notify":
		if h.notifier == nil {
			return fmt.Errorf("notify module unavailable")
		}
		_, err := h.notifier.Settings().Update(ctx, notify.UpdateInput{Enabled: &enabled})
		return err
	case "daily-report":
		if h.report == nil {
			return fmt.Errorf("report module unavailable")
		}
		_, err := h.report.Settings().Update(ctx, report.UpdateInput{Enabled: &enabled})
		return err
	case "redistribution":
		if h.redistribution == nil {
			return fmt.Errorf("redistribution module unavailable")
		}
		rt := h.redistribution.Settings().Get()
		rt.Enabled = enabled
		_, err := h.redistribution.Settings().Save(ctx, rt)
		return err
	case "ranking", "ledger", "creative":
		return h.store.SetSetting(ctx, moduleEnabledSettingKey(id), strconv.FormatBool(enabled))
	default:
		return h.store.SetSetting(ctx, moduleEnabledSettingKey(id), strconv.FormatBool(enabled))
	}
}

// AdminModuleSettings GET lists all module switches; PUT/PATCH updates one module switch.
func (h *Handler) AdminModuleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if !h.limitAdminRead.Allow("AdminModuleSettings:" + clientIP(r)) {
			writeErr(w, http.StatusTooManyRequests, "rate limited")
			return
		}
		if _, err := h.requireAdmin(r); err != nil {
			writeErr(w, http.StatusUnauthorized, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": h.moduleStatusList(r.Context())})
	case http.MethodPut, http.MethodPatch, http.MethodPost:
		if !h.limitAdminWrite.Allow("AdminModuleSettings:" + clientIP(r)) {
			writeErr(w, http.StatusTooManyRequests, "rate limited")
			return
		}
		if _, err := h.requireAdmin(r); err != nil {
			writeErr(w, http.StatusUnauthorized, err.Error())
			return
		}
		var in struct {
			ID      string `json:"id"`
			Enabled *bool  `json:"enabled"`
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err := json.Unmarshal(body, &in); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid json")
			return
		}
		id := normalizeModuleID(in.ID)
		if id == "" || in.Enabled == nil {
			writeErr(w, http.StatusBadRequest, "id/enabled required")
			return
		}
		if err := h.setModuleEnabled(r.Context(), r, id, *in.Enabled); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": h.moduleStatusList(r.Context()), "id": id, "enabled": h.moduleEnabled(r.Context(), id)})
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}
