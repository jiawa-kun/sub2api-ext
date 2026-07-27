package handler

import (
	"net/http"

	"sub2api-ext/internal/modules"
)

// Modules lists extension modules shipped by this sidecar service.
// GET /api/modules  (public; no auth required)
func (h *Handler) Modules(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	list := modules.Builtin()
	// Reflect runtime check-in switch on the checkin module card.
	rt := h.settings.Get()
	for i := range list {
		if list[i].ID == "checkin" {
			list[i].Enabled = rt.Enabled
		}
		if list[i].ID == "account-patrol" && h.patrol != nil {
			list[i].Enabled = h.patrol.Settings().Get().Enabled
		}
		if list[i].ID == "notify" && h.notifier != nil {
			list[i].Enabled = h.notifier.Settings().Get().Enabled
		}
		if list[i].ID == "lottery" && h.lottery != nil {
			list[i].Enabled = h.lottery.Get().Enabled
		}
		if list[i].ID == "daily-report" && h.report != nil {
			list[i].Enabled = h.report.Settings().Get().Enabled
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"product":      modules.ProductID,
		"product_name": modules.ProductName,
		"compat_name":  modules.CompatName,
		"base_path":    h.cfg.Server.BasePath,
		"modules":      list,
	})
}
