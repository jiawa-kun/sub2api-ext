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
	for i := range list {
		list[i].Enabled = h.moduleEnabled(r.Context(), list[i].ID)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"product":      modules.ProductID,
		"product_name": modules.ProductName,
		"compat_name":  modules.CompatName,
		"base_path":    h.cfg.Server.BasePath,
		"modules":      list,
	})
}
