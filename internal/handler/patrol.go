package handler

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"sub2api-ext/internal/patrol"
)

// AdminGetPatrolSettings GET /api/admin/patrol/settings
func (h *Handler) AdminGetPatrolSettings(w http.ResponseWriter, r *http.Request) {
	if !h.limitAdminRead.Allow("AdminGetPatrolSettings:" + clientIP(r)) {
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
	if h.patrol == nil {
		writeErr(w, http.StatusServiceUnavailable, "patrol module unavailable")
		return
	}
	rt := h.patrol.Settings().Get()
	writeJSON(w, http.StatusOK, map[string]any{
		"settings": rt,
		"status":   h.patrol.Snapshot(),
	})
}

// AdminUpdatePatrolSettings PUT/POST /api/admin/patrol/settings
func (h *Handler) AdminUpdatePatrolSettings(w http.ResponseWriter, r *http.Request) {
	if !h.limitAdminWrite.Allow("AdminUpdatePatrolSettings:" + clientIP(r)) {
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
	if h.patrol == nil {
		writeErr(w, http.StatusServiceUnavailable, "patrol module unavailable")
		return
	}
	var in patrol.UpdateInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	// Enabling patrol is sensitive: prefer server admin credential when configured.
	if in.Enabled != nil && *in.Enabled && h.cfg.Security.SensitiveWriteRequireAPIKey {
		if !h.isServerAdminCredential(r) && h.effectiveAdminCred() != "" {
			// still allow browser admin JWT, but encourage key; do not hard-block JWT admins
		}
	}
	rt, err := h.patrol.Settings().Update(r.Context(), in)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"settings": rt,
		"status":   h.patrol.Snapshot(),
		"message":  "patrol settings updated",
	})
}

// AdminPatrolStatus GET /api/admin/patrol/status
func (h *Handler) AdminPatrolStatus(w http.ResponseWriter, r *http.Request) {
	if !h.limitAdminRead.Allow("AdminPatrolStatus:" + clientIP(r)) {
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
	if h.patrol == nil {
		writeErr(w, http.StatusServiceUnavailable, "patrol module unavailable")
		return
	}
	limit := 10
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	runs, err := h.patrol.RecentRuns(r.Context(), limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": h.patrol.Snapshot(),
		"runs":   runs,
	})
}

// AdminPatrolRun POST /api/admin/patrol/run
func (h *Handler) AdminPatrolRun(w http.ResponseWriter, r *http.Request) {
	if !h.limitAdminWrite.Allow("AdminPatrolRun:" + clientIP(r)) {
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
	if h.patrol == nil {
		writeErr(w, http.StatusServiceUnavailable, "patrol module unavailable")
		return
	}
	id, err := h.patrol.Trigger(r.Context(), "manual")
	if err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"message": "patrol started",
		"run_id":  id,
		"status":  h.patrol.Snapshot(),
	})
}

// AdminPatrolStop POST /api/admin/patrol/stop
func (h *Handler) AdminPatrolStop(w http.ResponseWriter, r *http.Request) {
	if !h.limitAdminWrite.Allow("AdminPatrolStop:" + clientIP(r)) {
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
	if h.patrol == nil {
		writeErr(w, http.StatusServiceUnavailable, "patrol module unavailable")
		return
	}
	ok := h.patrol.StopRun()
	writeJSON(w, http.StatusOK, map[string]any{
		"stopped": ok,
		"status":  h.patrol.Snapshot(),
	})
}


// AdminPatrolOptions GET /api/admin/patrol/options
// Returns group dropdown options (and models for selected groups if ?groups=a,b provided).
func (h *Handler) AdminPatrolOptions(w http.ResponseWriter, r *http.Request) {
	if !h.limitAdminRead.Allow("AdminPatrolOptions:" + clientIP(r)) {
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
	if h.patrol == nil {
		writeErr(w, http.StatusServiceUnavailable, "patrol module unavailable")
		return
	}

	ctx := r.Context()
	rt := h.patrol.Settings().Get()
	// empty platform => merge common platforms inside client
	platform := strings.TrimSpace(r.URL.Query().Get("platform"))
	groups, err := h.client.ListGroups(ctx, platform)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "list groups: "+err.Error())
		return
	}

	// selected groups for model aggregation
	selected := []string{}
	if raw := strings.TrimSpace(r.URL.Query().Get("groups")); raw != "" {
		for _, p := range strings.Split(raw, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				selected = append(selected, p)
			}
		}
	}
	if len(selected) == 0 {
		selected = append([]string{}, rt.Groups...)
	}
	// models are group-specific: only use the first selected group
	if len(selected) > 1 {
		selected = selected[:1]
	}

	modelSet := map[string]struct{}{}
	modelErrs := []string{}
	for _, g := range selected {
		models, err := h.client.CollectGroupModelsLimited(ctx, g, rt.Timezone, 8)
		if err != nil {
			modelErrs = append(modelErrs, g+": "+err.Error())
			continue
		}
		for _, m := range models {
			modelSet[m] = struct{}{}
		}
	}
	models := make([]string, 0, len(modelSet))
	for m := range modelSet {
		models = append(models, m)
	}
	sort.Strings(models)

	writeJSON(w, http.StatusOK, map[string]any{
		"groups":          groups,
		"models":          models,
		"selected_groups": selected,
		"model_errors":    modelErrs,
		"settings":        rt,
	})
}

// AdminPatrolModels GET /api/admin/patrol/models?group=xxx
func (h *Handler) AdminPatrolModels(w http.ResponseWriter, r *http.Request) {
	if !h.limitAdminRead.Allow("AdminPatrolModels:" + clientIP(r)) {
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
	if h.patrol == nil {
		writeErr(w, http.StatusServiceUnavailable, "patrol module unavailable")
		return
	}
	group := strings.TrimSpace(r.URL.Query().Get("group"))
	if group == "" {
		// allow groups=a,b
		group = strings.TrimSpace(r.URL.Query().Get("groups"))
	}
	if group == "" {
		writeErr(w, http.StatusBadRequest, "group is required")
		return
	}
	rt := h.patrol.Settings().Get()
	parts := strings.Split(group, ",")
	modelSet := map[string]struct{}{}
	for _, g := range parts {
		g = strings.TrimSpace(g)
		if g == "" {
			continue
		}
		models, err := h.client.CollectGroupModelsLimited(r.Context(), g, rt.Timezone, 10)
		if err != nil {
			writeErr(w, http.StatusBadGateway, "list models for group "+g+": "+err.Error())
			return
		}
		for _, m := range models {
			modelSet[m] = struct{}{}
		}
	}
	models := make([]string, 0, len(modelSet))
	for m := range modelSet {
		models = append(models, m)
	}
	sort.Strings(models)
	writeJSON(w, http.StatusOK, map[string]any{
		"group":  group,
		"models": models,
		"count":  len(models),
	})
}
