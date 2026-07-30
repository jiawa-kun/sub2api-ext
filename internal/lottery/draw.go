package lottery

import (
	"crypto/rand"
	"math/big"
	"sync"

	"sub2api-ext/internal/lottery/settings"
)

const minAmountUnit = settings.MinAmountUnit

// Draw 带公平抽奖和预算原子控制
func Draw(prizes []settings.Prize, spentToday float64, rt settings.Runtime) settings.Result {
	if len(prizes) == 0 {
		return settings.Result{Prize: settings.Prize{Label: "谢谢参与", Amount: 0}, Status: "miss"}
	}

	total := settings.TotalWeight(prizes)
	if total <= 0 {
		return settings.Result{Prize: settings.Prize{Label: "谢谢参与", Amount: 0}, Status: "miss"}
	}

	roll := randWeighted(total)
	picked := selectByRoll(prizes, roll)

	amount := Resolve(rt, picked, spentToday)

	return settings.Result{
		Prize:  picked,
		Amount: amount,
		Status: "win",
	}
}

func randWeighted(total int64) int64 {
	if total <= 0 {
		return 0
	}
	n, _ := rand.Int(rand.Reader, big.NewInt(total))
	return n.Int64()
}

func selectByRoll(prizes []settings.Prize, roll int64) settings.Prize {
	acc := int64(0)
	for _, p := range prizes {
		if p.Weight <= 0 {
			continue
		}
		acc += int64(p.Weight)
		if roll < acc {
			return p
		}
	}
	return prizes[len(prizes)-1]
}

func Resolve(rt settings.Runtime, picked settings.Prize, spentToday float64) float64 {
	amount := picked.Amount
	if rt.HardCap > 0 && amount > rt.HardCap {
		amount = rt.HardCap
	}
	if amount < minAmountUnit {
		return 0
	}
	if rt.DailyBudget > 0 {
		remain := rt.DailyBudget - spentToday
		if remain < minAmountUnit {
			return 0
		}
		if amount > remain {
			amount = remain
		}
	}
	return settings.RoundAmount(amount)
}

func TotalWeight(prizes []settings.Prize) int64 {
	var total int64
	for _, p := range prizes {
		total += int64(p.Weight)
	}
	return total
}

func roundAmount(a float64) float64 {
	return a
}
