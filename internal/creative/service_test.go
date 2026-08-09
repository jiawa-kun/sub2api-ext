package creative

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"sub2api-ext/internal/store"
)

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

func TestValidateImageDataURL(t *testing.T) {
	valid := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("png"))
	if err := ValidateImageDataURL(valid); err != nil {
		t.Fatalf("valid image data URL rejected: %v", err)
	}
	if err := ValidateImageDataURL("https://example.com/a.png"); err == nil {
		t.Fatal("remote image URL must not be accepted as upload data")
	}
}

func TestProviderEndpointAcceptsRootOrV1BaseURL(t *testing.T) {
	for _, base := range []string{"https://provider.example.com", "https://provider.example.com/v1"} {
		if got := providerEndpoint(base, "/v1/images/generations"); got != "https://provider.example.com/v1/images/generations" {
			t.Fatalf("providerEndpoint(%q)=%q", base, got)
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
	svc := &Service{store: st, client: media.Client()}
	job := &store.CreativeJob{
		ProviderID: p.ID, MediaType: "image", Status: store.CreativeJobCompleted,
		ResultJSON: `{"data":[{"url":"` + media.URL + `/asset.png"}]}`,
	}
	body, contentType, _, err := svc.OpenJobContent(context.Background(), job, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	if _, err := io.ReadAll(body); err != nil {
		t.Fatal(err)
	}
	if gotAuthorization != "" {
		t.Fatalf("provider key leaked to external media host: %q", gotAuthorization)
	}
	if contentType != "image/png" {
		t.Fatalf("content type=%q", contentType)
	}
}
