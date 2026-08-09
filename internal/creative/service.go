package creative

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"sub2api-ext/internal/credit"
	"sub2api-ext/internal/store"
	"sub2api-ext/internal/sub2api"
)

const (
	CapabilityImage = "image_generation"
	CapabilityVideo = "video_generation"
	ProtocolImages  = "openai_images"
	ProtocolVideo   = "openai_video_task"
	ProviderOpenAI  = "openai_compatible"
	ProviderPool    = "sub2api_pool"
	SourceCharge    = "creative_charge"
	SourceRefund    = "creative_refund"
)

type Pricing struct {
	Currency    string             `json:"currency"`
	Unit        string             `json:"unit"`
	Fixed       float64            `json:"fixed,omitempty"`
	Resolutions map[string]float64 `json:"resolutions,omitempty"`
	InputImage  float64            `json:"input_image,omitempty"`
	Source      string             `json:"source,omitempty"`
	AsOf        string             `json:"as_of,omitempty"`
}
type Constraints struct {
	Resolutions  []string `json:"resolutions,omitempty"`
	AspectRatios []string `json:"aspect_ratios,omitempty"`
	MaxImages    int      `json:"max_images,omitempty"`
	DurationMin  int      `json:"duration_min,omitempty"`
	DurationMax  int      `json:"duration_max,omitempty"`
	MaxUploadMB  int      `json:"max_upload_mb,omitempty"`
	SupportsEdit bool     `json:"supports_edit,omitempty"`
}
type ImageInput struct {
	ModelDBID    int64  `json:"model_id"`
	Prompt       string `json:"prompt"`
	Count        int    `json:"n"`
	AspectRatio  string `json:"aspect_ratio"`
	Resolution   string `json:"resolution"`
	ImageDataURL string `json:"image_data_url,omitempty"`
	RequestKey   string `json:"request_key"`
}
type VideoInput struct {
	ModelDBID    int64  `json:"model_id"`
	Prompt       string `json:"prompt"`
	Duration     int    `json:"duration"`
	AspectRatio  string `json:"aspect_ratio"`
	Resolution   string `json:"resolution"`
	ImageDataURL string `json:"image_data_url,omitempty"`
	RequestKey   string `json:"request_key"`
}
type ModelOption struct {
	store.CreativeModel
	Pricing      Pricing     `json:"pricing"`
	Constraints  Constraints `json:"constraints"`
	ProviderName string      `json:"provider_name"`
}

type Service struct {
	store    *store.Store
	accounts *sub2api.Client
	credit   *credit.Service
	client   *http.Client
	stop     chan struct{}
	wg       sync.WaitGroup
	pollMu   sync.Mutex
}

func New(st *store.Store, accounts *sub2api.Client, creditSvc *credit.Service) *Service {
	return &Service{store: st, accounts: accounts, credit: creditSvc, client: &http.Client{Timeout: 5 * time.Minute}, stop: make(chan struct{})}
}
func (s *Service) Start() {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		t := time.NewTicker(10 * time.Second)
		defer t.Stop()
		s.pollPending(context.Background())
		for {
			select {
			case <-s.stop:
				return
			case <-t.C:
				s.pollPending(context.Background())
			}
		}
	}()
}
func (s *Service) Stop() {
	select {
	case <-s.stop:
	default:
		close(s.stop)
	}
	s.wg.Wait()
}

func (s *Service) ListProviders(ctx context.Context) ([]store.CreativeProvider, error) {
	return s.store.ListCreativeProviders(ctx)
}
func (s *Service) SaveProvider(ctx context.Context, p store.CreativeProvider) (*store.CreativeProvider, error) {
	p.Kind = defaultString(strings.TrimSpace(p.Kind), ProviderOpenAI)
	if p.Kind != ProviderOpenAI && p.Kind != ProviderPool {
		return nil, fmt.Errorf("暂不支持 Provider 类型 %s", p.Kind)
	}
	if p.Kind == ProviderPool {
		if s.accounts == nil {
			return nil, fmt.Errorf("Sub2API 客户端未配置")
		}
		p.Name = defaultString(strings.TrimSpace(p.Name), "Sub2API 账号池")
		p.BaseURL = s.accounts.BaseURL()
	}
	if p.ID > 0 && strings.TrimSpace(p.APIKey) == "" {
		if current, err := s.store.GetCreativeProvider(ctx, p.ID); err == nil {
			p.APIKey = current.APIKey
		}
	}
	if p.Enabled && strings.TrimSpace(p.APIKey) == "" {
		return nil, fmt.Errorf("启用渠道前必须配置媒体调用 API Key")
	}
	return s.store.SaveCreativeProvider(ctx, p)
}
func (s *Service) DeleteProvider(ctx context.Context, id int64) error {
	return s.store.DeleteCreativeProvider(ctx, id)
}
func (s *Service) ListModels(ctx context.Context, pid int64, enabled bool) ([]store.CreativeModel, error) {
	models, err := s.store.ListCreativeModels(ctx, pid, enabled)
	if err != nil || pid <= 0 {
		return models, err
	}
	p, err := s.store.GetCreativeProvider(ctx, pid)
	if err != nil || p.Kind != ProviderPool {
		return models, err
	}
	availability, discoverErr := s.discoverAccountModels(ctx, *p)
	if discoverErr != nil {
		return models, nil
	}
	for i := range models {
		models[i].AvailableAccounts = availability[models[i].ModelID]
	}
	return models, nil
}
func (s *Service) SaveModel(ctx context.Context, m store.CreativeModel) (*store.CreativeModel, error) {
	if _, err := decodePricing(m.PriceJSON); err != nil {
		return nil, err
	}
	if _, err := decodeConstraints(m.ConstraintsJSON); err != nil {
		return nil, err
	}
	return s.store.UpsertCreativeModel(ctx, m)
}
func (s *Service) ListJobs(ctx context.Context, f store.CreativeJobFilter) ([]store.CreativeJob, int, error) {
	return s.store.ListCreativeJobs(ctx, f)
}
func (s *Service) GetJob(ctx context.Context, id, userID int64) (*store.CreativeJob, error) {
	return s.store.GetCreativeJob(ctx, id, userID)
}

func (s *Service) ModelOptions(ctx context.Context) ([]ModelOption, error) {
	models, err := s.store.ListCreativeModels(ctx, 0, true)
	if err != nil {
		return nil, err
	}
	providers, err := s.store.ListCreativeProviders(ctx)
	if err != nil {
		return nil, err
	}
	pm := map[int64]string{}
	availability := map[int64]map[string]int{}
	for _, p := range providers {
		if p.Enabled {
			pm[p.ID] = p.Name
			if p.Kind == ProviderPool {
				if counts, e := s.discoverAccountModels(ctx, p); e == nil {
					availability[p.ID] = counts
				} else {
					availability[p.ID] = map[string]int{}
				}
			}
		}
	}
	out := []ModelOption{}
	for _, m := range models {
		pn, ok := pm[m.ProviderID]
		if !ok {
			continue
		}
		p, e := decodePricing(m.PriceJSON)
		if e != nil {
			continue
		}
		if counts, isPool := availability[m.ProviderID]; isPool {
			m.AvailableAccounts = counts[m.ModelID]
			if m.AvailableAccounts <= 0 {
				continue
			}
		}
		var c Constraints
		_ = json.Unmarshal([]byte(m.ConstraintsJSON), &c)
		out = append(out, ModelOption{CreativeModel: m, Pricing: p, Constraints: c, ProviderName: pn})
	}
	return out, nil
}

func (s *Service) SyncProviderModels(ctx context.Context, providerID int64) (int, error) {
	p, err := s.store.GetCreativeProvider(ctx, providerID)
	if err != nil {
		return 0, err
	}
	set := map[string]bool{}
	if p.Kind == ProviderPool {
		counts, discoverErr := s.discoverAccountModels(ctx, *p)
		if discoverErr != nil {
			return 0, fmt.Errorf("account models: %w", discoverErr)
		}
		for modelID := range counts {
			set[modelID] = true
		}
	} else {
		remote, remoteErr := s.listRemoteModels(ctx, *p)
		if remoteErr != nil {
			return 0, fmt.Errorf("provider models: %w", remoteErr)
		}
		remoteSet := map[string]bool{}
		for _, m := range remote {
			remoteSet[m] = true
		}
		accounts, accountsErr := s.accounts.ListAllAccounts(ctx, p.SourceGroup, 100, "Asia/Shanghai")
		if accountsErr != nil {
			return 0, fmt.Errorf("account models: %w", accountsErr)
		}
		for _, account := range accounts {
			if !account.Schedulable || badAccountStatus(account.Status) {
				continue
			}
			for _, modelID := range account.ModelMappingKeys() {
				if remoteSet[modelID] {
					set[modelID] = true
				}
			}
		}
	}
	count := 0
	for modelID := range set {
		capability, protocol, pricing, constraints, known := inferModel(modelID)
		rawPrice, _ := json.Marshal(pricing)
		rawConstraints, _ := json.Marshal(constraints)
		_, err := s.store.UpsertCreativeModel(ctx, store.CreativeModel{ProviderID: p.ID, ModelID: modelID, DisplayName: modelID, Capability: capability, Protocol: protocol, PriceJSON: string(rawPrice), ConstraintsJSON: string(rawConstraints), SourceGroup: p.SourceGroup, Enabled: known})
		if err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

func (s *Service) discoverAccountModels(ctx context.Context, p store.CreativeProvider) (map[string]int, error) {
	accounts, err := s.accounts.ListAllAccounts(ctx, p.SourceGroup, 100, "Asia/Shanghai")
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	for _, account := range accounts {
		if !account.Schedulable || badAccountStatus(account.Status) {
			continue
		}
		for _, modelID := range account.ModelMappingKeys() {
			counts[modelID]++
		}
	}
	return counts, nil
}

type AccountPoolOverview struct {
	Groups          []sub2api.Group `json:"groups"`
	Accounts        int             `json:"accounts"`
	HealthyAccounts int             `json:"healthy_accounts"`
	Models          map[string]int  `json:"models"`
}

func (s *Service) AccountPoolOverview(ctx context.Context, group string) (AccountPoolOverview, error) {
	groups, groupsErr := s.accounts.ListGroups(ctx, "")
	accounts, err := s.accounts.ListAllAccounts(ctx, strings.TrimSpace(group), 100, "Asia/Shanghai")
	if err != nil {
		return AccountPoolOverview{}, err
	}
	out := AccountPoolOverview{Groups: groups, Accounts: len(accounts), Models: map[string]int{}}
	if groupsErr != nil {
		out.Groups = []sub2api.Group{}
	}
	for _, account := range accounts {
		if !account.Schedulable || badAccountStatus(account.Status) {
			continue
		}
		out.HealthyAccounts++
		for _, modelID := range account.ModelMappingKeys() {
			capability, _, _, _, known := inferModel(modelID)
			if known && (capability == CapabilityImage || capability == CapabilityVideo) {
				out.Models[modelID]++
			}
		}
	}
	return out, nil
}
func badAccountStatus(v string) bool {
	v = strings.ToLower(strings.TrimSpace(v))
	return strings.Contains(v, "disable") || strings.Contains(v, "suspend") || strings.Contains(v, "inactive") || strings.Contains(v, "error") || strings.Contains(v, "删除") || strings.Contains(v, "暂停")
}

func inferModel(id string) (string, string, Pricing, Constraints, bool) {
	n := normalizeModel(id)
	imageC := Constraints{Resolutions: []string{"1k", "2k"}, AspectRatios: []string{"1:1", "16:9", "9:16", "4:3", "3:4", "3:2", "2:3"}, MaxImages: 4, MaxUploadMB: 5}
	videoC := Constraints{Resolutions: []string{"480p", "720p"}, AspectRatios: []string{"1:1", "16:9", "9:16", "4:3", "3:4", "3:2", "2:3"}, DurationMin: 1, DurationMax: 15, MaxUploadMB: 5}
	switch n {
	case "grok-imagine-image":
		imageC.SupportsEdit = true
		return CapabilityImage, ProtocolImages, Pricing{Currency: "USD", Unit: "image", Fixed: .02, InputImage: .002, Source: "xAI official", AsOf: "2026-07-14"}, imageC, true
	case "grok-imagine-image-lite":
		return CapabilityImage, ProtocolImages, Pricing{Currency: "USD", Unit: "image", Fixed: .02, Source: "xAI official", AsOf: "2026-07-14"}, imageC, true
	case "grok-imagine-image-quality":
		imageC.SupportsEdit = true
		return CapabilityImage, ProtocolImages, Pricing{Currency: "USD", Unit: "image", Resolutions: map[string]float64{"1k": .05, "2k": .07}, InputImage: .01, Source: "xAI official", AsOf: "2026-07-14"}, imageC, true
	case "grok-imagine-image-quality-lite":
		return CapabilityImage, ProtocolImages, Pricing{Currency: "USD", Unit: "image", Resolutions: map[string]float64{"1k": .05, "2k": .07}, Source: "xAI official", AsOf: "2026-07-14"}, imageC, true
	case "grok-imagine-video", "grok-imagine-video-1.5":
		return CapabilityVideo, ProtocolVideo, Pricing{Currency: "USD", Unit: "second", Resolutions: map[string]float64{"480p": .08, "720p": .14}, Source: "xAI official", AsOf: "2026-07-14"}, videoC, true
	}
	if strings.Contains(n, "image") || strings.Contains(n, "flux") || strings.Contains(n, "imagen") || strings.Contains(n, "dall-e") {
		return CapabilityImage, ProtocolImages, Pricing{Currency: "USD", Unit: "image"}, imageC, false
	}
	if strings.Contains(n, "video") || strings.Contains(n, "veo") || strings.Contains(n, "kling") {
		return CapabilityVideo, ProtocolVideo, Pricing{Currency: "USD", Unit: "second"}, videoC, false
	}
	return "unknown", "", Pricing{Currency: "USD"}, Constraints{}, false
}
func normalizeModel(id string) string {
	v := strings.ToLower(strings.TrimSpace(id))
	for _, p := range []string{"build/", "web/", "console/", "grok_build/", "grok_web/", "grok_console/"} {
		v = strings.TrimPrefix(v, p)
	}
	return v
}

func (s *Service) TestProvider(ctx context.Context, id int64) error {
	p, err := s.store.GetCreativeProvider(ctx, id)
	if err != nil {
		return err
	}
	if p.Kind == ProviderPool {
		models, discoverErr := s.discoverAccountModels(ctx, *p)
		if discoverErr != nil {
			return discoverErr
		}
		if len(models) == 0 {
			return fmt.Errorf("所选账号分组没有可用媒体模型")
		}
	}
	_, err = s.listRemoteModels(ctx, *p)
	return err
}
func (s *Service) listRemoteModels(ctx context.Context, p store.CreativeProvider) ([]string, error) {
	if p.Kind != ProviderOpenAI && p.Kind != ProviderPool {
		return nil, fmt.Errorf("unsupported provider kind %s", p.Kind)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.providerEndpoint(p, "/v1/models"), nil)
	if err != nil {
		return nil, err
	}
	applyProviderAuth(req, p)
	s.prepareProviderRequest(req, p)
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(body, 400))
	}
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return nil, fmt.Errorf("invalid model response")
	}
	out := []string{}
	for _, m := range payload.Data {
		if strings.TrimSpace(m.ID) != "" {
			out = append(out, m.ID)
		}
	}
	return out, nil
}

func (s *Service) GenerateImage(ctx context.Context, userID int64, in ImageInput) (*store.CreativeJob, error) {
	in.Prompt = strings.TrimSpace(in.Prompt)
	if in.Prompt == "" {
		return nil, fmt.Errorf("prompt is required")
	}
	if in.Count <= 0 {
		in.Count = 1
	}
	if in.Count > 10 {
		return nil, fmt.Errorf("n must be between 1 and 10")
	}
	in.AspectRatio = defaultString(strings.TrimSpace(in.AspectRatio), "1:1")
	in.Resolution = strings.ToLower(defaultString(strings.TrimSpace(in.Resolution), "1k"))
	m, p, price, err := s.resolveModel(ctx, in.ModelDBID, CapabilityImage, in.Resolution, in.Count)
	if err != nil {
		return nil, err
	}
	if err := validateModelConstraints(m, in.AspectRatio, in.Resolution, in.Count); err != nil {
		return nil, err
	}
	if in.ImageDataURL != "" {
		constraints, _ := decodeConstraints(m.ConstraintsJSON)
		if !constraints.SupportsEdit {
			return nil, fmt.Errorf("该模型不支持图片编辑")
		}
		pricing, pricingErr := decodePricing(m.PriceJSON)
		if pricingErr != nil || pricing.InputImage <= 0 {
			return nil, fmt.Errorf("模型没有有效的图片编辑价格")
		}
		price = roundMoney(price + pricing.InputImage)
	}
	params, _ := json.Marshal(map[string]any{
		"model_id": in.ModelDBID, "prompt": in.Prompt, "n": in.Count,
		"aspect_ratio": in.AspectRatio, "resolution": in.Resolution,
		"has_reference_image": in.ImageDataURL != "",
	})
	job, existing, err := s.prepareJob(ctx, userID, p.ID, m.ModelID, "image", in.Prompt, string(params), in.RequestKey, price)
	if err != nil || existing {
		return job, err
	}
	payload := map[string]any{"model": m.ModelID, "prompt": in.Prompt, "n": in.Count, "aspect_ratio": in.AspectRatio, "resolution": in.Resolution, "response_format": "url", "stream": false}
	path := "/v1/images/generations"
	if in.ImageDataURL != "" {
		path = "/v1/images/edits"
		payload["image"] = map[string]any{"url": in.ImageDataURL}
	}
	body, status, err := s.providerJSON(ctx, p, http.MethodPost, path, payload)
	if err != nil || status >= 300 {
		return s.failAndRefund(ctx, job, "upstream_error", providerError(status, body, err))
	}
	var parsed struct {
		Data []map[string]any `json:"data"`
	}
	if json.Unmarshal(body, &parsed) != nil || len(parsed.Data) == 0 {
		return s.failAndRefund(ctx, job, "invalid_response", fmt.Errorf("图片接口未返回图片"))
	}
	now := time.Now().UTC()
	job.ResultJSON = string(body)
	job.UpstreamStatus = "done"
	job.Status = store.CreativeJobCompleted
	job.Progress = 100
	job.CompletedAt = &now
	if err := s.store.UpdateCreativeJob(ctx, *job); err != nil {
		return job, err
	}
	_ = s.store.AddCreativeJobEvent(ctx, store.CreativeJobEvent{JobID: job.ID, EventType: "completed", Message: "图片生成完成"})
	return job, nil
}

func (s *Service) CreateVideo(ctx context.Context, userID int64, in VideoInput) (*store.CreativeJob, error) {
	in.Prompt = strings.TrimSpace(in.Prompt)
	if in.Prompt == "" && in.ImageDataURL == "" {
		return nil, fmt.Errorf("prompt or image is required")
	}
	if in.Duration <= 0 {
		in.Duration = 8
	}
	if in.Duration > 60 {
		return nil, fmt.Errorf("duration must be between 1 and 60")
	}
	in.AspectRatio = defaultString(strings.TrimSpace(in.AspectRatio), "16:9")
	in.Resolution = strings.ToLower(defaultString(strings.TrimSpace(in.Resolution), "720p"))
	m, p, price, err := s.resolveModel(ctx, in.ModelDBID, CapabilityVideo, in.Resolution, in.Duration)
	if err != nil {
		return nil, err
	}
	if err := validateModelConstraints(m, in.AspectRatio, in.Resolution, in.Duration); err != nil {
		return nil, err
	}
	params, _ := json.Marshal(map[string]any{
		"model_id": in.ModelDBID, "prompt": in.Prompt, "duration": in.Duration,
		"aspect_ratio": in.AspectRatio, "resolution": in.Resolution,
		"has_reference_image": in.ImageDataURL != "",
	})
	job, existing, err := s.prepareJob(ctx, userID, p.ID, m.ModelID, "video", in.Prompt, string(params), in.RequestKey, price)
	if err != nil || existing {
		return job, err
	}
	payload := map[string]any{"model": m.ModelID, "prompt": in.Prompt, "duration": in.Duration, "aspect_ratio": in.AspectRatio, "resolution": in.Resolution}
	if in.ImageDataURL != "" {
		payload["image"] = map[string]any{"url": in.ImageDataURL}
	}
	body, status, err := s.providerJSON(ctx, p, http.MethodPost, "/v1/videos/generations", payload)
	if err != nil || status >= 300 {
		return s.failAndRefund(ctx, job, "upstream_error", providerError(status, body, err))
	}
	var parsed struct {
		RequestID string `json:"request_id"`
	}
	if json.Unmarshal(body, &parsed) != nil || parsed.RequestID == "" {
		return s.failAndRefund(ctx, job, "invalid_response", fmt.Errorf("视频接口未返回 request_id"))
	}
	job.UpstreamRequestID = parsed.RequestID
	job.UpstreamStatus = "pending"
	job.ResultJSON = string(body)
	job.Status = store.CreativeJobProcessing
	job.Progress = 0
	if err := s.store.UpdateCreativeJob(ctx, *job); err != nil {
		return job, err
	}
	_ = s.store.AddCreativeJobEvent(ctx, store.CreativeJobEvent{JobID: job.ID, EventType: "submitted", Message: "视频任务已提交"})
	return job, nil
}

func (s *Service) resolveModel(ctx context.Context, id int64, capability, resolution string, quantity int) (store.CreativeModel, store.CreativeProvider, float64, error) {
	m, err := s.store.GetCreativeModel(ctx, id)
	if err != nil {
		return store.CreativeModel{}, store.CreativeProvider{}, 0, err
	}
	if !m.Enabled || m.Capability != capability {
		return *m, store.CreativeProvider{}, 0, fmt.Errorf("模型未启用或能力不匹配")
	}
	expectedProtocol := ProtocolImages
	if capability == CapabilityVideo {
		expectedProtocol = ProtocolVideo
	}
	if m.Protocol != expectedProtocol {
		return *m, store.CreativeProvider{}, 0, fmt.Errorf("模型协议不匹配：需要 %s", expectedProtocol)
	}
	p, err := s.store.GetCreativeProvider(ctx, m.ProviderID)
	if err != nil {
		return *m, store.CreativeProvider{}, 0, err
	}
	if !p.Enabled {
		return *m, *p, 0, fmt.Errorf("Provider 未启用")
	}
	pricing, err := decodePricing(m.PriceJSON)
	if err != nil {
		return *m, *p, 0, err
	}
	unit := pricing.Fixed
	if resolution != "" && pricing.Resolutions != nil {
		unit = pricing.Resolutions[strings.ToLower(resolution)]
	}
	if unit <= 0 {
		return *m, *p, 0, fmt.Errorf("模型没有有效价格")
	}
	return *m, *p, roundMoney(unit * float64(quantity)), nil
}
func (s *Service) prepareJob(ctx context.Context, userID, providerID int64, modelID, mediaType, prompt, params, requestKey string, amount float64) (*store.CreativeJob, bool, error) {
	requestKey = strings.TrimSpace(requestKey)
	if requestKey == "" {
		requestKey = newToken(12)
	}
	if old, err := s.store.GetCreativeJobByRequestKey(ctx, userID, requestKey); err == nil {
		return old, true, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, err
	}
	order := "cr_" + newToken(12)
	job, err := s.store.CreateCreativeJob(ctx, store.CreativeJob{OrderNo: order, RequestKey: requestKey, UserID: userID, ProviderID: providerID, ModelID: modelID, MediaType: mediaType, Prompt: prompt, ParamsJSON: params, ChargeAmount: amount, ChargeStatus: "pending", Status: store.CreativeJobCreated})
	if err != nil {
		return nil, false, err
	}
	_ = s.store.AddCreativeJobEvent(ctx, store.CreativeJobEvent{JobID: job.ID, EventType: "created", Message: "订单已创建"})
	if s.credit == nil {
		return s.failNoRefund(ctx, job, "billing_unavailable", fmt.Errorf("余额服务不可用"))
	}
	res, err := s.credit.Reclaim(ctx, credit.Request{UserID: userID, Amount: amount, Source: SourceCharge, SourceRef: order, Scope: SourceCharge, Slot: order, Notes: fmt.Sprintf("AI创作扣费 %s -%.4f", order, amount)})
	if err != nil {
		return s.failNoRefund(ctx, job, "charge_failed", err)
	}
	job.ChargeStatus = "charged"
	if res != nil {
		job.ChargeLedgerID = res.LedgerID
	}
	job.Status = store.CreativeJobProcessing
	durableCtx, cancel := durableContext(ctx)
	defer cancel()
	if err := s.store.UpdateCreativeJob(durableCtx, *job); err != nil {
		return job, false, err
	}
	_ = s.store.AddCreativeJobEvent(durableCtx, store.CreativeJobEvent{JobID: job.ID, EventType: "charged", Message: fmt.Sprintf("已扣除 %.4f", amount)})
	return job, false, nil
}
func (s *Service) failNoRefund(ctx context.Context, job *store.CreativeJob, code string, cause error) (*store.CreativeJob, bool, error) {
	durableCtx, cancel := durableContext(ctx)
	defer cancel()
	now := time.Now().UTC()
	job.Status = store.CreativeJobFailed
	job.ErrorCode = code
	job.ErrorMessage = cause.Error()
	job.CompletedAt = &now
	_ = s.store.UpdateCreativeJob(durableCtx, *job)
	return job, false, cause
}
func (s *Service) failAndRefund(ctx context.Context, job *store.CreativeJob, code string, cause error) (*store.CreativeJob, error) {
	durableCtx, cancel := durableContext(ctx)
	defer cancel()
	now := time.Now().UTC()
	job.Status = store.CreativeJobFailed
	job.ErrorCode = code
	job.ErrorMessage = cause.Error()
	job.CompletedAt = &now
	if job.ChargeStatus == "charged" && job.RefundLedgerID == 0 {
		job.ChargeStatus = "refund_pending"
	}
	_ = s.store.UpdateCreativeJob(durableCtx, *job)
	_ = s.store.AddCreativeJobEvent(durableCtx, store.CreativeJobEvent{JobID: job.ID, EventType: "failed", Message: cause.Error()})
	_ = s.refundJob(durableCtx, job)
	return job, cause
}

func (s *Service) refundJob(ctx context.Context, job *store.CreativeJob) error {
	if job == nil || job.RefundLedgerID != 0 || (job.ChargeStatus != "charged" && job.ChargeStatus != "refund_pending") || s.credit == nil {
		return nil
	}
	res, err := s.credit.Grant(ctx, credit.Request{UserID: job.UserID, Amount: job.ChargeAmount, Source: SourceRefund, SourceRef: job.OrderNo, Scope: SourceRefund, Slot: job.OrderNo, Notes: "AI创作失败退款 " + job.OrderNo})
	if err != nil {
		return err
	}
	job.ChargeStatus = "refunded"
	job.Status = store.CreativeJobRefunded
	if res != nil {
		job.RefundLedgerID = res.LedgerID
	}
	if err := s.store.UpdateCreativeJob(ctx, *job); err != nil {
		return err
	}
	_ = s.store.AddCreativeJobEvent(ctx, store.CreativeJobEvent{JobID: job.ID, EventType: "refunded", Message: fmt.Sprintf("已退款 %.4f", job.ChargeAmount)})
	return nil
}

func durableContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
}

func (s *Service) pollPending(ctx context.Context) {
	s.pollMu.Lock()
	defer s.pollMu.Unlock()
	jobs, err := s.store.ListPendingCreativeVideos(ctx, 20)
	if err != nil {
		return
	}
	for i := range jobs {
		job := jobs[i]
		p, err := s.store.GetCreativeProvider(ctx, job.ProviderID)
		if err != nil {
			continue
		}
		body, status, err := s.providerJSON(ctx, *p, http.MethodGet, "/v1/videos/"+url.PathEscape(job.UpstreamRequestID), nil)
		if err != nil || status >= 300 {
			continue
		}
		var v struct {
			Status   string         `json:"status"`
			Progress int            `json:"progress"`
			Video    map[string]any `json:"video"`
			Error    struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(body, &v) != nil {
			continue
		}
		job.UpstreamStatus = v.Status
		job.Progress = v.Progress
		job.ResultJSON = string(body)
		switch v.Status {
		case "done", "completed":
			now := time.Now().UTC()
			job.Status = store.CreativeJobCompleted
			job.Progress = 100
			job.CompletedAt = &now
			_ = s.store.UpdateCreativeJob(ctx, job)
			_ = s.store.AddCreativeJobEvent(ctx, store.CreativeJobEvent{JobID: job.ID, EventType: "completed", Message: "视频生成完成"})
		case "failed":
			_, _ = s.failAndRefund(ctx, &job, defaultString(v.Error.Code, "upstream_failed"), errors.New(defaultString(v.Error.Message, "视频生成失败")))
		default:
			_ = s.store.UpdateCreativeJob(ctx, job)
		}
	}
	s.retryRefunds(ctx)
}

func (s *Service) retryRefunds(ctx context.Context) {
	jobs, err := s.store.ListPendingCreativeRefunds(ctx, 20)
	if err != nil {
		return
	}
	for i := range jobs {
		durableCtx, cancel := durableContext(ctx)
		_ = s.refundJob(durableCtx, &jobs[i])
		cancel()
	}
}

func (s *Service) OpenJobContent(ctx context.Context, job *store.CreativeJob, index int) (io.ReadCloser, string, int64, error) {
	if job.Status != store.CreativeJobCompleted {
		return nil, "", 0, fmt.Errorf("作品尚未完成")
	}
	p, err := s.store.GetCreativeProvider(ctx, job.ProviderID)
	if err != nil {
		return nil, "", 0, err
	}
	rawURL := ""
	if job.MediaType == "image" {
		var v struct {
			Data []struct {
				URL string `json:"url"`
			} `json:"data"`
		}
		if json.Unmarshal([]byte(job.ResultJSON), &v) != nil || index < 0 || index >= len(v.Data) {
			return nil, "", 0, fmt.Errorf("图片结果不存在")
		}
		rawURL = v.Data[index].URL
	} else {
		var v struct {
			Video struct {
				URL string `json:"url"`
			} `json:"video"`
		}
		if json.Unmarshal([]byte(job.ResultJSON), &v) != nil {
			return nil, "", 0, fmt.Errorf("视频结果不存在")
		}
		rawURL = v.Video.URL
	}
	if strings.HasPrefix(rawURL, "/") {
		rawURL = s.providerEndpoint(*p, rawURL)
	}
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, "", 0, fmt.Errorf("媒体地址无效")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", 0, err
	}
	providerURL, _ := url.Parse(s.providerBaseURL(*p))
	if sameURLOrigin(u, providerURL) {
		applyProviderAuth(req, *p)
		s.prepareProviderRequest(req, *p)
	}
	mediaClient := *s.client
	mediaClient.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if !sameURLOrigin(next.URL, providerURL) {
			next.Header.Del("Authorization")
			next.Host = ""
		}
		return nil
	}
	resp, err := mediaClient.Do(req)
	if err != nil {
		return nil, "", 0, err
	}
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		return nil, "", 0, fmt.Errorf("媒体读取 HTTP %d: %s", resp.StatusCode, truncate(b, 300))
	}
	size := resp.ContentLength
	return resp.Body, defaultString(resp.Header.Get("Content-Type"), "application/octet-stream"), size, nil
}

func (s *Service) providerJSON(ctx context.Context, p store.CreativeProvider, method, path string, payload any) ([]byte, int, error) {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return nil, 0, err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.providerEndpoint(p, path), body)
	if err != nil {
		return nil, 0, err
	}
	applyProviderAuth(req, p)
	s.prepareProviderRequest(req, p)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	return raw, resp.StatusCode, readErr
}

func (s *Service) providerBaseURL(p store.CreativeProvider) string {
	if p.Kind == ProviderPool && s.accounts != nil {
		return s.accounts.BaseURL()
	}
	return p.BaseURL
}

func (s *Service) providerEndpoint(p store.CreativeProvider, path string) string {
	return providerEndpoint(s.providerBaseURL(p), path)
}

func (s *Service) prepareProviderRequest(req *http.Request, p store.CreativeProvider) {
	if p.Kind == ProviderPool && s.accounts != nil {
		s.accounts.PrepareGatewayRequest(req)
	}
}
func applyProviderAuth(req *http.Request, p store.CreativeProvider) {
	if strings.TrimSpace(p.APIKey) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(p.APIKey))
	}
}
func sameURLOrigin(a, b *url.URL) bool {
	return a != nil && b != nil && strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host)
}
func providerEndpoint(baseURL, path string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if strings.HasSuffix(strings.ToLower(baseURL), "/v1") && strings.HasPrefix(path, "/v1/") {
		return baseURL + strings.TrimPrefix(path, "/v1")
	}
	return baseURL + path
}
func decodePricing(raw string) (Pricing, error) {
	var p Pricing
	if err := json.Unmarshal([]byte(defaultString(strings.TrimSpace(raw), "{}")), &p); err != nil {
		return p, fmt.Errorf("invalid price_json: %w", err)
	}
	return p, nil
}
func decodeConstraints(raw string) (Constraints, error) {
	var c Constraints
	if err := json.Unmarshal([]byte(defaultString(strings.TrimSpace(raw), "{}")), &c); err != nil {
		return c, fmt.Errorf("invalid constraints_json: %w", err)
	}
	return c, nil
}
func validateModelConstraints(m store.CreativeModel, ratio, resolution string, quantity int) error {
	c, err := decodeConstraints(m.ConstraintsJSON)
	if err != nil {
		return err
	}
	if len(c.AspectRatios) > 0 && !containsFold(c.AspectRatios, ratio) {
		return fmt.Errorf("模型不支持比例 %s", ratio)
	}
	if len(c.Resolutions) > 0 && !containsFold(c.Resolutions, resolution) {
		return fmt.Errorf("模型不支持分辨率 %s", resolution)
	}
	if m.Capability == CapabilityImage && c.MaxImages > 0 && quantity > c.MaxImages {
		return fmt.Errorf("该模型最多生成 %d 张图片", c.MaxImages)
	}
	if m.Capability == CapabilityVideo {
		if c.DurationMin > 0 && quantity < c.DurationMin {
			return fmt.Errorf("该模型最短生成 %d 秒视频", c.DurationMin)
		}
		if c.DurationMax > 0 && quantity > c.DurationMax {
			return fmt.Errorf("该模型最长生成 %d 秒视频", c.DurationMax)
		}
	}
	return nil
}
func containsFold(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(target)) {
			return true
		}
	}
	return false
}
func roundMoney(v float64) float64 { return float64(int64(v*10000+.5)) / 10000 }
func defaultString(v, d string) string {
	if strings.TrimSpace(v) == "" {
		return d
	}
	return v
}
func newToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b)
}
func providerError(status int, body []byte, err error) error {
	if err != nil {
		return err
	}
	return fmt.Errorf("上游 HTTP %d: %s", status, truncate(body, 500))
}
func truncate(v []byte, n int) string {
	s := strings.TrimSpace(string(v))
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}
func ValidateImageDataURL(v string) error {
	if v == "" {
		return nil
	}
	parts := strings.SplitN(v, ",", 2)
	if len(parts) != 2 || !strings.HasPrefix(strings.ToLower(parts[0]), "data:image/") || !strings.Contains(strings.ToLower(parts[0]), ";base64") {
		return fmt.Errorf("参考图片必须是 Base64 图片")
	}
	raw, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		return fmt.Errorf("参考图片 Base64 无效")
	}
	if len(raw) > 5<<20 {
		return fmt.Errorf("参考图片不能超过 5MB")
	}
	return nil
}
