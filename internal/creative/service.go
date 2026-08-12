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
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

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
	Resolutions   []string `json:"resolutions,omitempty"`
	AspectRatios  []string `json:"aspect_ratios,omitempty"`
	MaxImages     int      `json:"max_images,omitempty"`
	DurationMin   int      `json:"duration_min,omitempty"`
	DurationMax   int      `json:"duration_max,omitempty"`
	MaxUploadMB   int      `json:"max_upload_mb,omitempty"`
	SupportsEdit  bool     `json:"supports_edit,omitempty"`
	RequiresImage bool     `json:"requires_image,omitempty"`
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
	ProviderKind string      `json:"provider_kind"`
	BillingMode  string      `json:"billing_mode"`
}
type ModelSyncResult struct {
	Synced  int `json:"synced"`
	Added   int `json:"added"`
	Kept    int `json:"kept"`
	Removed int `json:"removed"`
}

type MediaContent struct {
	Body          io.ReadCloser
	ReadSeeker    io.ReadSeeker
	Name          string
	ModTime       time.Time
	ContentType   string
	ContentLength int64
	ContentRange  string
	AcceptRanges  string
	StatusCode    int
}

type Service struct {
	store         *store.Store
	accounts      *sub2api.Client
	credit        creditService
	client        *http.Client
	credentialKey []byte
	mediaRoot     string
	stop          chan struct{}
	wg            sync.WaitGroup
	pollMu        sync.Mutex
	routeMu       sync.Mutex
	mediaMu       sync.Mutex
}

type creditService interface {
	Grant(context.Context, credit.Request) (*credit.Result, error)
	Reclaim(context.Context, credit.Request) (*credit.Result, error)
}

func New(st *store.Store, accounts *sub2api.Client, creditSvc *credit.Service, options ...string) *Service {
	secret := ""
	mediaRoot := ""
	if len(options) > 0 {
		secret = options[0]
	}
	if len(options) > 1 {
		mediaRoot = strings.TrimSpace(options[1])
	}
	return &Service{store: st, accounts: accounts, credit: creditSvc, client: &http.Client{Timeout: 5 * time.Minute}, credentialKey: deriveCredentialKey(secret), mediaRoot: mediaRoot, stop: make(chan struct{})}
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
	s.routeMu.Lock()
	defer s.routeMu.Unlock()

	p.Kind = defaultString(strings.TrimSpace(p.Kind), ProviderOpenAI)
	p.APIKey = strings.TrimSpace(p.APIKey)
	if p.Kind != ProviderOpenAI && p.Kind != ProviderPool {
		return nil, fmt.Errorf("暂不支持 Provider 类型 %s", p.Kind)
	}
	var current *store.CreativeProvider
	if p.ID > 0 {
		var err error
		current, err = s.store.GetCreativeProvider(ctx, p.ID)
		if err != nil {
			return nil, err
		}
		if current.Kind != p.Kind {
			return nil, fmt.Errorf("渠道类型创建后不能修改")
		}
	}
	if p.Kind == ProviderPool {
		if s.accounts == nil {
			return nil, fmt.Errorf("Sub2API 客户端未配置")
		}
		providers, err := s.store.ListCreativeProviders(ctx)
		if err != nil {
			return nil, err
		}
		for _, current := range providers {
			if current.Kind == ProviderPool && current.ID != p.ID {
				return nil, fmt.Errorf("Sub2API 账号池渠道已存在")
			}
		}
		p.Name = defaultString(strings.TrimSpace(p.Name), "Sub2API 账号池")
		p.BaseURL = s.accounts.BaseURL()
		p.APIKey = ""
	} else {
		if err := validateProviderBaseURL(p.BaseURL); err != nil {
			return nil, err
		}
		providedKey := p.APIKey
		if current != nil && providedKey == "" {
			p.APIKey = current.APIKey
		}
		if current != nil {
			baseURL := strings.TrimRight(strings.TrimSpace(p.BaseURL), "/")
			routeChanged := baseURL != current.BaseURL || (providedKey != "" && providedKey != current.APIKey)
			if routeChanged {
				active, err := s.store.HasActiveCreativeVideo(ctx, 0, p.ID)
				if err != nil {
					return nil, err
				}
				if active {
					return nil, fmt.Errorf("该渠道有视频任务生成中，完成后才能更换 Base URL 或全局 Key")
				}
			}
		}
	}
	if p.Kind == ProviderOpenAI && p.Enabled && strings.TrimSpace(p.APIKey) == "" {
		return nil, fmt.Errorf("启用渠道前必须配置媒体调用 API Key")
	}
	saved, err := s.store.SaveCreativeProvider(ctx, p)
	if err != nil {
		return nil, err
	}
	if p.Kind == ProviderPool && saved.APIKey != "" {
		if err := s.store.ClearCreativeProviderAPIKey(ctx, saved.ID); err != nil {
			return nil, err
		}
		return s.store.GetCreativeProvider(ctx, saved.ID)
	}
	return saved, nil
}

func validateProviderBaseURL(value string) error {
	u, err := url.Parse(strings.TrimSpace(value))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return fmt.Errorf("外部渠道 Base URL 必须是有效的 HTTP(S) 地址")
	}
	return nil
}

func (s *Service) EnsureAccountPoolProvider(ctx context.Context) (*store.CreativeProvider, error) {
	providers, err := s.store.ListCreativeProviders(ctx)
	if err != nil {
		return nil, err
	}
	for i := range providers {
		if providers[i].Kind == ProviderPool {
			return s.SaveProvider(ctx, providers[i])
		}
	}
	provider, err := s.SaveProvider(ctx, store.CreativeProvider{Name: "Sub2API 账号池", Kind: ProviderPool, Enabled: true})
	if err != nil {
		return nil, err
	}
	if _, err := s.SyncProviderModels(ctx, provider.ID); err != nil {
		return provider, err
	}
	return provider, nil
}
func (s *Service) DeleteProvider(ctx context.Context, id int64) error {
	s.routeMu.Lock()
	defer s.routeMu.Unlock()

	provider, err := s.store.GetCreativeProvider(ctx, id)
	if err != nil {
		return err
	}
	if provider.Kind == ProviderPool {
		return fmt.Errorf("内置 Sub2API 账号池不能删除，可将其停用")
	}
	hasJobs, err := s.store.HasCreativeJobsForProvider(ctx, id)
	if err != nil {
		return err
	}
	if hasJobs {
		return fmt.Errorf("该渠道已有创作订单，不能删除，可将其停用")
	}
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
	pricing, err := decodePricing(m.PriceJSON)
	if err != nil {
		return nil, err
	}
	constraints, err := decodeConstraints(m.ConstraintsJSON)
	if err != nil {
		return nil, err
	}
	if m.Capability == CapabilityVideo && pricing.Resolutions != nil {
		constraints.Resolutions = pricedResolutions(pricing.Resolutions)
		raw, marshalErr := json.Marshal(constraints)
		if marshalErr != nil {
			return nil, marshalErr
		}
		m.ConstraintsJSON = string(raw)
	}
	return s.store.UpsertCreativeModel(ctx, m)
}
func (s *Service) DeleteModel(ctx context.Context, id int64) error {
	if id <= 0 {
		return fmt.Errorf("invalid model id")
	}
	return s.store.DeleteCreativeModel(ctx, id)
}
func (s *Service) ListJobs(ctx context.Context, f store.CreativeJobFilter) ([]store.CreativeJob, int, error) {
	return s.store.ListCreativeJobs(ctx, f)
}
func (s *Service) GetJob(ctx context.Context, id, userID int64) (*store.CreativeJob, error) {
	return s.store.GetCreativeJob(ctx, id, userID)
}

func (s *Service) DeleteJob(ctx context.Context, id, userID int64) error {
	job, err := s.store.GetCreativeJob(ctx, id, userID)
	if err != nil {
		return err
	}
	if job.Status == store.CreativeJobCreated || job.Status == store.CreativeJobProcessing {
		return fmt.Errorf("生成中的作品不能删除")
	}
	if job.ChargeStatus == "refund_pending" {
		return fmt.Errorf("作品正在退款，完成后才能删除")
	}
	if err := s.store.HideCreativeJob(ctx, id, userID); err != nil {
		return err
	}
	s.removeLocalMedia(ctx, job)
	_ = s.store.AddCreativeJobEvent(ctx, store.CreativeJobEvent{JobID: id, EventType: "hidden_by_user", Message: "用户从作品列表删除"})
	return nil
}

func (s *Service) DeleteJobAsAdmin(ctx context.Context, id int64) error {
	job, err := s.store.GetCreativeJob(ctx, id, 0)
	if err != nil {
		return err
	}
	if job.DeletedAt != nil {
		return sql.ErrNoRows
	}
	if job.Status == store.CreativeJobCreated || job.Status == store.CreativeJobProcessing {
		return fmt.Errorf("生成中的作品不能删除")
	}
	if job.ChargeStatus == "refund_pending" {
		return fmt.Errorf("作品正在退款，完成后才能删除")
	}
	if err := s.store.HideCreativeJob(ctx, id, job.UserID); err != nil {
		return err
	}
	s.removeLocalMedia(ctx, job)
	_ = s.store.AddCreativeJobEvent(ctx, store.CreativeJobEvent{JobID: id, EventType: "deleted_by_admin", Message: "管理员从作品库删除"})
	return nil
}

func (s *Service) ModelOptions(ctx context.Context, userID int64) ([]ModelOption, error) {
	models, err := s.store.ListCreativeModels(ctx, 0, true)
	if err != nil {
		return nil, err
	}
	providers, err := s.store.ListCreativeProviders(ctx)
	if err != nil {
		return nil, err
	}
	pm := map[int64]store.CreativeProvider{}
	availability := map[int64]map[string]int{}
	for _, p := range providers {
		if p.Enabled {
			pm[p.ID] = p
			if p.Kind == ProviderPool {
				counts, accountErr := s.discoverAccountModels(ctx, p)
				callProvider, credentialErr := s.providerForUser(ctx, userID, p)
				allowed := map[string]int{}
				if accountErr == nil && credentialErr == nil {
					if remote, remoteErr := s.listRemoteModels(ctx, callProvider); remoteErr == nil {
						for _, modelID := range remote {
							if counts[modelID] > 0 {
								allowed[modelID] = counts[modelID]
							}
						}
					}
				}
				availability[p.ID] = allowed
			}
		}
	}
	out := []ModelOption{}
	for _, m := range models {
		provider, ok := pm[m.ProviderID]
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
		if m.Capability == CapabilityVideo && p.Resolutions != nil {
			c.Resolutions = pricedResolutions(p.Resolutions)
			if len(c.Resolutions) == 0 {
				continue
			}
		}
		billingMode := "platform"
		if provider.Kind == ProviderPool {
			billingMode = "upstream"
		}
		out = append(out, ModelOption{CreativeModel: m, Pricing: p, Constraints: c, ProviderName: provider.Name, ProviderKind: provider.Kind, BillingMode: billingMode})
	}
	return out, nil
}

func pricedResolutions(prices map[string]float64) []string {
	seen := make(map[string]bool, len(prices))
	out := make([]string, 0, len(prices))
	for _, resolution := range []string{"480p", "720p", "1080p"} {
		if prices[resolution] > 0 {
			out = append(out, resolution)
			seen[resolution] = true
		}
	}
	extra := make([]string, 0, len(prices))
	for resolution, price := range prices {
		resolution = strings.ToLower(strings.TrimSpace(resolution))
		if price > 0 && resolution != "" && !seen[resolution] {
			extra = append(extra, resolution)
			seen[resolution] = true
		}
	}
	sort.Strings(extra)
	return append(out, extra...)
}

func (s *Service) SyncProviderModels(ctx context.Context, providerID int64) (ModelSyncResult, error) {
	p, err := s.store.GetCreativeProvider(ctx, providerID)
	if err != nil {
		return ModelSyncResult{}, err
	}
	set := map[string]bool{}
	if p.Kind == ProviderPool {
		counts, discoverErr := s.discoverAccountModels(ctx, *p)
		if discoverErr != nil {
			return ModelSyncResult{}, fmt.Errorf("account models: %w", discoverErr)
		}
		for modelID := range counts {
			set[modelID] = true
		}
	} else {
		remote, remoteErr := s.listRemoteModels(ctx, *p)
		if remoteErr != nil {
			return ModelSyncResult{}, fmt.Errorf("provider models: %w", remoteErr)
		}
		for _, modelID := range remote {
			set[modelID] = true
		}
	}
	existing, err := s.store.ListCreativeModels(ctx, p.ID, false)
	if err != nil {
		return ModelSyncResult{}, err
	}
	existingByModel := make(map[string]struct{}, len(existing))
	for _, model := range existing {
		existingByModel[model.ModelID] = struct{}{}
	}
	result := ModelSyncResult{Synced: len(set)}
	for modelID := range set {
		capability, protocol, pricing, constraints, known := inferModel(modelID)
		rawPrice, _ := json.Marshal(pricing)
		rawConstraints, _ := json.Marshal(constraints)
		_, err := s.store.UpsertCreativeModel(ctx, store.CreativeModel{ProviderID: p.ID, ModelID: modelID, DisplayName: modelID, Capability: capability, Protocol: protocol, PriceJSON: string(rawPrice), ConstraintsJSON: string(rawConstraints), SourceGroup: p.SourceGroup, Enabled: known})
		if err != nil {
			return result, err
		}
		if _, ok := existingByModel[modelID]; ok {
			result.Kept++
		} else {
			result.Added++
		}
	}
	removed, err := s.store.DeleteCreativeModelsNotIn(ctx, p.ID, set)
	if err != nil {
		return result, err
	}
	result.Removed = removed
	return result, nil
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
	case "grok-imagine-image-edit":
		imageC.SupportsEdit = true
		imageC.RequiresImage = true
		return CapabilityImage, ProtocolImages, Pricing{Currency: "USD", Unit: "image", Resolutions: map[string]float64{"1k": .05, "2k": .07}, InputImage: .01, Source: "xAI official", AsOf: "2026-07-14"}, imageC, true
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
		return nil
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
	if utf8.RuneCountInString(in.Prompt) > 4000 {
		return nil, fmt.Errorf("提示词不能超过 4000 个字符")
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
	} else if constraints, _ := decodeConstraints(m.ConstraintsJSON); constraints.RequiresImage {
		return nil, fmt.Errorf("该模型仅支持图片编辑，请先上传参考图")
	}
	callProvider, err := s.providerForUser(ctx, userID, p)
	if err != nil {
		return nil, err
	}
	params, _ := json.Marshal(map[string]any{
		"model_id": in.ModelDBID, "prompt": in.Prompt, "n": in.Count,
		"aspect_ratio": in.AspectRatio, "resolution": in.Resolution,
		"has_reference_image": in.ImageDataURL != "",
	})
	job, existing, err := s.prepareJob(ctx, userID, p.ID, m.ModelID, "image", in.Prompt, string(params), in.RequestKey, price, p.Kind != ProviderPool)
	if err != nil || existing {
		return job, err
	}
	payload := map[string]any{"model": m.ModelID, "prompt": in.Prompt, "n": in.Count, "aspect_ratio": in.AspectRatio, "resolution": in.Resolution, "response_format": "url", "stream": false}
	path := "/v1/images/generations"
	if in.ImageDataURL != "" {
		path = "/v1/images/edits"
		payload["image"] = map[string]any{"url": in.ImageDataURL}
	}
	body, status, err := s.providerJSON(ctx, callProvider, http.MethodPost, path, payload)
	if err != nil || status >= 300 {
		return s.failAndRefund(ctx, job, "upstream_error", providerError(status, body, err))
	}
	var parsed struct {
		Data []map[string]any `json:"data"`
	}
	if json.Unmarshal(body, &parsed) != nil || len(parsed.Data) == 0 {
		return s.failAndRefund(ctx, job, "invalid_response", fmt.Errorf("图片接口未返回图片"))
	}
	archiveCtx, archiveCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
	images, sanitizedResult, archiveErr := s.archiveImages(archiveCtx, job, body)
	archiveCancel()
	if archiveErr != nil {
		return s.failAndRefund(ctx, job, "image_archive_failed", fmt.Errorf("保存图片到本地失败: %w", archiveErr))
	}
	now := time.Now().UTC()
	job.ResultJSON = sanitizedResult
	job.UpstreamStatus = "done"
	job.Status = store.CreativeJobCompleted
	job.Progress = 100
	job.CompletedAt = &now
	durableCtx, cancel := durableContext(ctx)
	defer cancel()
	if err := s.store.CompleteCreativeImageJob(durableCtx, *job, images); err != nil {
		s.removeArchivedImages(images)
		return s.failAndRefund(durableCtx, job, "result_state_failed", fmt.Errorf("保存图片结果失败: %w", err))
	}
	_ = s.store.AddCreativeJobEvent(durableCtx, store.CreativeJobEvent{JobID: job.ID, EventType: "completed", Message: "图片生成完成"})
	return job, nil
}

func (s *Service) CreateVideo(ctx context.Context, userID int64, in VideoInput) (*store.CreativeJob, error) {
	in.Prompt = strings.TrimSpace(in.Prompt)
	if in.Prompt == "" && in.ImageDataURL == "" {
		return nil, fmt.Errorf("prompt or image is required")
	}
	if utf8.RuneCountInString(in.Prompt) > 4000 {
		return nil, fmt.Errorf("提示词不能超过 4000 个字符")
	}
	if in.Duration <= 0 {
		in.Duration = 8
	}
	if in.Duration > 60 {
		return nil, fmt.Errorf("duration must be between 1 and 60")
	}
	in.AspectRatio = defaultString(strings.TrimSpace(in.AspectRatio), "16:9")
	in.Resolution = strings.ToLower(defaultString(strings.TrimSpace(in.Resolution), "720p"))
	s.routeMu.Lock()
	m, p, price, err := s.resolveModel(ctx, in.ModelDBID, CapabilityVideo, in.Resolution, in.Duration)
	if err != nil {
		s.routeMu.Unlock()
		return nil, err
	}
	if err := validateModelConstraints(m, in.AspectRatio, in.Resolution, in.Duration); err != nil {
		s.routeMu.Unlock()
		return nil, err
	}
	callProvider, err := s.providerForUser(ctx, userID, p)
	if err != nil {
		s.routeMu.Unlock()
		return nil, err
	}
	params, _ := json.Marshal(map[string]any{
		"model_id": in.ModelDBID, "prompt": in.Prompt, "duration": in.Duration,
		"aspect_ratio": in.AspectRatio, "resolution": in.Resolution,
		"has_reference_image": in.ImageDataURL != "",
	})
	job, existing, err := s.prepareJob(ctx, userID, p.ID, m.ModelID, "video", in.Prompt, string(params), in.RequestKey, price, p.Kind != ProviderPool)
	s.routeMu.Unlock()
	if err != nil || existing {
		return job, err
	}
	payload := map[string]any{"model": m.ModelID, "prompt": in.Prompt, "duration": in.Duration, "aspect_ratio": in.AspectRatio, "resolution": in.Resolution}
	if in.ImageDataURL != "" {
		payload["image"] = map[string]any{"url": in.ImageDataURL}
	}
	body, status, err := s.providerJSON(ctx, callProvider, http.MethodPost, "/v1/videos/generations", payload)
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
	durableCtx, cancel := durableContext(ctx)
	defer cancel()
	if err := s.store.UpdateCreativeJob(durableCtx, *job); err != nil {
		return s.failAndRefund(durableCtx, job, "result_state_failed", fmt.Errorf("保存视频任务失败: %w", err))
	}
	_ = s.store.AddCreativeJobEvent(durableCtx, store.CreativeJobEvent{JobID: job.ID, EventType: "submitted", Message: "视频任务已提交"})
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
func (s *Service) prepareJob(ctx context.Context, userID, providerID int64, modelID, mediaType, prompt, params, requestKey string, amount float64, billByExtension bool) (*store.CreativeJob, bool, error) {
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
	chargeStatus := "pending"
	status := store.CreativeJobCreated
	if !billByExtension {
		chargeStatus = "upstream"
		status = store.CreativeJobProcessing
	}
	job, err := s.store.CreateCreativeJob(ctx, store.CreativeJob{OrderNo: order, RequestKey: requestKey, UserID: userID, ProviderID: providerID, ModelID: modelID, MediaType: mediaType, Prompt: prompt, ParamsJSON: params, ChargeAmount: amount, ChargeStatus: chargeStatus, Status: status})
	if err != nil {
		return nil, false, err
	}
	_ = s.store.AddCreativeJobEvent(ctx, store.CreativeJobEvent{JobID: job.ID, EventType: "created", Message: "订单已创建"})
	if !billByExtension {
		_ = s.store.AddCreativeJobEvent(ctx, store.CreativeJobEvent{JobID: job.ID, EventType: "upstream_billing", Message: "费用由用户 Sub2API API Key 结算"})
		return job, false, nil
	}
	if s.credit == nil {
		return s.failNoRefund(ctx, job, "billing_unavailable", fmt.Errorf("余额服务不可用"))
	}
	res, err := s.credit.Reclaim(ctx, credit.Request{UserID: userID, Amount: amount, Source: SourceCharge, SourceRef: order, Scope: SourceCharge, Slot: order, Notes: fmt.Sprintf("AI创作扣费 %s -%.4f", order, amount)})
	if err != nil {
		if res != nil {
			job.ChargeStatus = "refund_pending"
			job.ChargeLedgerID = res.LedgerID
			failed, cause := s.failAndRefund(ctx, job, "charge_state_failed", err)
			return failed, false, cause
		}
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
		failed, cause := s.failAndRefund(durableCtx, job, "charge_state_failed", err)
		return failed, false, cause
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
		callProvider, err := s.providerForUser(ctx, job.UserID, *p)
		if err != nil {
			continue
		}
		body, status, err := s.providerJSON(ctx, callProvider, http.MethodGet, "/v1/videos/"+url.PathEscape(job.UpstreamRequestID), nil)
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
			if err := s.archiveVideo(ctx, &job); err != nil {
				continue
			}
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

func (s *Service) OpenJobContent(ctx context.Context, job *store.CreativeJob, index int, rangeHeader string) (*MediaContent, error) {
	if job.Status != store.CreativeJobCompleted {
		return nil, fmt.Errorf("作品尚未完成")
	}
	if job.MediaType == "image" {
		if content, err := s.openLocalImage(ctx, job, index); content != nil || err != nil {
			return content, err
		}
		if err := s.archiveExistingImages(ctx, job); err == nil {
			if content, openErr := s.openLocalImage(ctx, job, index); content != nil || openErr != nil {
				return content, openErr
			}
		}
	}
	if content, err := s.openLocalMedia(job); content != nil || err != nil {
		return content, err
	}
	return s.openRemoteJobContent(ctx, job, index, rangeHeader)
}

func (s *Service) openRemoteJobContent(ctx context.Context, job *store.CreativeJob, index int, rangeHeader string) (*MediaContent, error) {
	p, err := s.store.GetCreativeProvider(ctx, job.ProviderID)
	if err != nil {
		return nil, err
	}
	rawURL := ""
	if job.MediaType == "image" {
		var v struct {
			Data []struct {
				URL string `json:"url"`
			} `json:"data"`
		}
		if json.Unmarshal([]byte(job.ResultJSON), &v) != nil || index < 0 || index >= len(v.Data) {
			return nil, fmt.Errorf("图片结果不存在")
		}
		rawURL = v.Data[index].URL
	} else {
		var v struct {
			Video struct {
				URL string `json:"url"`
			} `json:"video"`
		}
		if json.Unmarshal([]byte(job.ResultJSON), &v) != nil {
			return nil, fmt.Errorf("视频结果不存在")
		}
		rawURL = v.Video.URL
	}
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil, fmt.Errorf("媒体地址无效")
	}
	if strings.HasPrefix(rawURL, "/") {
		rawURL = s.providerEndpoint(*p, rawURL)
	} else if parsed, parseErr := url.Parse(rawURL); parseErr == nil && !parsed.IsAbs() && parsed.Host == "" {
		rawURL = s.providerEndpoint(*p, "/"+strings.TrimLeft(rawURL, "./"))
	}
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("媒体地址无效")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	if rangeHeader = strings.TrimSpace(rangeHeader); rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}
	providerURL, _ := url.Parse(s.providerBaseURL(*p))
	if sameURLOrigin(u, providerURL) {
		callProvider, credentialErr := s.providerForUser(ctx, job.UserID, *p)
		if credentialErr != nil {
			return nil, credentialErr
		}
		applyProviderAuth(req, callProvider)
		s.prepareProviderRequest(req, callProvider)
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
		return nil, err
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		return nil, fmt.Errorf("媒体读取 HTTP %d: %s", resp.StatusCode, truncate(b, 300))
	}
	return &MediaContent{
		Body:          resp.Body,
		ContentType:   defaultString(resp.Header.Get("Content-Type"), "application/octet-stream"),
		ContentLength: resp.ContentLength,
		ContentRange:  resp.Header.Get("Content-Range"),
		AcceptRanges:  resp.Header.Get("Accept-Ranges"),
		StatusCode:    resp.StatusCode,
	}, nil
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
	if strings.Contains(strings.ToLower(string(body)), "generated video rejected by content moderation") {
		return fmt.Errorf("视频未通过内容安全审核，请调整提示词或参考图后重试")
	}
	if status == http.StatusForbidden {
		var payload struct {
			Error struct {
				Message string `json:"message"`
				Type    string `json:"type"`
			} `json:"error"`
		}
		if json.Unmarshal(body, &payload) == nil &&
			strings.EqualFold(strings.TrimSpace(payload.Error.Type), "permission_error") &&
			strings.Contains(strings.ToLower(payload.Error.Message), "image generation is not enabled for this group") {
			return fmt.Errorf("当前 API Key 所属分组未开启图片生成功能，请联系管理员在 Sub2API 分组设置中开启")
		}
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
	if len(parts) != 2 {
		return fmt.Errorf("参考图片必须是 Base64 图片")
	}
	metadata := strings.Split(strings.ToLower(parts[0]), ";")
	if len(metadata) < 2 || (metadata[0] != "data:image/png" && metadata[0] != "data:image/jpeg" && metadata[0] != "data:image/webp") {
		return fmt.Errorf("参考图片格式必须是 PNG、JPEG 或 WebP")
	}
	hasBase64 := false
	for _, value := range metadata[1:] {
		if strings.TrimSpace(value) == "base64" {
			hasBase64 = true
			break
		}
	}
	if !hasBase64 {
		return fmt.Errorf("参考图片必须是 Base64 图片")
	}
	raw, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		return fmt.Errorf("参考图片 Base64 无效")
	}
	if len(raw) > 5<<20 {
		return fmt.Errorf("参考图片不能超过 5MB")
	}
	validContent := false
	switch metadata[0] {
	case "data:image/png":
		validContent = len(raw) >= 8 && bytes.Equal(raw[:8], []byte{'\x89', 'P', 'N', 'G', '\r', '\n', '\x1a', '\n'})
	case "data:image/jpeg":
		validContent = len(raw) >= 3 && raw[0] == 0xff && raw[1] == 0xd8 && raw[2] == 0xff
	case "data:image/webp":
		validContent = len(raw) >= 12 && string(raw[:4]) == "RIFF" && string(raw[8:12]) == "WEBP"
	}
	if !validContent {
		return fmt.Errorf("参考图片内容与格式不匹配")
	}
	return nil
}
