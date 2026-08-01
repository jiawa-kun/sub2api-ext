package redistribution

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"sub2api-ext/internal/sub2api"
)

const moneyScale = 10000.0

type UserSnapshot struct {
	User            sub2api.User
	ExtensionAt     *time.Time
	TotalUsage      float64
	RecentUsage     float64
	EligibilityNote string
}

func IsInactive(rt Runtime, snap UserSnapshot, now time.Time) (bool, []string) {
	u := snap.User
	if u.ID <= 0 || strings.EqualFold(u.Role, "admin") || !strings.EqualFold(u.Status, "active") {
		return false, []string{"用户角色或状态不允许回收"}
	}
	// Paid balances are protected even if payment is enabled in the future.
	if u.TotalRecharged > 0 {
		return false, []string{"存在充值记录"}
	}
	for _, id := range rt.ExcludeUserIDs {
		if id == u.ID {
			return false, []string{"用户在排除名单"}
		}
	}
	if u.Balance < rt.Reclaim.MinBalance || u.Balance <= rt.Reclaim.ReserveBalance {
		return false, []string{"余额未达到回收门槛"}
	}
	if rt.NewUserProtectionDays > 0 && !u.CreatedAt.IsZero() && now.Before(u.CreatedAt.AddDate(0, 0, rt.NewUserProtectionDays)) {
		return false, []string{"新用户保护期内"}
	}

	results := make([]bool, 0, len(rt.InactiveRules))
	reasons := make([]string, 0, len(rt.InactiveRules))
	for _, rule := range rt.InactiveRules {
		if !rule.Enabled {
			continue
		}
		ok, reason := matchInactiveRule(rule, snap, now)
		results = append(results, ok)
		if ok && reason != "" {
			reasons = append(reasons, reason)
		}
	}
	return combine(rt.InactiveLogic, results), reasons
}

func matchInactiveRule(rule Rule, snap UserSnapshot, now time.Time) (bool, string) {
	u := snap.User
	switch rule.Type {
	case RuleNoActiveDays:
		ref := fallbackTime(u.LastActiveAt, u.CreatedAt)
		ok := olderThan(ref, rule.Days, now)
		return ok, fmt.Sprintf("%d 天无活跃", rule.Days)
	case RuleNoUsageDays:
		ref := fallbackTime(u.LastUsedAt, u.CreatedAt)
		ok := olderThan(ref, rule.Days, now)
		return ok, fmt.Sprintf("%d 天无消费", rule.Days)
	case RuleNoExtensionDays:
		ref := fallbackTime(snap.ExtensionAt, u.CreatedAt)
		ok := olderThan(ref, rule.Days, now)
		return ok, fmt.Sprintf("%d 天无扩展行为", rule.Days)
	case RuleAccountAgeDays:
		ok := !u.CreatedAt.IsZero() && olderThan(&u.CreatedAt, rule.Days, now)
		return ok, fmt.Sprintf("注册超过 %d 天", rule.Days)
	case RuleBalanceAtLeast:
		ok := u.Balance >= rule.Amount
		return ok, fmt.Sprintf("余额达到 %.4f", rule.Amount)
	default:
		return false, ""
	}
}

func IsActive(rt Runtime, snap UserSnapshot, now time.Time) (bool, []string) {
	u := snap.User
	if u.ID <= 0 || strings.EqualFold(u.Role, "admin") || !strings.EqualFold(u.Status, "active") {
		return false, nil
	}
	results := make([]bool, 0, len(rt.ActiveRules))
	reasons := make([]string, 0, len(rt.ActiveRules))
	for _, rule := range rt.ActiveRules {
		if !rule.Enabled {
			continue
		}
		ok, reason := matchActiveRule(rule, snap, now)
		results = append(results, ok)
		if ok && reason != "" {
			reasons = append(reasons, reason)
		}
	}
	return combine(rt.ActiveLogic, results), reasons
}

func matchActiveRule(rule Rule, snap UserSnapshot, now time.Time) (bool, string) {
	u := snap.User
	switch rule.Type {
	case RuleActiveWithinDays:
		ok := within(u.LastActiveAt, rule.Days, now)
		return ok, fmt.Sprintf("最近 %d 天有活跃", rule.Days)
	case RuleUsedWithinDays:
		ok := within(u.LastUsedAt, rule.Days, now)
		return ok, fmt.Sprintf("最近 %d 天有消费", rule.Days)
	case RuleExtensionWithinDays:
		ok := within(snap.ExtensionAt, rule.Days, now)
		return ok, fmt.Sprintf("最近 %d 天有扩展行为", rule.Days)
	case RuleTotalUsageAtLeast:
		ok := snap.TotalUsage >= rule.Amount
		return ok, fmt.Sprintf("累计消费达到 %.4f", rule.Amount)
	default:
		return false, ""
	}
}

func ReclaimAmount(policy ReclaimPolicy, balance float64) float64 {
	available := balance - policy.ReserveBalance
	if available <= 0 || balance < policy.MinBalance {
		return 0
	}
	var amount float64
	switch policy.Mode {
	case ReclaimPercent:
		amount = balance * policy.Value / 100
	case ReclaimExcess:
		amount = available
	default:
		amount = policy.Value
	}
	if amount > available {
		amount = available
	}
	if policy.MaxPerUser > 0 && amount > policy.MaxPerUser {
		amount = policy.MaxPerUser
	}
	amount = floorMoney(amount)
	if amount <= 0 || amount < policy.MinPerUser {
		return 0
	}
	return amount
}

func Allocate(policy AllocationPolicy, pool float64, users []UserSnapshot) map[int64]float64 {
	out := make(map[int64]float64, len(users))
	pool = floorMoney(pool)
	if pool <= 0 || len(users) == 0 {
		return out
	}
	limit := policy.RecipientLimit
	if limit <= 0 || limit > len(users) {
		limit = len(users)
	}
	users = append([]UserSnapshot(nil), users...)
	sort.SliceStable(users, func(i, j int) bool {
		if users[i].RecentUsage != users[j].RecentUsage {
			return users[i].RecentUsage > users[j].RecentUsage
		}
		return users[i].User.ID < users[j].User.ID
	})
	users = users[:limit]

	weights := make([]float64, len(users))
	totalWeight := 0.0
	for i, user := range users {
		usage := user.RecentUsage
		if usage <= 0 {
			usage = user.TotalUsage
		}
		if usage > 0 {
			weights[i] = math.Sqrt(usage)
			totalWeight += weights[i]
		}
	}

	for i, user := range users {
		var amount float64
		switch policy.Mode {
		case AllocationFixed:
			amount = policy.FixedAmount
		case AllocationUsageWeighted:
			if totalWeight > 0 {
				amount = pool * weights[i] / totalWeight
			} else {
				amount = pool / float64(len(users))
			}
		case AllocationMixed:
			equalPool := pool * policy.EqualRatio / 100
			weightedPool := pool - equalPool
			amount = equalPool / float64(len(users))
			if totalWeight > 0 {
				amount += weightedPool * weights[i] / totalWeight
			} else {
				amount += weightedPool / float64(len(users))
			}
		default:
			amount = pool / float64(len(users))
		}
		if policy.MaxRewardPerUser > 0 && amount > policy.MaxRewardPerUser {
			amount = policy.MaxRewardPerUser
		}
		amount = floorMoney(amount)
		if amount >= policy.MinReward && amount > 0 {
			out[user.User.ID] = amount
		}
	}
	// Rounding and per-user caps may leave carry, but never spend more than the pool.
	total := 0.0
	for _, user := range users {
		amount := out[user.User.ID]
		if total+amount > pool {
			amount = floorMoney(pool - total)
			if amount < policy.MinReward {
				delete(out, user.User.ID)
				continue
			}
			out[user.User.ID] = amount
		}
		total += amount
	}
	return out
}

func combine(logic string, values []bool) bool {
	if len(values) == 0 {
		return false
	}
	if logic == LogicAny {
		for _, v := range values {
			if v {
				return true
			}
		}
		return false
	}
	for _, v := range values {
		if !v {
			return false
		}
	}
	return true
}

func fallbackTime(value *time.Time, fallback time.Time) *time.Time {
	if value != nil && !value.IsZero() {
		return value
	}
	if fallback.IsZero() {
		return nil
	}
	return &fallback
}

func olderThan(value *time.Time, days int, now time.Time) bool {
	if value == nil || value.IsZero() {
		return true
	}
	return !value.After(now.AddDate(0, 0, -days))
}

func within(value *time.Time, days int, now time.Time) bool {
	if value == nil || value.IsZero() {
		return false
	}
	return !value.Before(now.AddDate(0, 0, -days))
}

func floorMoney(v float64) float64 {
	if v <= 0 {
		return 0
	}
	return math.Floor((v+1e-9)*moneyScale) / moneyScale
}
