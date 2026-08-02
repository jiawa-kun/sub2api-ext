package handler

import (
	"context"
	"fmt"
	"log"
	"time"

	"sub2api-ext/internal/store"
)

// StartCampaignScheduler starts the periodic rank settlement loop.
func (h *Handler) StartCampaignScheduler() {
	h.campaignSchedMu.Lock()
	defer h.campaignSchedMu.Unlock()
	if h.campaignSchedStop != nil {
		return
	}
	h.campaignSchedStop = make(chan struct{})
	stop := h.campaignSchedStop
	h.campaignSchedWG.Add(1)
	go func() {
		defer h.campaignSchedWG.Done()
		h.campaignSchedulerLoop(stop)
	}()
	log.Printf("rank campaign scheduler started timezone=%s", store.CampaignTimezone)
}

func (h *Handler) StopCampaignScheduler() {
	h.campaignSchedMu.Lock()
	stop := h.campaignSchedStop
	h.campaignSchedStop = nil
	h.campaignSchedMu.Unlock()
	if stop != nil {
		close(stop)
		h.campaignSchedWG.Wait()
	}
}

func (h *Handler) campaignSchedulerLoop(stop <-chan struct{}) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	lastMinute := ""
	loc, err := time.LoadLocation(store.CampaignTimezone)
	if err != nil {
		log.Printf("rank campaign scheduler timezone failed: %v", err)
		return
	}
	for {
		select {
		case <-stop:
			return
		case now := <-ticker.C:
			local := now.In(loc)
			minute := local.Format("2006-01-02 15:04")
			if minute == lastMinute {
				continue
			}
			lastMinute = minute
			campaigns, err := h.store.ListRankCampaigns(context.Background(), 1000)
			if err != nil {
				log.Printf("rank campaign scheduler list failed: %v", err)
				continue
			}
			for _, campaign := range campaigns {
				if campaign.Status != store.CampaignStatusActive || store.NormalizeCampaignFrequency(campaign.Frequency) == store.CampaignFrequencyOnce {
					continue
				}
				if !campaignScheduleDue(campaign, local) {
					continue
				}
				period, err := store.PreviousCampaignPeriod(campaign.Frequency, local, loc)
				if err != nil || !store.PeriodInsideCampaign(period, campaign.StartDate, campaign.EndDate) {
					continue
				}
				row, err := h.store.GetCampaignPeriod(context.Background(), campaign.ID, period.Key)
				if err != nil {
					log.Printf("rank campaign scheduler period lookup failed campaign=%d period=%s: %v", campaign.ID, period.Key, err)
					continue
				}
				if row != nil && (row.Status == store.CampaignPeriodSettled || row.Status == store.CampaignPeriodEmpty) {
					continue
				}
				key := campaignRunKey(campaign.ID, period.Key)
				if !h.claimCampaignRun(key) {
					continue
				}
				go h.runScheduledCampaign(campaign, period, key)
			}
		}
	}
}

func campaignScheduleDue(campaign store.RankCampaign, local time.Time) bool {
	schedule, err := time.ParseInLocation("15:04", store.NormalizeCampaignSettlementTime(campaign.SettlementTime), local.Location())
	if err != nil {
		return false
	}
	due := time.Date(local.Year(), local.Month(), local.Day(), schedule.Hour(), schedule.Minute(), 0, 0, local.Location())
	return !local.Before(due)
}

func campaignRunKey(campaignID int64, periodKey string) string {
	return fmt.Sprintf("%d:%s", campaignID, periodKey)
}

func (h *Handler) claimCampaignRun(key string) bool {
	h.campaignRunMu.Lock()
	defer h.campaignRunMu.Unlock()
	if h.campaignRuns[key] {
		return false
	}
	h.campaignRuns[key] = true
	return true
}

func (h *Handler) runScheduledCampaign(campaign store.RankCampaign, period store.CampaignPeriod, key string) {
	defer func() {
		h.campaignRunMu.Lock()
		delete(h.campaignRuns, key)
		h.campaignRunMu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	if _, err := h.settleCampaignPeriod(ctx, &campaign, period, "schedule"); err != nil {
		log.Printf("rank campaign scheduled settlement failed campaign=%d period=%s: %v", campaign.ID, period.Key, err)
	}
}
