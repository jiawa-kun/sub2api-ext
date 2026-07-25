package modules

// Module describes one extension capability mounted under this service.
// Check-in is the first built-in module; more modules can register later
// without changing the Sub2API iframe integration model.
type Module struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	UserPath    string   `json:"user_path"`
	AdminPath   string   `json:"admin_path,omitempty"`
	APIBase     string   `json:"api_base,omitempty"`
	Enabled     bool     `json:"enabled"`
	Version     string   `json:"version,omitempty"`
	Status      string   `json:"status"` // active | planned
	Tags        []string `json:"tags,omitempty"`
}

// Product is the public identity of this sidecar service.
const (
	ProductID   = "sub2api-ext"
	ProductName = "Sub2API 扩展"
	// ProjectName is the repo/binary/image/container name.
	ProjectName = "sub2api-ext"
	// CompatName kept for API compatibility with earlier clients.
	CompatName = ProjectName
)

// Builtin returns the currently shipped modules.
// Status "active" modules are available now; keep planned modules out until implemented.
func Builtin() []Module {
	return []Module{
		{
			ID:          "checkin",
			Name:        "每日签到",
			Description: "每日签到领取余额奖励，支持固定/随机额度、连签加成、日预算与管理审计。",
			UserPath:    "./",
			AdminPath:   "./admin.html",
			APIBase:     "./api",
			Enabled:     true,
			Version:     "1.0",
			Status:      "active",
			Tags:        []string{"余额", "运营", "iframe"},
		},
	}
}

// ActiveIDs returns ids of active modules.
func ActiveIDs() []string {
	all := Builtin()
	out := make([]string, 0, len(all))
	for _, m := range all {
		if m.Status == "active" && m.Enabled {
			out = append(out, m.ID)
		}
	}
	return out
}
