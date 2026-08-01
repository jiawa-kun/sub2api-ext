package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"sub2api-ext/internal/redistribution"
)

func (h *Handler) AdminRedistributionSettings(w http.ResponseWriter, r *http.Request) {
	if h.redistribution == nil {
		writeErr(w, http.StatusServiceUnavailable, "redistribution module unavailable")
		return
	}
	if _, err := h.requireAdmin(r); err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	switch r.Method {
	case http.MethodGet:
		if !h.limitAdminRead.Allow("AdminRedistributionSettings:" + clientIP(r)) {
			writeErr(w, http.StatusTooManyRequests, "rate limited")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"settings": h.redistribution.Settings().Get(),
			"stats":    h.redistribution.Stats(r.Context()),
		})
	case http.MethodPut, http.MethodPost:
		if !h.limitAdminWrite.Allow("AdminRedistributionSettings:" + clientIP(r)) {
			writeErr(w, http.StatusTooManyRequests, "rate limited")
			return
		}
		var rt redistribution.Runtime
		if err := json.NewDecoder(r.Body).Decode(&rt); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
			return
		}
		saved, err := h.redistribution.Settings().Save(r.Context(), rt)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"settings": saved,
			"stats":    h.redistribution.Stats(r.Context()),
			"message":  "redistribution settings updated",
		})
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) AdminRedistributionPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.limitAdminWrite.Allow("AdminRedistributionPreview:" + clientIP(r)) {
		writeErr(w, http.StatusTooManyRequests, "rate limited")
		return
	}
	if _, err := h.requireAdmin(r); err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	if h.redistribution == nil {
		writeErr(w, http.StatusServiceUnavailable, "redistribution module unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	detail, err := h.redistribution.Preview(ctx, "manual", time.Now())
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"preview": detail})
}

func (h *Handler) AdminRedistributionExecute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.limitAdminWrite.Allow("AdminRedistributionExecute:" + clientIP(r)) {
		writeErr(w, http.StatusTooManyRequests, "rate limited")
		return
	}
	if _, err := h.requireAdmin(r); err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	if h.redistribution == nil {
		writeErr(w, http.StatusServiceUnavailable, "redistribution module unavailable")
		return
	}
	var in struct {
		BatchID int64  `json:"batch_id"`
		Confirm string `json:"confirm"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	if in.BatchID <= 0 {
		writeErr(w, http.StatusBadRequest, "batch_id required")
		return
	}
	if strings.ToUpper(strings.TrimSpace(in.Confirm)) != "EXECUTE" {
		writeErr(w, http.StatusBadRequest, "confirm must be EXECUTE")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()
	detail, err := h.redistribution.Execute(ctx, in.BatchID)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": detail})
}

func (h *Handler) AdminRedistributionStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if _, err := h.requireAdmin(r); err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	if h.redistribution == nil {
		writeErr(w, http.StatusServiceUnavailable, "redistribution module unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"stopped": h.redistribution.Stop()})
}

func (h *Handler) AdminRedistributionBatches(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if _, err := h.requireAdmin(r); err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, err := h.store.ListRedistributionBatches(r.Context(), limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

func (h *Handler) AdminRedistributionBatchByID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if _, err := h.requireAdmin(r); err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	if h.redistribution == nil {
		writeErr(w, http.StatusServiceUnavailable, "redistribution module unavailable")
		return
	}
	part := strings.TrimPrefix(r.URL.Path, h.cfg.Server.BasePath+"/api/admin/redistribution/batches/")
	id, err := strconv.ParseInt(strings.Trim(part, "/"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, "invalid batch id")
		return
	}
	detail, err := h.redistribution.Detail(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"detail": detail})
}

func (h *Handler) RedistributionRewards(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.redistribution == nil {
		writeErr(w, http.StatusServiceUnavailable, "redistribution module unavailable")
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
	items, err := h.store.ListRedistributionRewards(r.Context(), user.ID, 20)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) RedistributionClaim(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.redistribution == nil {
		writeErr(w, http.StatusServiceUnavailable, "redistribution module unavailable")
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
	var in struct {
		BatchID int64 `json:"batch_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.BatchID <= 0 {
		writeErr(w, http.StatusBadRequest, "batch_id required")
		return
	}
	entry, err := h.redistribution.Claim(r.Context(), user.ID, in.BatchID)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"reward": entry})
}
