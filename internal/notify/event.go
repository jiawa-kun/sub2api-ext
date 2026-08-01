// Package notify delivers operational events (patrol actions, budget
// exhaustion, config changes) to an external chat/webhook endpoint.
//
// Delivery is strictly best-effort and fully asynchronous: the queue is
// bounded and drops events when full, so a slow or dead webhook can never
// block or slow down patrol runs and check-in requests.
package notify

import (
	"time"
)

// Event levels, ordered by severity.
const (
	LevelInfo  = "info"
	LevelWarn  = "warn"
	LevelError = "error"
)

// Event types. These double as the keys used for per-event subscription.
const (
	TypePatrolRunFinished      = "patrol.run_finished"
	TypePatrolAccountAction    = "patrol.account_action"
	TypeCheckinBudget          = "checkin.budget_exhausted"
	TypeLotteryBudget          = "lottery.budget_exhausted"
	TypeSettingsChanged        = "settings.changed"
	TypeRedistributionFinished = "redistribution.finished"
	TypeTest                   = "notify.test"
	// TypeDailyReport is delivered by the report module. It is deliberately
	// absent from AllTypes: the digest has its own on/off switch and is sent
	// directly, so it is not part of the alert subscription list.
	TypeDailyReport = "report.daily"
)

// AllTypes lists every event type that can be subscribed to.
func AllTypes() []string {
	return []string{
		TypePatrolRunFinished,
		TypePatrolAccountAction,
		TypeCheckinBudget,
		TypeLotteryBudget,
		TypeSettingsChanged,
		TypeRedistributionFinished,
	}
}

// TypeLabel returns a human-readable Chinese label for an event type.
func TypeLabel(t string) string {
	switch t {
	case TypePatrolRunFinished:
		return "巡检运行结束"
	case TypePatrolAccountAction:
		return "账号被下线/删除"
	case TypeCheckinBudget:
		return "签到日预算耗尽"
	case TypeLotteryBudget:
		return "抽奖日预算耗尽"
	case TypeSettingsChanged:
		return "配置被修改"
	case TypeRedistributionFinished:
		return "额度回流完成"
	case TypeDailyReport:
		return "运营日报"
	case TypeTest:
		return "测试通知"
	default:
		return t
	}
}

// Field is one key/value detail line rendered in the message body.
type Field struct {
	Key   string
	Value string
}

// Event is a single outbound notification.
type Event struct {
	Type   string
	Level  string
	Title  string
	Text   string
	Fields []Field
	Time   time.Time
}

// levelRank maps a level to a comparable severity.
func levelRank(level string) int {
	switch level {
	case LevelError:
		return 3
	case LevelWarn:
		return 2
	default:
		return 1
	}
}

// LevelLabel returns a short Chinese label for a level.
func LevelLabel(level string) string {
	switch level {
	case LevelError:
		return "错误"
	case LevelWarn:
		return "警告"
	default:
		return "通知"
	}
}
