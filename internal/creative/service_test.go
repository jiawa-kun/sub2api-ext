package creative

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"sub2api-ext/internal/credit"
	"sub2api-ext/internal/store"
	"sub2api-ext/internal/sub2api"
)

const testPNGBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="

func testPNGBytes() []byte {
	raw, _ := base64.StdEncoding.DecodeString(testPNGBase64)
	return raw
}

func TestInferGrokMediaPricing(t *testing.T) {
	tests := []struct {
		model      string
		capability string
		unit       string
		fixed      float64
		resolution string
		price      float64
	}{
		{"grok-imagine-image", CapabilityImage, "image", 0.02, "", 0},
		{"grok-imagine-image-quality", CapabilityImage, "image", 0, "2k", 0.07},
		{"grok-imagine-video-1.5", CapabilityVideo, "second", 0, "720p", 0.14},
	}
	for _, tt := range tests {
		capability, _, pricing, _, known := inferModel(tt.model)
		if !known || capability != tt.capability || pricing.Unit != tt.unit || pricing.Fixed != tt.fixed {
			t.Fatalf("inferModel(%q) = capability=%q pricing=%+v known=%v", tt.model, capability, pricing, known)
		}
		if tt.resolution != "" && pricing.Resolutions[tt.resolution] != tt.price {
			t.Fatalf("inferModel(%q) %s price=%v", tt.model, tt.resolution, pricing.Resolutions[tt.resolution])
		}
	}
	_, _, pricing, _, known := inferModel("future-image-model")
	if known || pricing.Fixed != 0 {
		t.Fatalf("unknown models must be discovered without a default price: %+v known=%v", pricing, known)
	}
	capability, protocol, pricing, constraints, known := inferModel("grok-imagine-image-edit")
	if !known || capability != CapabilityImage || protocol != ProtocolImages || !constraints.SupportsEdit || !constraints.RequiresImage || pricing.InputImage != 0.01 {
		t.Fatalf("image edit model inference=%q %q %+v %+v known=%v", capability, protocol, pricing, constraints, known)
	}
}

func TestAccountPoolProviderUsesNoGlobalKeyAndIsSingleton(t *testing.T) {
	upstream := httptest.NewServer(http.NotFoundHandler())
	defer upstream.Close()
	st, err := store.Open(filepath.Join(t.TempDir(), "creative.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	legacy, err := st.SaveCreativeProvider(context.Background(), store.CreativeProvider{
		Name: "legacy pool", Kind: ProviderPool, BaseURL: "https://old.example.com", APIKey: "legacy-global-key", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	svc := New(st, sub2api.New(upstream.URL, "admin-key", time.Second), nil)
	normalized, err := svc.EnsureAccountPoolProvider(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if normalized.ID != legacy.ID || normalized.APIKey != "" || normalized.BaseURL != upstream.URL || !normalized.Enabled {
		t.Fatalf("normalized provider=%+v", normalized)
	}
	if _, err := svc.SaveProvider(context.Background(), store.CreativeProvider{Name: "duplicate", Kind: ProviderPool, Enabled: true}); err == nil || !strings.Contains(err.Error(), "已存在") {
		t.Fatalf("duplicate pool provider error=%v", err)
	}
	if _, err := svc.SaveProvider(context.Background(), store.CreativeProvider{ID: legacy.ID, Name: "converted", Kind: ProviderOpenAI, BaseURL: upstream.URL, APIKey: "global-key", Enabled: true}); err == nil || !strings.Contains(err.Error(), "不能修改") {
		t.Fatalf("pool kind mutation error=%v", err)
	}
	if err := svc.DeleteProvider(context.Background(), legacy.ID); err == nil || !strings.Contains(err.Error(), "不能删除") {
		t.Fatalf("delete pool provider error=%v", err)
	}
}

func TestExternalProviderProtectsActiveAndHistoricalJobs(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "creative.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc := New(st, nil, nil)
	p, err := svc.SaveProvider(context.Background(), store.CreativeProvider{
		Name: "external", Kind: ProviderOpenAI, BaseURL: "https://provider.example.com", APIKey: "global-key", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	job, err := st.CreateCreativeJob(context.Background(), store.CreativeJob{
		OrderNo: "active-video", RequestKey: "active-video", UserID: 7, ProviderID: p.ID,
		ModelID: "video", MediaType: "video", Status: store.CreativeJobProcessing,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SaveProvider(context.Background(), store.CreativeProvider{
		ID: p.ID, Name: p.Name, Kind: p.Kind, BaseURL: "https://new-provider.example.com", APIKey: "new-global-key", Enabled: true,
	}); err == nil || !strings.Contains(err.Error(), "视频任务生成中") {
		t.Fatalf("active route mutation error=%v", err)
	}
	job.Status = store.CreativeJobCompleted
	if err := st.UpdateCreativeJob(context.Background(), *job); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SaveProvider(context.Background(), store.CreativeProvider{
		ID: p.ID, Name: p.Name, Kind: p.Kind, BaseURL: "https://new-provider.example.com", APIKey: "new-global-key", Enabled: true,
	}); err != nil {
		t.Fatalf("completed route mutation error=%v", err)
	}
	if err := svc.DeleteProvider(context.Background(), p.ID); err == nil || !strings.Contains(err.Error(), "已有创作订单") {
		t.Fatalf("historical provider delete error=%v", err)
	}
}

func TestResolveModelRejectsMissingPrice(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "creative.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	p, err := st.SaveCreativeProvider(ctx, store.CreativeProvider{Name: "p", BaseURL: "https://example.com", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	m, err := st.UpsertCreativeModel(ctx, store.CreativeModel{
		ProviderID: p.ID, ModelID: "future-image-model", Capability: CapabilityImage,
		Protocol: ProtocolImages, PriceJSON: `{"currency":"USD","unit":"image"}`, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{store: st}
	if _, _, _, err := svc.resolveModel(ctx, m.ID, CapabilityImage, "1k", 1); err == nil || !strings.Contains(err.Error(), "有效价格") {
		t.Fatalf("missing price must reject invocation, got %v", err)
	}
}

func TestResolveModelRejectsWrongProtocol(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "creative.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	p, err := st.SaveCreativeProvider(ctx, store.CreativeProvider{Name: "p", BaseURL: "https://example.com", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	m, err := st.UpsertCreativeModel(ctx, store.CreativeModel{ProviderID: p.ID, ModelID: "image", Capability: CapabilityImage, Protocol: ProtocolVideo, PriceJSON: `{"fixed":0.02}`, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{store: st}
	if _, _, _, err := svc.resolveModel(ctx, m.ID, CapabilityImage, "1k", 1); err == nil || !strings.Contains(err.Error(), "协议") {
		t.Fatalf("wrong protocol must be rejected, got %v", err)
	}
}

func TestFailureStatePersistsAfterRequestCancellation(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "creative.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	p, err := st.SaveCreativeProvider(ctx, store.CreativeProvider{Name: "p", BaseURL: "https://example.com", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	job, err := st.CreateCreativeJob(ctx, store.CreativeJob{OrderNo: "cancelled", RequestKey: "cancelled", UserID: 1, ProviderID: p.ID, ModelID: "image", MediaType: "image"})
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	svc := &Service{store: st}
	_, _, _ = svc.failNoRefund(cancelled, job, "cancelled", context.Canceled)
	saved, err := st.GetCreativeJob(ctx, job.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Status != store.CreativeJobFailed || saved.ErrorCode != "cancelled" {
		t.Fatalf("failure was not persisted after cancellation: %+v", saved)
	}
}

type chargedWithErrorCredit struct {
	refunded bool
}

func (f *chargedWithErrorCredit) Reclaim(context.Context, credit.Request) (*credit.Result, error) {
	return &credit.Result{LedgerID: 41}, errors.New("reclaimed but ledger write failed")
}

func (f *chargedWithErrorCredit) Grant(context.Context, credit.Request) (*credit.Result, error) {
	f.refunded = true
	return &credit.Result{LedgerID: 42}, nil
}

func TestPrepareJobCompensatesWhenChargeSucceededButStateWriteFailed(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "creative.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	p, err := st.SaveCreativeProvider(context.Background(), store.CreativeProvider{Name: "external", BaseURL: "https://example.com", APIKey: "global-key", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	billing := &chargedWithErrorCredit{}
	svc := &Service{store: st, credit: billing}
	job, existing, err := svc.prepareJob(context.Background(), 7, p.ID, "image", "image", "prompt", `{}`, "charge-state-failed", 0.02, true)
	if err == nil || existing {
		t.Fatalf("existing=%v err=%v", existing, err)
	}
	if !billing.refunded || job == nil || job.Status != store.CreativeJobRefunded || job.ChargeStatus != "refunded" || job.ChargeLedgerID != 41 || job.RefundLedgerID != 42 {
		t.Fatalf("billing.refunded=%v job=%+v", billing.refunded, job)
	}
	saved, err := st.GetCreativeJob(context.Background(), job.ID, 7)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Status != store.CreativeJobRefunded || saved.RefundLedgerID != 42 {
		t.Fatalf("saved job=%+v", saved)
	}
}

func TestVideoArchiveFailureRetriesThenFailsForRefund(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "creative.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	p, err := st.SaveCreativeProvider(ctx, store.CreativeProvider{Name: "provider", BaseURL: "https://provider.example.com", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	job, err := st.CreateCreativeJob(ctx, store.CreativeJob{
		OrderNo: "video-archive-fail", RequestKey: "video-archive-fail", UserID: 7, ProviderID: p.ID,
		ModelID: "video", MediaType: "video", Status: store.CreativeJobProcessing, ChargeStatus: "charged", ChargeAmount: 0.2,
		UpstreamStatus: "completed", ResultJSON: `{"video":{"url":"https://cdn.example.com/video.mp4"}}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	svc := New(st, nil, nil)
	cause := errors.New("disk full")
	svc.handleVideoArchiveFailure(ctx, job, cause)
	saved, err := st.GetCreativeJob(ctx, job.ID, job.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Status != store.CreativeJobProcessing || saved.ErrorCode != videoArchiveRetryCode || !strings.Contains(saved.ErrorMessage, "第 1/3 次") {
		t.Fatalf("first archive failure state=%+v", saved)
	}
	if !svc.videoArchiveRetryCoolingDown(ctx, saved) {
		t.Fatal("archive retry should cool down after first failure")
	}

	svc.handleVideoArchiveFailure(ctx, saved, cause)
	saved, err = st.GetCreativeJob(ctx, job.ID, job.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Status != store.CreativeJobProcessing || !strings.Contains(saved.ErrorMessage, "第 2/3 次") {
		t.Fatalf("second archive failure state=%+v", saved)
	}
	svc.handleVideoArchiveFailure(ctx, saved, cause)
	saved, err = st.GetCreativeJob(ctx, job.ID, job.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Status != store.CreativeJobFailed || saved.ErrorCode != videoArchiveFailureEvent || saved.ChargeStatus != "refund_pending" {
		t.Fatalf("third archive failure state=%+v", saved)
	}
	count, _, _, err := st.CreativeJobEventStats(ctx, job.ID, videoArchiveFailureEvent)
	if err != nil || count != 3 {
		t.Fatalf("archive failure events count=%d err=%v", count, err)
	}
}

func TestValidateImageDataURL(t *testing.T) {
	validPNG := []byte{'\x89', 'P', 'N', 'G', '\r', '\n', '\x1a', '\n'}
	valid := "data:image/png;base64," + base64.StdEncoding.EncodeToString(validPNG)
	if err := ValidateImageDataURL(valid); err != nil {
		t.Fatalf("valid image data URL rejected: %v", err)
	}
	if err := ValidateImageDataURL("https://example.com/a.png"); err == nil {
		t.Fatal("remote image URL must not be accepted as upload data")
	}
	spoofed := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("<svg></svg>"))
	if err := ValidateImageDataURL(spoofed); err == nil || !strings.Contains(err.Error(), "不匹配") {
		t.Fatalf("spoofed image content error=%v", err)
	}
	svg := "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte("<svg></svg>"))
	if err := ValidateImageDataURL(svg); err == nil || !strings.Contains(err.Error(), "PNG") {
		t.Fatalf("unsupported image MIME error=%v", err)
	}
}

func TestCreativePromptLengthLimit(t *testing.T) {
	svc := &Service{}
	tooLong := strings.Repeat("界", 4001)
	if _, err := svc.GenerateImage(context.Background(), 7, ImageInput{Prompt: tooLong}); err == nil || !strings.Contains(err.Error(), "4000") {
		t.Fatalf("image prompt length error=%v", err)
	}
	if _, err := svc.CreateVideo(context.Background(), 7, VideoInput{Prompt: tooLong}); err == nil || !strings.Contains(err.Error(), "4000") {
		t.Fatalf("video prompt length error=%v", err)
	}
}

func TestProviderEndpointAcceptsRootOrV1BaseURL(t *testing.T) {
	for _, base := range []string{"https://provider.example.com", "https://provider.example.com/v1"} {
		if got := providerEndpoint(base, "/v1/images/generations"); got != "https://provider.example.com/v1/images/generations" {
			t.Fatalf("providerEndpoint(%q)=%q", base, got)
		}
	}
}

func TestProviderErrorExplainsDisabledImageGroup(t *testing.T) {
	err := providerError(http.StatusForbidden, []byte(`{"error":{"message":"Image generation is not enabled for this group","type":"permission_error"}}`), nil)
	if err == nil || !strings.Contains(err.Error(), "所属分组未开启图片生成功能") {
		t.Fatalf("disabled image group error=%v", err)
	}

	other := providerError(http.StatusForbidden, []byte(`{"error":{"message":"quota denied","type":"permission_error"}}`), nil)
	if other == nil || !strings.Contains(other.Error(), "上游 HTTP 403") || strings.Contains(other.Error(), "所属分组") {
		t.Fatalf("unrelated permission error=%v", other)
	}
}

func TestProviderErrorExplainsVideoModerationRejection(t *testing.T) {
	err := providerError(http.StatusBadRequest, []byte(`{"error":{"message":"Generated video rejected by content moderation"}}`), nil)
	if err == nil || err.Error() != "视频未通过内容安全审核，请调整提示词或参考图后重试" {
		t.Fatalf("video moderation error=%v", err)
	}
}

func TestDeleteJobRejectsActiveAndRefundPendingJobs(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "creative-delete-service.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	p, err := st.SaveCreativeProvider(ctx, store.CreativeProvider{Name: "provider", Kind: ProviderOpenAI, BaseURL: "https://provider.example.com", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	svc := New(st, nil, nil)
	active, err := st.CreateCreativeJob(ctx, store.CreativeJob{OrderNo: "cr_active", RequestKey: "active", UserID: 7, ProviderID: p.ID, ModelID: "image", MediaType: "image", Status: store.CreativeJobProcessing})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteJob(ctx, active.ID, 7); err == nil || !strings.Contains(err.Error(), "生成中") {
		t.Fatalf("active delete error=%v", err)
	}
	refunding, err := st.CreateCreativeJob(ctx, store.CreativeJob{OrderNo: "cr_refunding", RequestKey: "refunding", UserID: 7, ProviderID: p.ID, ModelID: "image", MediaType: "image", Status: store.CreativeJobFailed, ChargeStatus: "refund_pending"})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteJob(ctx, refunding.ID, 7); err == nil || !strings.Contains(err.Error(), "退款") {
		t.Fatalf("refund pending delete error=%v", err)
	}
	completed, err := st.CreateCreativeJob(ctx, store.CreativeJob{OrderNo: "cr_completed", RequestKey: "completed", UserID: 7, ProviderID: p.ID, ModelID: "image", MediaType: "image", Status: store.CreativeJobCompleted, ChargeStatus: "charged"})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteJob(ctx, completed.ID, 7); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetCreativeJob(ctx, completed.ID, 7); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted completed job error=%v", err)
	}
}

func TestSaveExternalProviderRejectsUnsafeBaseURL(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "creative.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc := New(st, nil, nil)
	for _, baseURL := range []string{"provider.example.com", "file:///tmp/provider", "https://user:secret@provider.example.com"} {
		if _, err := svc.SaveProvider(context.Background(), store.CreativeProvider{Name: "external", Kind: ProviderOpenAI, BaseURL: baseURL, APIKey: "global-key", Enabled: true}); err == nil || !strings.Contains(err.Error(), "HTTP(S)") {
			t.Fatalf("baseURL=%q error=%v", baseURL, err)
		}
	}
}

func TestValidateModelConstraints(t *testing.T) {
	m := store.CreativeModel{
		Capability:      CapabilityImage,
		ConstraintsJSON: `{"resolutions":["1k"],"aspect_ratios":["1:1"],"max_images":2}`,
	}
	if err := validateModelConstraints(m, "1:1", "1k", 2); err != nil {
		t.Fatalf("valid constraints rejected: %v", err)
	}
	if err := validateModelConstraints(m, "16:9", "1k", 1); err == nil {
		t.Fatal("unsupported aspect ratio must be rejected")
	}
	if err := validateModelConstraints(m, "1:1", "2k", 1); err == nil {
		t.Fatal("unsupported resolution must be rejected")
	}
	if err := validateModelConstraints(m, "1:1", "1k", 3); err == nil {
		t.Fatal("image count above model limit must be rejected")
	}
}

func TestSaveVideoModelUsesOnlyPricedResolutions(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "creative.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	p, err := st.SaveCreativeProvider(context.Background(), store.CreativeProvider{Name: "external", Kind: ProviderOpenAI, BaseURL: "https://provider.example.com", APIKey: "key", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	svc := New(st, nil, nil)
	m, err := svc.SaveModel(context.Background(), store.CreativeModel{
		ProviderID: p.ID, ModelID: "video-model", Capability: CapabilityVideo, Protocol: ProtocolVideo,
		PriceJSON:       `{"currency":"USD","unit":"second","resolutions":{"480p":0.08,"720p":0,"1080p":0.22}}`,
		ConstraintsJSON: `{"resolutions":["480p","720p"],"aspect_ratios":["16:9"],"duration_min":1,"duration_max":15}`,
		Enabled:         true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var constraints Constraints
	if err := json.Unmarshal([]byte(m.ConstraintsJSON), &constraints); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(constraints.Resolutions, []string{"480p", "1080p"}) {
		t.Fatalf("saved resolutions=%v", constraints.Resolutions)
	}
	options, err := svc.ModelOptions(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(options) != 1 || !reflect.DeepEqual(options[0].Constraints.Resolutions, []string{"480p", "1080p"}) {
		t.Fatalf("model options=%+v", options)
	}
	if _, _, _, err := svc.resolveModel(context.Background(), m.ID, CapabilityVideo, "720p", 5); err == nil || !strings.Contains(err.Error(), "有效价格") {
		t.Fatalf("unpriced resolution error=%v", err)
	}
}

func TestOpenJobContentDoesNotLeakProviderKeyToExternalURL(t *testing.T) {
	var gotAuthorization string
	media := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthorization = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("image"))
	}))
	defer media.Close()

	st, err := store.Open(filepath.Join(t.TempDir(), "creative.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	p, err := st.SaveCreativeProvider(context.Background(), store.CreativeProvider{
		Name: "provider", BaseURL: "https://provider.example.com", APIKey: "provider-secret", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	publicMediaURL, mediaClient := publicURLForTestServer(t, media, "/asset.png")
	svc := &Service{store: st, client: mediaClient}
	job := &store.CreativeJob{
		ProviderID: p.ID, MediaType: "image", Status: store.CreativeJobCompleted,
		ResultJSON: `{"data":[{"url":"` + publicMediaURL + `"}]}`,
	}
	content, err := svc.OpenJobContent(context.Background(), job, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	defer content.Body.Close()
	if _, err := io.ReadAll(content.Body); err != nil {
		t.Fatal(err)
	}
	if gotAuthorization != "" {
		t.Fatalf("provider key leaked to external media host: %q", gotAuthorization)
	}
	if content.ContentType != "image/png" {
		t.Fatalf("content type=%q", content.ContentType)
	}
}

func publicURLForTestServer(t *testing.T, server *httptest.Server, path string) (string, *http.Client) {
	t.Helper()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, parsed.Host)
		},
	}
	t.Cleanup(transport.CloseIdleConnections)
	return "http://198.51.100.10" + path, &http.Client{Transport: transport}
}

func TestOpenJobContentRejectsUnsafeExternalMediaURL(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "creative.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	p, err := st.SaveCreativeProvider(context.Background(), store.CreativeProvider{
		Name: "provider", BaseURL: "https://provider.example.com", APIKey: "provider-secret", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{store: st, client: http.DefaultClient}
	job := &store.CreativeJob{
		ProviderID: p.ID, MediaType: "image", Status: store.CreativeJobCompleted,
		ResultJSON: `{"data":[{"url":"http://127.0.0.1/private.png"}]}`,
	}
	content, err := svc.OpenJobContent(context.Background(), job, 0, "")
	if err == nil {
		if content != nil && content.Body != nil {
			_ = content.Body.Close()
		}
		t.Fatal("unsafe loopback media URL was accepted")
	}
	if !strings.Contains(err.Error(), "内网") && !strings.Contains(err.Error(), "本机") {
		t.Fatalf("unsafe media error=%v", err)
	}
}

func TestOpenJobContentForwardsVideoRange(t *testing.T) {
	var gotRange string
	media := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRange = r.Header.Get("Range")
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Range", "bytes 0-3/10")
		w.Header().Set("Content-Length", "4")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("data"))
	}))
	defer media.Close()
	st, err := store.Open(filepath.Join(t.TempDir(), "creative.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	p, err := st.SaveCreativeProvider(context.Background(), store.CreativeProvider{Name: "provider", BaseURL: media.URL, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{store: st, client: media.Client()}
	job := &store.CreativeJob{ProviderID: p.ID, MediaType: "video", Status: store.CreativeJobCompleted, ResultJSON: `{"video":{"url":"` + media.URL + `/video.mp4"}}`}
	content, err := svc.OpenJobContent(context.Background(), job, 0, "bytes=0-3")
	if err != nil {
		t.Fatal(err)
	}
	defer content.Body.Close()
	if gotRange != "bytes=0-3" || content.StatusCode != http.StatusPartialContent || content.ContentRange != "bytes 0-3/10" || content.AcceptRanges != "bytes" || content.ContentLength != 4 {
		t.Fatalf("range=%q content=%+v", gotRange, content)
	}
}

func TestAccountPoolSyncUsesHealthyAccountModelMappings(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/admin/accounts" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":0,"data":{"items":[`+
			`{"id":1,"name":"healthy","schedulable":true,"status":"active","credentials":{"model_mapping":{"grok-imagine-image":"image","grok-imagine-video":"video"}}},`+
			`{"id":2,"name":"paused","schedulable":false,"status":"active","credentials":{"model_mapping":{"future-image-model":"image"}}}`+
			`],"page":1,"pages":1,"total":2}}`)
	}))
	defer upstream.Close()

	st, err := store.Open(filepath.Join(t.TempDir(), "creative.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	p, err := st.SaveCreativeProvider(context.Background(), store.CreativeProvider{
		Name: "pool", Kind: ProviderPool, BaseURL: upstream.URL, APIKey: "media-key", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	client := sub2api.New(upstream.URL, "admin-key", time.Second)
	svc := New(st, client, nil)
	res, err := svc.SyncProviderModels(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Synced != 2 || res.Added != 2 || res.Kept != 0 || res.Removed != 0 {
		t.Fatalf("sync result=%+v", res)
	}
	models, err := svc.ListModels(context.Background(), p.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 {
		t.Fatalf("models=%+v", models)
	}
	for _, model := range models {
		if model.AvailableAccounts != 1 || !model.Enabled {
			t.Fatalf("model availability/default state=%+v", model)
		}
	}
}

func TestAccountPoolSyncRemovesMissingModels(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/admin/accounts" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":0,"data":{"items":[{"id":1,"name":"healthy","schedulable":true,"status":"active","credentials":{"model_mapping":{"grok-imagine-image":"image"}}}],"page":1,"pages":1,"total":1}}`)
	}))
	defer upstream.Close()

	st, err := store.Open(filepath.Join(t.TempDir(), "creative-sync-clean.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	p, err := st.SaveCreativeProvider(context.Background(), store.CreativeProvider{Name: "pool", Kind: ProviderPool, BaseURL: upstream.URL, APIKey: "media-key", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.UpsertCreativeModel(context.Background(), store.CreativeModel{ProviderID: p.ID, ModelID: "grok-imagine-image", DisplayName: "old image", Capability: CapabilityImage, Protocol: ProtocolImages, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.UpsertCreativeModel(context.Background(), store.CreativeModel{ProviderID: p.ID, ModelID: "legacy-video-model", DisplayName: "legacy", Capability: CapabilityVideo, Protocol: ProtocolVideo, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	svc := New(st, sub2api.New(upstream.URL, "admin-key", time.Second), nil)
	res, err := svc.SyncProviderModels(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Synced != 1 || res.Added != 0 || res.Kept != 1 || res.Removed != 1 {
		t.Fatalf("sync result=%+v", res)
	}
	models, err := svc.ListModels(context.Background(), p.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ModelID != "grok-imagine-image" {
		t.Fatalf("models=%+v", models)
	}
}

func TestAccountPoolModelsFailClosedWhenAccountDiscoveryFails(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "temporary failure", http.StatusBadGateway)
	}))
	defer upstream.Close()

	st, err := store.Open(filepath.Join(t.TempDir(), "creative.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	p, err := st.SaveCreativeProvider(context.Background(), store.CreativeProvider{
		Name: "pool", Kind: ProviderPool, BaseURL: upstream.URL, APIKey: "media-key", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.UpsertCreativeModel(context.Background(), store.CreativeModel{
		ProviderID: p.ID, ModelID: "grok-imagine-image", Capability: CapabilityImage,
		Protocol: ProtocolImages, PriceJSON: `{"currency":"USD","unit":"image","fixed":0.02}`,
		ConstraintsJSON: `{"resolutions":["1k"]}`, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	svc := New(st, sub2api.New(upstream.URL, "admin-key", time.Second), nil)
	options, err := svc.ModelOptions(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(options) != 0 {
		t.Fatalf("account discovery failed, but user options remained visible: %+v", options)
	}
}

func TestAccountPoolModelsHiddenAndGenerationRejectedWithoutUserKey(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/admin/accounts" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":0,"data":{"items":[{"id":1,"schedulable":true,"status":"active","credentials":{"model_mapping":{"grok-imagine-image":"image"}}}],"page":1,"pages":1,"total":1}}`)
	}))
	defer upstream.Close()
	st, err := store.Open(filepath.Join(t.TempDir(), "creative.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	p, err := st.SaveCreativeProvider(context.Background(), store.CreativeProvider{Name: "pool", Kind: ProviderPool, BaseURL: upstream.URL, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	m, err := st.UpsertCreativeModel(context.Background(), store.CreativeModel{
		ProviderID: p.ID, ModelID: "grok-imagine-image", Capability: CapabilityImage, Protocol: ProtocolImages,
		PriceJSON: `{"currency":"USD","unit":"image","fixed":0.02}`, ConstraintsJSON: `{"resolutions":["1k"],"aspect_ratios":["1:1"]}`, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	svc := New(st, sub2api.New(upstream.URL, "admin-key", time.Second), nil, "test-credential-secret-with-at-least-32-characters")
	options, err := svc.ModelOptions(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(options) != 0 {
		t.Fatalf("models without user key=%+v", options)
	}
	job, err := svc.GenerateImage(context.Background(), 7, ImageInput{ModelDBID: m.ID, Prompt: "test", Count: 1, AspectRatio: "1:1", Resolution: "1k"})
	if err == nil || !strings.Contains(err.Error(), "请先配置自己的 Sub2API API Key") || job != nil {
		t.Fatalf("job=%+v error=%v", job, err)
	}
	jobs, total, err := st.ListCreativeJobs(context.Background(), store.CreativeJobFilter{UserID: 7})
	if err != nil || total != 0 || len(jobs) != 0 {
		t.Fatalf("unexpected jobs total=%d items=%+v err=%v", total, jobs, err)
	}
}

func TestExternalProviderSyncDoesNotReadAccountPool(t *testing.T) {
	accountCalls := 0
	accounts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		accountCalls++
		http.Error(w, "account pool must not be used", http.StatusInternalServerError)
	}))
	defer accounts.Close()
	external := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer global-provider-key" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":"grok-imagine-image"},{"id":"future-video-model"}]}`)
	}))
	defer external.Close()
	st, err := store.Open(filepath.Join(t.TempDir(), "creative.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	p, err := st.SaveCreativeProvider(context.Background(), store.CreativeProvider{Name: "external", Kind: ProviderOpenAI, BaseURL: external.URL, APIKey: "global-provider-key", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	svc := New(st, sub2api.New(accounts.URL, "admin-key", time.Second), nil)
	res, err := svc.SyncProviderModels(context.Background(), p.ID)
	if err != nil || res.Synced != 2 {
		t.Fatalf("sync result=%+v err=%v", res, err)
	}
	if accountCalls != 0 {
		t.Fatalf("external sync read account pool %d times", accountCalls)
	}
}

func TestCredentialSecretFailsClosedAndActiveVideoBlocksMutation(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "creative.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	p, err := st.SaveCreativeProvider(context.Background(), store.CreativeProvider{Name: "pool", Kind: ProviderPool, BaseURL: "https://provider.example.com", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	good := New(st, nil, nil, "test-credential-secret-with-at-least-32-characters")
	ciphertext, err := encryptCredential(good.credentialKey, 7, p.ID, "user-media-key")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveCreativeUserCredential(context.Background(), store.CreativeUserCredential{UserID: 7, ProviderID: p.ID, APIKeyCipher: ciphertext, KeyHint: "****-key"}); err != nil {
		t.Fatal(err)
	}
	wrong := New(st, nil, nil, "different-credential-secret-with-32-characters")
	if _, err := wrong.providerForUser(context.Background(), 7, *p); err == nil || !strings.Contains(err.Error(), "解密失败") || strings.Contains(err.Error(), "user-media-key") {
		t.Fatalf("wrong secret error=%v", err)
	}
	job, err := st.CreateCreativeJob(context.Background(), store.CreativeJob{
		OrderNo: "video-active", RequestKey: "video-active", UserID: 7, ProviderID: p.ID,
		ModelID: "grok-imagine-video", MediaType: "video", Status: store.CreativeJobProcessing,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := good.DeleteUserCredential(context.Background(), 7, p.ID); err == nil || !strings.Contains(err.Error(), "视频任务生成中") {
		t.Fatalf("active delete error=%v", err)
	}
	if _, err := good.SaveUserCredential(context.Background(), 7, p.ID, "replacement-media-key"); err == nil || !strings.Contains(err.Error(), "视频任务生成中") {
		t.Fatalf("active replace error=%v", err)
	}
	job.Status = store.CreativeJobCompleted
	if err := st.UpdateCreativeJob(context.Background(), *job); err != nil {
		t.Fatal(err)
	}
	if err := good.DeleteUserCredential(context.Background(), 7, p.ID); err != nil {
		t.Fatalf("completed job should allow delete: %v", err)
	}
}

func TestGenerateAccountPoolImageEditUsesEncryptedUserKeyWithoutDoubleCharge(t *testing.T) {
	var gotEditPayload map[string]any
	balanceCalled := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/admin/users/7/balance":
			balanceCalled = true
			http.Error(w, "must not double charge", http.StatusInternalServerError)
		case r.URL.Path == "/api/v1/admin/accounts":
			_, _ = io.WriteString(w, `{"code":0,"data":{"items":[{"id":1,"schedulable":true,"status":"active","credentials":{"model_mapping":{"grok-imagine-image-quality":"image"}}}],"page":1,"pages":1,"total":1}}`)
		case r.URL.Path == "/v1/models":
			if r.Header.Get("Authorization") != "Bearer user-media-key" {
				t.Errorf("model auth=%q", r.Header.Get("Authorization"))
			}
			_, _ = io.WriteString(w, `{"data":[{"id":"grok-imagine-image-quality"}]}`)
		case r.URL.Path == "/v1/images/edits":
			if r.Header.Get("Authorization") != "Bearer user-media-key" {
				t.Errorf("media auth=%q", r.Header.Get("Authorization"))
			}
			if err := json.NewDecoder(r.Body).Decode(&gotEditPayload); err != nil {
				t.Error(err)
			}
			_, _ = io.WriteString(w, `{"data":[{"b64_json":"`+testPNGBase64+`"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	st, err := store.Open(filepath.Join(t.TempDir(), "creative.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	p, err := st.SaveCreativeProvider(context.Background(), store.CreativeProvider{
		Name: "pool", Kind: ProviderPool, BaseURL: upstream.URL, APIKey: "media-key", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, protocol, pricing, constraints, _ := inferModel("grok-imagine-image-quality")
	priceJSON, _ := json.Marshal(pricing)
	constraintsJSON, _ := json.Marshal(constraints)
	m, err := st.UpsertCreativeModel(context.Background(), store.CreativeModel{
		ProviderID: p.ID, ModelID: "grok-imagine-image-quality", Capability: CapabilityImage,
		Protocol: protocol, PriceJSON: string(priceJSON), ConstraintsJSON: string(constraintsJSON), Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	client := sub2api.New(upstream.URL, "admin-key", time.Second)
	svc := New(st, client, credit.New(st, client), "test-credential-secret-with-at-least-32-characters", filepath.Join(t.TempDir(), "creative", "videos"))
	if _, err := svc.SaveUserCredential(context.Background(), 7, p.ID, "user-media-key"); err != nil {
		t.Fatal(err)
	}
	storedCredential, err := st.GetCreativeUserCredential(context.Background(), 7, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(storedCredential.APIKeyCipher, "user-media-key") || storedCredential.KeyHint != "****-key" {
		t.Fatalf("credential was not safely stored: %+v", storedCredential)
	}
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("image"))
	job, err := svc.GenerateImage(context.Background(), 7, ImageInput{
		ModelDBID: m.ID, Prompt: "edit", Count: 1, AspectRatio: "1:1", Resolution: "1k",
		ImageDataURL: dataURL, RequestKey: "edit-request",
	})
	if err != nil {
		t.Fatal(err)
	}
	if job.ChargeAmount != 0.06 || job.ChargeStatus != "upstream" || job.Status != store.CreativeJobCompleted {
		t.Fatalf("job=%+v", job)
	}
	if strings.Contains(job.ResultJSON, "b64_json") {
		t.Fatalf("base64 image leaked into stored result: %s", job.ResultJSON)
	}
	images, err := st.ListCreativeJobImages(context.Background(), job.ID)
	if err != nil || len(images) != 1 || images[0].LocalMediaSize != int64(len(testPNGBytes())) {
		t.Fatalf("local images=%+v err=%v", images, err)
	}
	content, err := svc.OpenJobContent(context.Background(), job, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	gotImage, err := io.ReadAll(content.Body)
	_ = content.Body.Close()
	if err != nil || string(gotImage) != string(testPNGBytes()) {
		t.Fatalf("local image=%x err=%v", gotImage, err)
	}
	if balanceCalled {
		t.Fatal("account-pool request invoked extension balance deduction")
	}
	image, ok := gotEditPayload["image"].(map[string]any)
	if !ok || image["url"] != dataURL {
		t.Fatalf("edit payload=%+v", gotEditPayload)
	}
	if strings.Contains(job.ParamsJSON, "base64") || !strings.Contains(job.ParamsJSON, `"has_reference_image":true`) {
		t.Fatalf("unsafe or incomplete persisted params=%s", job.ParamsJSON)
	}
}

func TestGenerateExternalImageUsesGlobalKeyAndExtensionBilling(t *testing.T) {
	var charged bool
	var providerAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/admin/users/7/balance":
			charged = true
			if r.Method != http.MethodPost || r.Header.Get("x-api-key") != "admin-key" {
				t.Errorf("balance request method=%s auth=%q", r.Method, r.Header.Get("x-api-key"))
			}
			_, _ = io.WriteString(w, `{"code":0,"data":{"id":7,"balance":9.98}}`)
		case "/v1/images/generations":
			providerAuth = r.Header.Get("Authorization")
			_, _ = io.WriteString(w, `{"data":[{"url":"/image.png"}]}`)
		case "/image.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(testPNGBytes())
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	st, err := store.Open(filepath.Join(t.TempDir(), "creative.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	p, err := st.SaveCreativeProvider(context.Background(), store.CreativeProvider{
		Name: "external", Kind: ProviderOpenAI, BaseURL: upstream.URL, APIKey: "global-provider-key", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	m, err := st.UpsertCreativeModel(context.Background(), store.CreativeModel{
		ProviderID: p.ID, ModelID: "external-image", Capability: CapabilityImage, Protocol: ProtocolImages,
		PriceJSON: `{"currency":"USD","unit":"image","fixed":0.02}`, ConstraintsJSON: `{"resolutions":["1k"],"aspect_ratios":["1:1"]}`, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	client := sub2api.New(upstream.URL, "admin-key", time.Second)
	svc := New(st, client, credit.New(st, client), "", filepath.Join(t.TempDir(), "creative", "videos"))
	job, err := svc.GenerateImage(context.Background(), 7, ImageInput{
		ModelDBID: m.ID, Prompt: "generate", Count: 1, AspectRatio: "1:1", Resolution: "1k", RequestKey: "external-request",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !charged || providerAuth != "Bearer global-provider-key" {
		t.Fatalf("charged=%v providerAuth=%q", charged, providerAuth)
	}
	if job.ChargeStatus != "charged" || job.ChargeAmount != 0.02 || job.Status != store.CreativeJobCompleted {
		t.Fatalf("job=%+v", job)
	}
}
