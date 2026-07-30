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
		{
			ID:          "ranking",
			Name:        "排行榜",
			Description: "用户消费排行（对接 Sub2API 用量）+ 扩展奖励排行（签到/抽奖获得总额），支持今日/昨日/近7天/近30天；管理台可配置奖励榜/消费榜活动结算发奖。",
			UserPath:    "./rank.html",
			AdminPath:   "./admin.html#campaign",
			APIBase:     "./api/ranking",
			Enabled:     true,
			Version:     "1.0",
			Status:      "active",
			Tags:        []string{"运营", "排行", "玩法"},
		},
		{
			ID:          "tasks",
			Name:        "任务中心",
			Description: "签到/抽奖等扩展行为任务，完成可领取额外奖励（默认奖励为 0，可在管理台配置）。",
			UserPath:    "./tasks.html",
			AdminPath:   "./admin.html#tasks",
			APIBase:     "./api/tasks",
			Enabled:     true,
			Version:     "1.0",
			Status:      "active",
			Tags:        []string{"运营", "任务", "玩法"},
		},
		{
			ID:          "ledger",
			Name:        "发放总账",
			Description: "扩展侧签到/抽奖/排行发奖/任务领取的统一发放流水与对账。",
			UserPath:    "",
			AdminPath:   "./admin.html#ledger",
			APIBase:     "./api/admin/ledger",
			Enabled:     true,
			Version:     "1.0",
			Status:      "active",
			Tags:        []string{"运营", "对账", "财务"},
		},
		{
			ID:          "account-patrol",
			Name:        "账号模型巡检",
			Description: "按分组定时测活账号模型，失败自动下线/删除，支持管理页配置与手动触发。",
			UserPath:    "",
			AdminPath:   "./admin.html#patrol",
			APIBase:     "./api/admin/patrol",
			Enabled:     true,
			Version:     "1.0",
			Status:      "active",
			Tags:        []string{"账号", "巡检", "定时任务"},
		},
		{
			ID:          "notify",
			Name:        "通知中心",
			Description: "把巡检处置、签到预算耗尽、配置变更等事件推送到 Webhook / 企业微信 / Telegram。",
			UserPath:    "",
			AdminPath:   "./admin.html#notify",
			APIBase:     "./api/admin/notify",
			Enabled:     true,
			Version:     "1.0",
			Status:      "active",
			Tags:        []string{"通知", "告警", "运维"},
		},
		{
			ID:          "lottery",
			Name:        "幸运抽奖",
			Description: "签到后每日一次抽奖，奖项名称/额度/权重均可后台配置，独立日预算与单次上限。",
			UserPath:    "./",
			AdminPath:   "./admin.html#lottery",
			APIBase:     "./api/lottery",
			Enabled:     true,
			Version:     "1.0",
			Status:      "active",
			Tags:        []string{"余额", "运营", "玩法"},
		},
		{
			ID:          "daily-report",
			Name:        "运营日报",
			Description: "每天定时把签到、抽奖、巡检的当日结果汇总成一条日报，复用通知中心渠道送达。",
			UserPath:    "",
			AdminPath:   "./admin.html#report",
			APIBase:     "./api/admin/report",
			Enabled:     true,
			Version:     "1.0",
			Status:      "active",
			Tags:        []string{"运营", "日报", "定时任务"},
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
