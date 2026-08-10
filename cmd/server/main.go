package main

import (
	"context"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"sub2api-ext/internal/config"
	"sub2api-ext/internal/creative"
	"sub2api-ext/internal/credit"
	"sub2api-ext/internal/handler"
	"sub2api-ext/internal/lottery"
	"sub2api-ext/internal/modules"
	"sub2api-ext/internal/notify"
	"sub2api-ext/internal/patrol"
	"sub2api-ext/internal/redistribution"
	"sub2api-ext/internal/report"
	"sub2api-ext/internal/settings"
	"sub2api-ext/internal/store"
	"sub2api-ext/internal/sub2api"
	"sub2api-ext/internal/tasks"
	"sub2api-ext/web"
)

func main() {
	cfgPath := flag.String("config", envOr("CONFIG_PATH", "configs/config.yaml"), "config file path")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	st, err := store.Open(cfg.Store.SQLitePath)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer st.Close()

	stg := settings.New(st, cfg.Checkin)
	if err := stg.PersistClamped(context.Background()); err != nil {
		log.Printf("persist clamped settings: %v", err)
	}
	client := sub2api.NewWithPublicHost(cfg.Sub2API.BaseURL, cfg.Sub2API.AdminToken, cfg.Sub2API.PublicHost, cfg.Timeout())
	patrolSettings := patrol.NewSettings(st, cfg.Patrol)
	patrolSvc := patrol.NewService(client, st, patrolSettings)
	patrolSvc.StartScheduler()
	defer patrolSvc.StopScheduler()

	notifySettings := notify.NewSettings(st, cfg.Notify)
	notifier := notify.NewNotifier(notifySettings)
	notifier.Start()
	defer notifier.Stop()
	patrolSvc.SetNotifier(notifier)

	lotterySettings := lottery.NewSettings(st, cfg.Lottery)

	// The digest reads live budgets through closures so it always reports the
	// value currently in effect, not the one captured at startup.
	reportSettings := report.NewSettings(st, cfg.Report)
	reportSvc := report.NewService(st, reportSettings, notifier, report.Deps{
		CheckinBudget:  func() float64 { return stg.Get().DailyBudget },
		LotteryBudget:  func() float64 { return lotterySettings.Get().DailyBudget },
		LotteryEnabled: func() bool { return lotterySettings.Get().Enabled },
	})
	reportSvc.StartScheduler()
	defer reportSvc.StopScheduler()

	h := handler.New(cfg, st, client, stg, patrolSvc)
	h.SetNotifier(notifier)
	h.SetLottery(lotterySettings)
	h.SetReport(reportSvc)

	creditSvc := credit.New(st, client)
	if n, err := creditSvc.Backfill(context.Background()); err != nil {
		log.Printf("ledger backfill: %v", err)
	} else if n > 0 {
		log.Printf("ledger backfill inserted %d rows", n)
	}
	h.SetCredit(creditSvc)
	h.StartCampaignScheduler()
	defer h.StopCampaignScheduler()
	redistributionSettings := redistribution.NewSettings(st)
	redistributionSvc := redistribution.NewService(st, client, creditSvc, redistributionSettings, notifier)
	redistributionSvc.StartScheduler()
	defer redistributionSvc.StopScheduler()
	h.SetRedistribution(redistributionSvc)
	taskSettings := tasks.NewSettings(st)
	h.SetTasks(taskSettings)
	creativeMediaRoot := filepath.Join(filepath.Dir(cfg.Store.SQLitePath), "creative", "videos")
	creativeSvc := creative.New(st, client, creditSvc, cfg.Security.CreativeCredentialSecret, creativeMediaRoot)
	if len(strings.TrimSpace(cfg.Security.CreativeCredentialSecret)) < 32 {
		log.Printf("creative user API key storage disabled: CREATIVE_CREDENTIAL_SECRET must be at least 32 characters")
	}
	if _, err := creativeSvc.EnsureAccountPoolProvider(context.Background()); err != nil {
		log.Printf("creative account pool bootstrap: %v", err)
	}
	creativeSvc.Start()
	defer creativeSvc.Stop()
	h.SetCreative(creativeSvc)

	mux := http.NewServeMux()
	base := cfg.Server.BasePath

	mux.HandleFunc(base+"/healthz", h.Health)
	mux.HandleFunc(base+"/readyz", h.Ready)
	mux.HandleFunc(base+"/metrics", h.Metrics)
	mux.HandleFunc(base+"/api/modules", h.Modules)
	mux.HandleFunc(base+"/api/status", h.Status)
	mux.HandleFunc(base+"/api/checkin", h.Checkin)
	mux.HandleFunc(base+"/api/calendar", h.Calendar)
	mux.HandleFunc(base+"/api/admin/settings", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			h.AdminGetSettings(w, r)
		case http.MethodPut, http.MethodPost:
			h.AdminUpdateSettings(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc(base+"/api/admin/settings/audit", h.AdminListAudit)
	mux.HandleFunc(base+"/api/admin/settings/rollback", h.AdminRollbackSettings)
	mux.HandleFunc(base+"/api/admin/stats", h.AdminStats)
	mux.HandleFunc(base+"/api/admin/checkins", h.AdminListCheckins)
	mux.HandleFunc(base+"/api/admin/settings/template", h.AdminApplyTemplate)
	mux.HandleFunc(base+"/api/admin/patrol/settings", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			h.AdminGetPatrolSettings(w, r)
		case http.MethodPut, http.MethodPost:
			h.AdminUpdatePatrolSettings(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc(base+"/api/admin/patrol/status", h.AdminPatrolStatus)
	mux.HandleFunc(base+"/api/admin/patrol/accounts", h.AdminPatrolAccounts)
	mux.HandleFunc(base+"/api/admin/patrol/options", h.AdminPatrolOptions)
	mux.HandleFunc(base+"/api/admin/patrol/models", h.AdminPatrolModels)
	mux.HandleFunc(base+"/api/admin/notify/settings", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			h.AdminGetNotifySettings(w, r)
		case http.MethodPut, http.MethodPost:
			h.AdminUpdateNotifySettings(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc(base+"/api/admin/notify/test", h.AdminNotifyTest)
	mux.HandleFunc(base+"/api/lottery/status", h.LotteryStatus)
	mux.HandleFunc(base+"/api/ranking/rewards", h.RankingRewards)

	mux.HandleFunc(base+"/api/me/ledger", h.MeLedger)
	mux.HandleFunc(base+"/api/admin/overview", h.AdminOverview)
	mux.HandleFunc(base+"/api/admin/ledger", h.AdminListLedger)
	mux.HandleFunc(base+"/api/admin/ledger/stats", h.AdminLedgerStats)
	mux.HandleFunc(base+"/api/admin/ledger/export", h.AdminExportLedger)
	mux.HandleFunc(base+"/api/admin/rank/campaigns", h.AdminRankCampaigns)
	mux.HandleFunc(base+"/api/admin/rank/campaigns/", h.AdminRankCampaignByID)
	mux.HandleFunc(base+"/api/ranking/campaigns", h.PublicRankCampaigns)
	mux.HandleFunc(base+"/api/tasks", h.TasksList)
	mux.HandleFunc(base+"/api/tasks/claim", h.TasksClaim)
	mux.HandleFunc(base+"/api/admin/tasks/settings", h.AdminTasksSettings)
	mux.HandleFunc(base+"/api/creative/options", h.CreativeOptions)
	mux.HandleFunc(base+"/api/creative/credentials", h.CreativeCredentials)
	mux.HandleFunc(base+"/api/creative/images", h.CreativeImages)
	mux.HandleFunc(base+"/api/creative/videos", h.CreativeVideos)
	mux.HandleFunc(base+"/api/creative/jobs", h.CreativeJobs)
	mux.HandleFunc(base+"/api/creative/jobs/", h.CreativeJobByID)
	mux.HandleFunc(base+"/api/admin/creative/providers", h.AdminCreativeProviders)
	mux.HandleFunc(base+"/api/admin/creative/providers/", h.AdminCreativeProviderByID)
	mux.HandleFunc(base+"/api/admin/creative/account-pool", h.AdminCreativeAccountPool)
	mux.HandleFunc(base+"/api/admin/creative/models", h.AdminCreativeModels)
	mux.HandleFunc(base+"/api/admin/creative/users", h.AdminCreativeUsers)
	mux.HandleFunc(base+"/api/admin/creative/jobs", h.AdminCreativeJobs)
	mux.HandleFunc(base+"/api/admin/creative/jobs/", h.AdminCreativeJobByID)
	mux.HandleFunc(base+"/api/ranking/consumption", h.RankingConsumption)
	mux.HandleFunc(base+"/api/lottery/draw", h.LotteryDraw)
	mux.HandleFunc(base+"/api/admin/lottery/settings", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			h.AdminGetLotterySettings(w, r)
		case http.MethodPut, http.MethodPost:
			h.AdminUpdateLotterySettings(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc(base+"/api/admin/lottery/draws", h.AdminLotteryDraws)
	mux.HandleFunc(base+"/api/admin/lottery/stats", h.AdminLotteryStats)
	mux.HandleFunc(base+"/api/admin/report/settings", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			h.AdminGetReportSettings(w, r)
		case http.MethodPut, http.MethodPost:
			h.AdminUpdateReportSettings(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc(base+"/api/admin/report/preview", h.AdminReportPreview)
	mux.HandleFunc(base+"/api/admin/report/send", h.AdminReportSend)
	mux.HandleFunc(base+"/api/admin/redistribution/settings", h.AdminRedistributionSettings)
	mux.HandleFunc(base+"/api/admin/redistribution/preview", h.AdminRedistributionPreview)
	mux.HandleFunc(base+"/api/admin/redistribution/execute", h.AdminRedistributionExecute)
	mux.HandleFunc(base+"/api/admin/redistribution/stop", h.AdminRedistributionStop)
	mux.HandleFunc(base+"/api/admin/redistribution/batches", h.AdminRedistributionBatches)
	mux.HandleFunc(base+"/api/admin/redistribution/batches/", h.AdminRedistributionBatchByID)
	mux.HandleFunc(base+"/api/redistribution/rewards", h.RedistributionRewards)
	mux.HandleFunc(base+"/api/redistribution/rewards/claim", h.RedistributionClaim)
	mux.HandleFunc(base+"/api/redistribution/pool", h.RedistributionPool)
	mux.HandleFunc(base+"/api/redistribution/recover", h.RedistributionRecover)
	mux.HandleFunc(base+"/api/admin/patrol/run", h.AdminPatrolRun)
	mux.HandleFunc(base+"/api/admin/patrol/stop", h.AdminPatrolStop)

	mux.HandleFunc(base+"/api/pages/tutorial", h.PublicGetTutorial)
	mux.HandleFunc(base+"/api/admin/pages/tutorial", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			h.AdminGetTutorial(w, r)
		case http.MethodPut, http.MethodPost:
			h.AdminSetTutorial(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc(base+"/api/admin/pages/tutorial/assets", h.AdminUploadTutorialAsset)
	mux.HandleFunc(base+"/tutorial-assets/", h.PublicTutorialAsset)

	staticRoot, err := fs.Sub(web.StaticFS, "static")
	if err != nil {
		log.Fatalf("static fs: %v", err)
	}
	fileServer := http.FileServer(http.FS(staticRoot))
	mux.Handle(base+"/", http.StripPrefix(base+"/", spaFallback(fileServer, staticRoot)))

	srv := &http.Server{
		Addr:              cfg.Server.Addr,
		Handler:           withEmbedHeaders(cfg, withCORS(cfg, withLogging(mux))),
		ReadHeaderTimeout: 10 * time.Second,
	}

	rt := stg.Get()
	if rt.RewardMode == "random" || rt.RandomReward {
		log.Printf("sub2api-ext (sub2api-ext) listening on %s base_path=%q mode=random reward=[%.4f,%.4f] tz=%s enabled=%v",
			cfg.Server.Addr, cfg.Server.BasePath, rt.RewardMin, rt.RewardMax, rt.Timezone, rt.Enabled)
	} else {
		log.Printf("sub2api-ext (sub2api-ext) listening on %s base_path=%q mode=fixed reward=%.4f tz=%s enabled=%v",
			cfg.Server.Addr, cfg.Server.BasePath, rt.RewardAmount, rt.Timezone, rt.Enabled)
	}
	log.Printf("product=%s modules=%v sub2api base_url=%s hard_cap=%.4f daily_budget=%.4f streak=%v step=%.4f",
		modules.ProductID, modules.ActiveIDs(), cfg.Sub2API.BaseURL, rt.HardCap, rt.DailyBudget, rt.StreakEnabled, rt.StreakStep)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server: %v", err)
	}
}

func spaFallback(next http.Handler, fsys fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(fsys, path); err != nil {
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/"
			next.ServeHTTP(w, r2)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start))
	})
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func withCORS(cfg config.Config, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := strings.TrimSpace(r.Header.Get("Origin"))
		if origin == "" {
			next.ServeHTTP(w, r)
			return
		}
		if !cfg.OriginAllowed(origin) {
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-User-Token, x-api-key")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, OPTIONS")
		w.Header().Set("Vary", "Origin")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func withEmbedHeaders(cfg config.Config, next http.Handler) http.Handler {
	csp := cfg.CSPFrameAncestors()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", csp)
		w.Header().Del("X-Frame-Options")
		next.ServeHTTP(w, r)
	})
}
