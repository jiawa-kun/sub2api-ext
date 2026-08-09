package web

import (
	"strings"
	"testing"
)

func TestResponsiveTaskHistoryAndPaginationContracts(t *testing.T) {
	files := map[string][]string{
		"static/tasks.html":     {"id=\"poolFeature\"", "task pool-task", "box.innerHTML = rewardHTML + items.map"},
		"static/index.html":     {"近 7 次签到记录", "近 7 次抽奖记录", "id=\"lotteryRecent\"", "./create.html", "AI 创作"},
		"static/rewards.html":   {"const LIMIT = 10;"},
		"static/create.html":    {"./api/creative/options", "./api/creative/credentials", "./api/creative/images", "./api/creative/videos", "./api/creative/jobs?page=1&page_size=10", "image_data_url", "contentBlob", "Sub2API 结算", "扩展扣费"},
		"static/admin.html":     {"lotteryDrawPage={offset:0,limit:10", "ledgerPage={offset:0, limit:10", "<option value=\"10\">10条/页</option>", "id=\"tutorialVisual\"", "contenteditable=\"true\"", "btnTutorialLink", "btnTutorialSource", "insertTutorialLink", "id=\"sec-creative\"", "./api/admin/creative/jobs?page=1&page_size=10", "api_key:pool?'':el('creativeProviderKey')", "同步外部模型", "用户自有 Key"},
		"static/tutorial.html":  {"function enhanceTutorialContent", "a.target='_blank'", "noopener noreferrer"},
		"static/assets/app.css": {"height: clamp(220px, 22vw, 300px)", "body.app-tasks #list", "flex-direction: column"},
	}
	for name, required := range files {
		raw, err := StaticFS.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		text := string(raw)
		for _, marker := range required {
			if !strings.Contains(text, marker) {
				t.Fatalf("%s missing %q", name, marker)
			}
		}
	}
}

func TestCreativeNavigationContracts(t *testing.T) {
	for _, name := range []string{"static/index.html", "static/rank.html", "static/tasks.html", "static/rewards.html", "static/tutorial.html", "static/admin.html"} {
		raw, err := StaticFS.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), `href="./create.html"`) {
			t.Fatalf("%s missing creative navigation", name)
		}
	}
}

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
