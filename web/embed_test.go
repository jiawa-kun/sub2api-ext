package web

import (
	"encoding/xml"
	"regexp"
	"strings"
	"testing"
)

func TestResponsiveTaskHistoryAndPaginationContracts(t *testing.T) {
	files := map[string][]string{
		"static/tasks.html":       {"id=\"poolFeature\"", "task pool-task", "box.innerHTML = rewardHTML + items.map"},
		"static/index.html":       {"近 7 次签到记录", "近 7 次抽奖记录", "id=\"lotteryRecent\"", "./create.html", "./works.html", "AI 创作"},
		"static/rewards.html":     {"const LIMIT = 10;"},
		"static/create.html":      {"./api/creative/options", "./api/creative/credentials", "./api/creative/images", "./api/creative/videos", "./api/creative/jobs/${id}", "image_data_url", "id=\"workspaceStage\"", "id=\"resultMedia\"", "class=\"composer\"", "class=\"creative-studio\"", "id=\"credentialDialog\"", "class=\"top\"", "class=\"topnav\"", "class=\"active\" href=\"./create.html\"", "class=\"brand-title\"", "money(data.balance)", "./assets/media.css", "function streamURL", "preload=\"metadata\""},
		"static/works.html":       {"./api/creative/jobs?'", "PAGE_SIZE=10", "page_size:String(PAGE_SIZE)", "method:'DELETE'", "contentURL", "<dialog", "data-delete", "id=\"prevPage\"", "id=\"nextPage\"", "class=\"works-panel\"", "查看", "./assets/media.css", "function createVideoPoster", "class=\"video-poster\"", "state.posterActive<2", "preload=\"auto\""},
		"static/admin.html":       {"lotteryDrawPage={offset:0,limit:10", "ledgerPage={offset:0, limit:10", "<option value=\"10\">10条/页</option>", "id=\"tutorialVisual\"", "contenteditable=\"true\"", "btnTutorialLink", "btnTutorialSource", "insertTutorialLink", "id=\"sec-creative\"", "./api/admin/creative/jobs?page=1&page_size=10", "api_key:pool?'':el('creativeProviderKey')", "同步外部模型", "用户自有 Key", "['480p','720p','1080p']", "价格为 0 时不开放该档"},
		"static/tutorial.html":    {"function enhanceTutorialContent", "a.target='_blank'", "noopener noreferrer"},
		"static/assets/app.css":   {"height: clamp(220px, 22vw, 300px)", "body.app-tasks #list", "grid-template-columns: repeat(3, minmax(0, 1fr))", "flex-direction: column"},
		"static/assets/media.css": {"body.app-creative .creative-studio", "body.app-works .works-panel", "body.app-creative .composer", "position: relative", "body.app-works .video-poster", "aspect-ratio: 16 / 9", "repeat(4, minmax(0, 1fr))", "grid-template-columns: minmax(0, 1fr)", "overflow: hidden", "@media (max-width: 560px)"},
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

func TestSharedApplicationShellContracts(t *testing.T) {
	pages := []string{
		"static/index.html",
		"static/rank.html",
		"static/tasks.html",
		"static/create.html",
		"static/works.html",
		"static/rewards.html",
		"static/tutorial.html",
		"static/home.html",
		"static/admin.html",
	}
	labels := []string{"每日签到", "排行榜", "任务中心", "AI 创作", "我的作品", "我的奖励", "使用教程", "扩展中心", "管理台"}
	canonicalIcons := make(map[string]string, len(labels))
	anchorPattern := regexp.MustCompile(`(?s)<a\b[^>]*>.*?</a>`)
	iconPattern := regexp.MustCompile(`(?s)<svg\b.*?</svg>`)

	for _, name := range pages {
		raw, err := StaticFS.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		html := string(raw)
		start := strings.Index(html, `class="topnav"`)
		if start < 0 {
			t.Fatalf("%s missing shared top navigation", name)
		}
		end := strings.Index(html[start:], `class="brand-title"`)
		if end < 0 {
			t.Fatalf("%s missing shared page heading after navigation", name)
		}
		nav := html[start : start+end]
		anchors := anchorPattern.FindAllString(nav, -1)
		if len(anchors) != len(labels) {
			t.Fatalf("%s has %d top navigation items, want %d", name, len(anchors), len(labels))
		}
		for i, label := range labels {
			anchor := anchors[i]
			if !strings.Contains(anchor, label) {
				t.Fatalf("%s navigation item %d is not %q", name, i+1, label)
			}

			match := iconPattern.FindString(anchor)
			if match == "" {
				t.Fatalf("%s navigation label %q missing inline SVG", name, label)
			}
			icon := strings.Join(strings.Fields(match), " ")
			if want, ok := canonicalIcons[label]; ok && icon != want {
				t.Fatalf("%s navigation SVG for %q differs from the shared icon", name, label)
			}
			canonicalIcons[label] = icon
		}

		if !strings.Contains(html, `class="page-heading-copy"`) {
			t.Fatalf("%s missing shared page heading copy", name)
		}
		themeButton := regexp.MustCompile(`(?s)<button[^>]*id="themeBtn"[^>]*>\s*<svg.*?</svg>\s*<span[^>]*>.*?</span>\s*</button>`)
		if !themeButton.MatchString(html) {
			t.Fatalf("%s theme control must keep its SVG and text span", name)
		}
	}

	cssRaw, err := StaticFS.ReadFile("static/assets/app.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(cssRaw)
	for _, marker := range []string{
		"body.app-page *::before",
		"box-sizing: border-box",
		"body.app-page .page-heading-copy",
		"body.app-page .segmented-tabs",
		"body.app-page .topnav a.nav-extra",
		"@media (max-width: 980px)",
	} {
		if !strings.Contains(css, marker) {
			t.Fatalf("shared application stylesheet missing %q", marker)
		}
	}
}

func TestSharedPageIconsAndSegmentedTabs(t *testing.T) {
	pageIcons := map[string]string{
		"static/index.html":    "checkin-icon-app",
		"static/rank.html":     "ranking-icon-app",
		"static/tasks.html":    "tasks-icon-app",
		"static/create.html":   "creative-icon-app",
		"static/works.html":    "works-icon-app",
		"static/rewards.html":  "rewards-icon-app",
		"static/tutorial.html": "tutorial-icon-app",
		"static/home.html":     "ext-center-icon",
		"static/admin.html":    "ext-center-icon",
	}
	for name, icon := range pageIcons {
		raw, err := StaticFS.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		html := string(raw)
		for _, suffix := range []string{".svg", "-light.svg"} {
			if !strings.Contains(html, "./assets/"+icon+suffix) {
				t.Fatalf("%s missing page icon %s%s", name, icon, suffix)
			}
		}
	}

	for _, name := range []string{"static/rank.html", "static/create.html", "static/works.html", "static/admin.html"} {
		raw, err := StaticFS.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), "segmented-tabs") {
			t.Fatalf("%s missing shared segmented tabs", name)
		}
	}

	for _, name := range []string{
		"static/assets/checkin-icon-app.svg",
		"static/assets/checkin-icon-app-light.svg",
		"static/assets/creative-icon-app.svg",
		"static/assets/creative-icon-app-light.svg",
		"static/assets/works-icon-app.svg",
		"static/assets/works-icon-app-light.svg",
	} {
		raw, err := StaticFS.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		var root struct {
			XMLName xml.Name
		}
		if err := xml.Unmarshal(raw, &root); err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		if root.XMLName.Local != "svg" {
			t.Fatalf("%s root element is %q, want svg", name, root.XMLName.Local)
		}
	}
}

func TestCreativeNavigationContracts(t *testing.T) {
	for _, name := range []string{"static/index.html", "static/rank.html", "static/tasks.html", "static/rewards.html", "static/tutorial.html", "static/home.html", "static/create.html", "static/works.html", "static/admin.html"} {
		raw, err := StaticFS.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), `href="./create.html"`) {
			t.Fatalf("%s missing creative navigation", name)
		}
		if !strings.Contains(string(raw), `href="./works.html"`) {
			t.Fatalf("%s missing works navigation", name)
		}
	}
}

func TestCreativePageUsesSharedHeader(t *testing.T) {
	raw, err := StaticFS.ReadFile("static/create.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(raw)
	for _, forbidden := range []string{`class="nav-menu"`, `class="studio-header"`} {
		if strings.Contains(html, forbidden) {
			t.Fatalf("creative page still uses standalone header marker %q", forbidden)
		}
	}
}

func TestCreativeMediaPagesUseSharedCardShellAndDoNotEmbedCardVideos(t *testing.T) {
	cssRaw, err := StaticFS.ReadFile("static/assets/media.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(cssRaw)
	for _, required := range []string{"body.app-creative .creative-studio,\nbody.app-works .works-panel", "border: 1px solid var(--app-border)", "background: var(--app-surface)", "repeat(4, minmax(0, 1fr))"} {
		if !strings.Contains(css, required) {
			t.Fatalf("media stylesheet missing shared card layout marker %q", required)
		}
	}
	for _, forbidden := range []string{"width: min(1280px, 100%)", "position: sticky", "position: fixed", "repeat(3,minmax(0,1fr))"} {
		if strings.Contains(css, forbidden) {
			t.Fatalf("media stylesheet still contains standalone layout marker %q", forbidden)
		}
	}
	worksRaw, err := StaticFS.ReadFile("static/works.html")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(worksRaw), `class="work-video"`) {
		t.Fatal("works cards still embed visible video elements")
	}
}

func TestCreativeUserPagesHideInternalSettlementLabels(t *testing.T) {
	for _, name := range []string{"static/create.html", "static/works.html"} {
		raw, err := StaticFS.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"Sub2API 结算", "扩展扣费"} {
			if strings.Contains(string(raw), forbidden) {
				t.Fatalf("%s exposes internal settlement label %q", name, forbidden)
			}
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
