package lottery

import (
	"crypto/rand"
	"math/big"
)

// Draw outcomes returned to the caller.
const (
	StatusWin  = "win"
	StatusMiss = "miss"
)

// Result is the outcome of picking one prize.
type Result struct {
	Prize  Prize
	Amount float64
	Status string
}

type PickedPrize struct {
	Prize Prize
	Index int
}

// Pick selects a prize using crypto/rand weighted sampling.
//
// randFn is injectable so tests can drive a deterministic sequence; nil uses
// crypto/rand. Prizes with weight <= 0 are display-only and never selected.
func Pick(prizes []Prize, randFn func(max int64) int64) Prize {
	return PickWithIndex(prizes, randFn).Prize
}

// PickWithIndex selects a prize and preserves its configured position so the
// client can stop the wheel on the exact segment even when labels repeat.
func PickWithIndex(prizes []Prize, randFn func(max int64) int64) PickedPrize {
	total := TotalWeight(prizes)
	if total <= 0 {
		// Callers normalize first, so this only guards direct misuse.
		return PickedPrize{Prize: Prize{Label: "谢谢参与", Amount: 0, Weight: 1}, Index: -1}
	}
	if randFn == nil {
		randFn = cryptoRandInt63n
	}
	roll := randFn(int64(total))
	if roll < 0 {
		roll = 0
	}
	acc := int64(0)
	for i, p := range prizes {
		if p.Weight <= 0 {
			continue
		}
		acc += int64(p.Weight)
		if roll < acc {
			return PickedPrize{Prize: p, Index: i}
		}
	}
	// Unreachable unless weights mutated concurrently; fall back to the last
	// eligible prize rather than returning a zero value.
	for i := len(prizes) - 1; i >= 0; i-- {
		if prizes[i].Weight > 0 {
			return PickedPrize{Prize: prizes[i], Index: i}
		}
	}
	return PickedPrize{Prize: Prize{Label: "谢谢参与", Amount: 0, Weight: 1}, Index: -1}
}

// Resolve applies hard cap and remaining daily budget to a picked prize.
//
// A winning prize is downgraded to a miss only when the budget cannot cover
// the minimum unit. Partial payouts are allowed so the last bit of budget is
// still usable, matching how check-in treats its own budget.
func Resolve(rt Runtime, picked Prize, spentToday float64) Result {
	amount := picked.Amount
	if rt.HardCap > 0 && amount > rt.HardCap {
		amount = rt.HardCap
	}
	if amount < minAmountUnit {
		return Result{Prize: picked, Amount: 0, Status: StatusMiss}
	}
	if rt.DailyBudget > 0 {
		remain := rt.DailyBudget - spentToday
		if remain < minAmountUnit {
			return Result{Prize: picked, Amount: 0, Status: StatusMiss}
		}
		if amount > remain {
			amount = remain
		}
	}
	amount = roundAmount(amount)
	if amount < minAmountUnit {
		return Result{Prize: picked, Amount: 0, Status: StatusMiss}
	}
	return Result{Prize: picked, Amount: amount, Status: StatusWin}
}

// BudgetExhausted reports whether the remaining budget can no longer fund the
// smallest winning prize, which is when the entrance should be closed.
func BudgetExhausted(rt Runtime, spentToday float64) bool {
	if rt.DailyBudget <= 0 {
		return false
	}
	remain := rt.DailyBudget - spentToday
	if remain < minAmountUnit {
		return true
	}
	return MaxPrizeValue(rt) > 0 && remain < minAmountUnit
}

// cryptoRandInt63n returns a uniform value in [0, max).
func cryptoRandInt63n(max int64) int64 {
	if max <= 0 {
		return 0
	}
	n, err := rand.Int(rand.Reader, big.NewInt(max))
	if err != nil {
		// crypto/rand failure is fatal for fairness; degrade to 0 so the
		// first prize wins rather than panicking a user request.
		return 0
	}
	return n.Int64()
}
