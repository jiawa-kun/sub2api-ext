package metrics

import (
	"encoding/json"
	"net/http"
	"sync/atomic"
)

var (
	CheckinSuccess   atomic.Int64
	CheckinAlready   atomic.Int64
	CheckinDisabled  atomic.Int64
	CheckinBudget    atomic.Int64
	CheckinFail      atomic.Int64
	AuthFail         atomic.Int64
	CreditRetry      atomic.Int64
	CreditIdempotent atomic.Int64
)

type Snapshot struct {
	CheckinSuccess   int64 `json:"checkin_success"`
	CheckinAlready   int64 `json:"checkin_already"`
	CheckinDisabled  int64 `json:"checkin_disabled"`
	CheckinBudget    int64 `json:"checkin_budget_exhausted"`
	CheckinFail      int64 `json:"checkin_fail"`
	AuthFail         int64 `json:"auth_fail"`
	CreditRetry      int64 `json:"credit_retry"`
	CreditIdempotent int64 `json:"credit_idempotent_hit"`
}

func Get() Snapshot {
	return Snapshot{
		CheckinSuccess:   CheckinSuccess.Load(),
		CheckinAlready:   CheckinAlready.Load(),
		CheckinDisabled:  CheckinDisabled.Load(),
		CheckinBudget:    CheckinBudget.Load(),
		CheckinFail:      CheckinFail.Load(),
		AuthFail:         AuthFail.Load(),
		CreditRetry:      CreditRetry.Load(),
		CreditIdempotent: CreditIdempotent.Load(),
	}
}

func WriteJSON(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(Get())
}

func WritePrometheus(w http.ResponseWriter) {
	s := Get()
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = w.Write([]byte(
		"# HELP checkin_success_total successful check-ins\n" +
			"# TYPE checkin_success_total counter\n" +
			"checkin_success_total " + itoa(s.CheckinSuccess) + "\n" +
			"checkin_already_total " + itoa(s.CheckinAlready) + "\n" +
			"checkin_disabled_total " + itoa(s.CheckinDisabled) + "\n" +
			"checkin_budget_exhausted_total " + itoa(s.CheckinBudget) + "\n" +
			"checkin_fail_total " + itoa(s.CheckinFail) + "\n" +
			"auth_fail_total " + itoa(s.AuthFail) + "\n" +
			"credit_retry_total " + itoa(s.CreditRetry) + "\n" +
			"credit_idempotent_hit_total " + itoa(s.CreditIdempotent) + "\n",
	))
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	var b [32]byte
	i := len(b)
	n := v
	if n < 0 {
		n = -n
	}
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if v < 0 {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
