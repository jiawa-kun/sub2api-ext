package web

import (
	"strings"
	"testing"
)

func TestLotteryUIKeepsMysteryPresentation(t *testing.T) {
	raw, err := StaticFS.ReadFile("static/index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(raw)

	required := []string{
		"#lotteryBox #lotteryBtn",
		"return String(p&&p.label!=null?p.label:'');",
		"btn.textContent='抽一次'",
		"money(r.amount)",
	}
	for _, text := range required {
		if !strings.Contains(html, text) {
			t.Fatalf("lottery UI contract missing %q", text)
		}
	}

	forbidden := []string{
		"lottery-prize-amount",
		"btn.textContent=Number(s.today_amount)>0?('已到账",
	}
	for _, text := range forbidden {
		if strings.Contains(html, text) {
			t.Fatalf("lottery UI regression found %q", text)
		}
	}
}
