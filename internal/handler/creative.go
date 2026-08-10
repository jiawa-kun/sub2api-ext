package handler

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"sub2api-ext/internal/creative"
	"sub2api-ext/internal/store"
	"sub2api-ext/internal/sub2api"
)

func (h *Handler) SetCreative(s *creative.Service) { h.creative = s }

func (h *Handler) requireUser(r *http.Request) (*sub2api.User, error) {
	token := extractToken(r)
	if token == "" {
		return nil, fmt.Errorf("missing token")
	}
	u, err := h.client.ResolveUser(r.Context(), token, clientMetaFromRequest(r))
	if err != nil {
		return nil, fmt.Errorf("invalid token: %w", err)
	}
	if u.ID <= 0 {
		return nil, fmt.Errorf("invalid user")
	}
	return u, nil
}

func (h *Handler) CreativeOptions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	u, err := h.requireUser(r)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	if h.creative == nil {
		writeErr(w, http.StatusServiceUnavailable, "creative module unavailable")
		return
	}
	models, err := h.creative.ModelOptions(r.Context(), u.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	credentials, err := h.creative.CredentialOptions(r.Context(), u.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": models, "credentials": credentials, "balance": u.Balance, "user_id": u.ID, "is_admin": strings.EqualFold(u.Role, "admin")})
}

func (h *Handler) CreativeCredentials(w http.ResponseWriter, r *http.Request) {
	u, err := h.requireUser(r)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	if h.creative == nil {
		writeErr(w, http.StatusServiceUnavailable, "creative module unavailable")
		return
	}
	switch r.Method {
	case http.MethodGet:
		items, listErr := h.creative.CredentialOptions(r.Context(), u.ID)
		if listErr != nil {
			writeErr(w, http.StatusInternalServerError, listErr.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items})
	case http.MethodPost, http.MethodPut:
		var input struct {
			ProviderID int64  `json:"provider_id"`
			APIKey     string `json:"api_key"`
		}
		if decodeErr := decodeCreativeJSON(r, &input, 8<<10); decodeErr != nil {
			writeErr(w, http.StatusBadRequest, decodeErr.Error())
			return
		}
		item, saveErr := h.creative.SaveUserCredential(r.Context(), u.ID, input.ProviderID, input.APIKey)
		if saveErr != nil {
			writeErr(w, http.StatusBadRequest, saveErr.Error())
			return
		}
		writeJSON(w, http.StatusOK, item)
	case http.MethodDelete:
		providerID, parseErr := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("provider_id")), 10, 64)
		if parseErr != nil || providerID <= 0 {
			writeErr(w, http.StatusBadRequest, "provider_id is required")
			return
		}
		if deleteErr := h.creative.DeleteUserCredential(r.Context(), u.ID, providerID); deleteErr != nil {
			writeErr(w, http.StatusInternalServerError, deleteErr.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) CreativeImages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	u, err := h.requireUser(r)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	var in creative.ImageInput
	if err := decodeCreativeJSON(r, &in, 8<<20); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := creative.ValidateImageDataURL(in.ImageDataURL); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	job, err := h.creative.GenerateImage(r.Context(), u.ID, in)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error(), "job": creativeJobPublic(job)})
		return
	}
	writeJSON(w, http.StatusOK, creativeJobPublic(job))
}
func (h *Handler) CreativeVideos(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	u, err := h.requireUser(r)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	var in creative.VideoInput
	if err := decodeCreativeJSON(r, &in, 8<<20); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := creative.ValidateImageDataURL(in.ImageDataURL); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	job, err := h.creative.CreateVideo(r.Context(), u.ID, in)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error(), "job": creativeJobPublic(job)})
		return
	}
	writeJSON(w, http.StatusOK, creativeJobPublic(job))
}
func decodeCreativeJSON(r *http.Request, out any, limit int64) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		return err
	}
	if int64(len(body)) > limit {
		return fmt.Errorf("request body too large")
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("invalid json: %w", err)
	}
	return nil
}

func (h *Handler) CreativeJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	u, err := h.requireUser(r)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	limit := queryInt(r, "page_size", 10)
	page := queryInt(r, "page", 1)
	if page < 1 {
		page = 1
	}
	items, total, err := h.creative.ListJobs(r.Context(), store.CreativeJobFilter{UserID: u.ID, MediaType: strings.TrimSpace(r.URL.Query().Get("type")), Status: strings.TrimSpace(r.URL.Query().Get("status")), Limit: limit, Offset: (page - 1) * limit})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]any, 0, len(items))
	for i := range items {
		out = append(out, creativeJobPublic(&items[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "total": total, "page": page, "page_size": limit})
}
func (h *Handler) CreativeJobByID(w http.ResponseWriter, r *http.Request) {
	u, err := h.requireUser(r)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	path := strings.TrimPrefix(r.URL.Path, h.cfg.Server.BasePath+"/api/creative/jobs/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid job id")
		return
	}
	if len(parts) == 1 && r.Method == http.MethodDelete {
		if err := h.creative.DeleteJob(r.Context(), id, u.ID); err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, sql.ErrNoRows) {
				status = http.StatusNotFound
			}
			writeErr(w, status, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
		return
	}
	job, err := h.creative.GetJob(r.Context(), id, u.ID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "job not found")
		return
	}
	if len(parts) > 1 && parts[1] == "content" {
		if r.Method != http.MethodGet {
			writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.serveCreativeJobContent(w, r, job)
		return
	}
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, creativeJobPublic(job))
}
func creativeJobPublic(j *store.CreativeJob) map[string]any {
	if j == nil {
		return nil
	}
	params := json.RawMessage(`{}`)
	if raw := []byte(strings.TrimSpace(j.ParamsJSON)); json.Valid(raw) {
		params = json.RawMessage(raw)
	}
	count := 1
	if j.MediaType == "image" {
		var v struct {
			Data []any `json:"data"`
		}
		if json.Unmarshal([]byte(j.ResultJSON), &v) == nil && len(v.Data) > 0 {
			count = len(v.Data)
		}
	}
	return map[string]any{"id": j.ID, "order_no": j.OrderNo, "model_id": j.ModelID, "media_type": j.MediaType, "prompt": j.Prompt, "params": params, "charge_amount": j.ChargeAmount, "charge_status": j.ChargeStatus, "status": j.Status, "progress": j.Progress, "error_code": j.ErrorCode, "error_message": j.ErrorMessage, "content_count": count, "created_at": j.CreatedAt, "updated_at": j.UpdatedAt, "completed_at": j.CompletedAt, "deleted_at": j.DeletedAt}
}

func creativeAdminJobPublic(j *store.CreativeJob) map[string]any {
	out := creativeJobPublic(j)
	if out == nil {
		return nil
	}
	out["user_id"] = j.UserID
	out["provider_id"] = j.ProviderID
	return out
}

func (h *Handler) serveCreativeJobContent(w http.ResponseWriter, r *http.Request, job *store.CreativeJob) {
	index := queryInt(r, "index", 0)
	content, err := h.creative.OpenJobContent(r.Context(), job, index, r.Header.Get("Range"))
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	defer content.Body.Close()
	w.Header().Set("Content-Type", content.ContentType)
	w.Header().Set("Content-Disposition", "inline")
	w.Header().Set("Cache-Control", "private, no-store")
	if content.ReadSeeker != nil {
		http.ServeContent(w, r, content.Name, content.ModTime, content.ReadSeeker)
		return
	}
	if content.ContentRange != "" {
		w.Header().Set("Content-Range", content.ContentRange)
	}
	if content.AcceptRanges != "" {
		w.Header().Set("Accept-Ranges", content.AcceptRanges)
	}
	if content.ContentLength >= 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(content.ContentLength, 10))
	}
	w.WriteHeader(content.StatusCode)
	_, _ = io.Copy(w, content.Body)
}

func (h *Handler) AdminCreativeProviders(w http.ResponseWriter, r *http.Request) {
	if _, err := h.requireAdmin(r); err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	switch r.Method {
	case http.MethodGet:
		list, err := h.creative.ListProviders(r.Context())
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		out := make([]map[string]any, 0, len(list))
		for _, p := range list {
			out = append(out, providerPublic(p))
		}
		writeJSON(w, 200, map[string]any{"items": out})
	case http.MethodPost, http.MethodPut:
		var in struct {
			ID          int64  `json:"id"`
			Name        string `json:"name"`
			Kind        string `json:"kind"`
			BaseURL     string `json:"base_url"`
			APIKey      string `json:"api_key"`
			SourceGroup string `json:"source_group"`
			Enabled     bool   `json:"enabled"`
		}
		if err := decodeCreativeJSON(r, &in, 1<<20); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		p := store.CreativeProvider{
			ID:          in.ID,
			Name:        in.Name,
			Kind:        in.Kind,
			BaseURL:     in.BaseURL,
			APIKey:      in.APIKey,
			SourceGroup: in.SourceGroup,
			Enabled:     in.Enabled,
		}
		saved, err := h.creative.SaveProvider(r.Context(), p)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		writeJSON(w, 200, providerPublic(*saved))
	default:
		writeErr(w, 405, "method not allowed")
	}
}

func (h *Handler) AdminCreativeAccountPool(w http.ResponseWriter, r *http.Request) {
	if _, err := h.requireAdmin(r); err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	overview, err := h.creative.AccountPoolOverview(r.Context(), r.URL.Query().Get("group"))
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, overview)
}
func providerPublic(p store.CreativeProvider) map[string]any {
	return map[string]any{"id": p.ID, "name": p.Name, "kind": p.Kind, "base_url": p.BaseURL, "source_group": p.SourceGroup, "enabled": p.Enabled, "api_key_configured": strings.TrimSpace(p.APIKey) != "", "api_key_masked": maskCreativeSecret(p.APIKey), "created_at": p.CreatedAt, "updated_at": p.UpdatedAt}
}
func maskCreativeSecret(v string) string {
	v = strings.TrimSpace(v)
	if len(v) <= 8 {
		if v == "" {
			return ""
		}
		return "****"
	}
	return v[:4] + "****" + v[len(v)-4:]
}
func (h *Handler) AdminCreativeProviderByID(w http.ResponseWriter, r *http.Request) {
	if _, err := h.requireAdmin(r); err != nil {
		writeErr(w, 401, err.Error())
		return
	}
	path := strings.TrimPrefix(r.URL.Path, h.cfg.Server.BasePath+"/api/admin/creative/providers/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		writeErr(w, 400, "invalid provider id")
		return
	}
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}
	switch {
	case r.Method == http.MethodPost && action == "sync":
		res, err := h.creative.SyncProviderModels(r.Context(), id)
		if err != nil {
			writeErr(w, 502, err.Error())
			return
		}
		writeJSON(w, 200, res)
	case r.Method == http.MethodPost && action == "test":
		if err := h.creative.TestProvider(r.Context(), id); err != nil {
			writeErr(w, 502, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true})
	case r.Method == http.MethodDelete && action == "":
		if err := h.creative.DeleteProvider(r.Context(), id); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"deleted": true})
	default:
		writeErr(w, 405, "method not allowed")
	}
}
func (h *Handler) AdminCreativeModels(w http.ResponseWriter, r *http.Request) {
	if _, err := h.requireAdmin(r); err != nil {
		writeErr(w, 401, err.Error())
		return
	}
	switch r.Method {
	case http.MethodGet:
		pid := int64(queryInt(r, "provider_id", 0))
		list, err := h.creative.ListModels(r.Context(), pid, false)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"items": list})
	case http.MethodPut, http.MethodPost:
		var m store.CreativeModel
		if err := decodeCreativeJSON(r, &m, 1<<20); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		saved, err := h.creative.SaveModel(r.Context(), m)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		writeJSON(w, 200, saved)
	case http.MethodDelete:
		path := strings.TrimPrefix(r.URL.Path, h.cfg.Server.BasePath+"/api/admin/creative/models/")
		id, err := strconv.ParseInt(strings.Trim(path, "/"), 10, 64)
		if err != nil || id <= 0 {
			writeErr(w, 400, "invalid model id")
			return
		}
		if err := h.creative.DeleteModel(r.Context(), id); err != nil {
			status := 400
			if errors.Is(err, sql.ErrNoRows) {
				status = 404
			}
			writeErr(w, status, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"deleted": true})
	default:
		writeErr(w, 405, "method not allowed")
	}
}
func (h *Handler) AdminCreativeJobs(w http.ResponseWriter, r *http.Request) {
	if _, err := h.requireAdmin(r); err != nil {
		writeErr(w, 401, err.Error())
		return
	}
	if r.Method != http.MethodGet {
		writeErr(w, 405, "method not allowed")
		return
	}
	limit := queryInt(r, "page_size", 10)
	page := queryInt(r, "page", 1)
	filter := store.CreativeJobFilter{
		UserID:    int64(queryInt(r, "user_id", 0)),
		MediaType: strings.TrimSpace(r.URL.Query().Get("type")),
		Status:    strings.TrimSpace(r.URL.Query().Get("status")),
		Limit:     limit,
		Offset:    (page - 1) * limit,
	}
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get("deleted"))) {
	case "all":
		filter.IncludeDeleted = true
	case "deleted":
		filter.DeletedOnly = true
	}
	items, total, err := h.creative.ListJobs(r.Context(), filter)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	out := make([]map[string]any, 0, len(items))
	for i := range items {
		out = append(out, creativeAdminJobPublic(&items[i]))
	}
	writeJSON(w, 200, map[string]any{"items": out, "total": total, "page": page, "page_size": limit})
}

type creativeAdminUser struct {
	UserID      int64  `json:"user_id"`
	Username    string `json:"username,omitempty"`
	Email       string `json:"email,omitempty"`
	DisplayName string `json:"display_name"`
	WorkCount   int    `json:"work_count"`
}

func (h *Handler) AdminCreativeUsers(w http.ResponseWriter, r *http.Request) {
	if _, err := h.requireAdmin(r); err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	summaries, err := h.store.ListCreativeJobUserSummaries(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	profiles := map[int64]sub2api.User{}
	h.syncAdminCred()
	if h.client != nil && h.client.AdminToken() != "" {
		if users, listErr := h.client.ListAllAdminUsers(r.Context(), 5000); listErr == nil {
			for _, user := range users {
				profiles[user.ID] = user
			}
		}
	}
	out := make([]creativeAdminUser, 0, len(summaries))
	for _, summary := range summaries {
		profile := profiles[summary.UserID]
		name := strings.TrimSpace(profile.Username)
		if name == "" {
			name = strings.TrimSpace(profile.Email)
		}
		if name == "" {
			name = fmt.Sprintf("用户 #%d", summary.UserID)
		}
		out = append(out, creativeAdminUser{UserID: summary.UserID, Username: strings.TrimSpace(profile.Username), Email: strings.TrimSpace(profile.Email), DisplayName: name, WorkCount: summary.WorkCount})
	}
	sort.SliceStable(out, func(i, j int) bool {
		left, right := strings.ToLower(out[i].DisplayName), strings.ToLower(out[j].DisplayName)
		if left == right {
			return out[i].UserID < out[j].UserID
		}
		return left < right
	})
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (h *Handler) AdminCreativeJobByID(w http.ResponseWriter, r *http.Request) {
	if _, err := h.requireAdmin(r); err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	path := strings.TrimPrefix(r.URL.Path, h.cfg.Server.BasePath+"/api/admin/creative/jobs/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, "invalid job id")
		return
	}
	job, err := h.creative.GetJob(r.Context(), id, 0)
	if err != nil {
		writeErr(w, http.StatusNotFound, "job not found")
		return
	}
	if len(parts) > 1 && parts[1] == "content" {
		if r.Method != http.MethodGet {
			writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.serveCreativeJobContent(w, r, job)
		return
	}
	if len(parts) != 1 {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, creativeAdminJobPublic(job))
	case http.MethodDelete:
		if err := h.creative.DeleteJobAsAdmin(r.Context(), id); err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, sql.ErrNoRows) {
				status = http.StatusNotFound
			}
			writeErr(w, status, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}
func queryInt(r *http.Request, key string, fallback int) int {
	v, _ := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get(key)))
	if v <= 0 {
		return fallback
	}
	return v
}
