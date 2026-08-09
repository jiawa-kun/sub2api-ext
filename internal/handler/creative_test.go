package handler

import (
	"encoding/json"
	"strings"
	"testing"

	"sub2api-ext/internal/store"
)

func TestCreativeProviderPublicRedactsAPIKey(t *testing.T) {
	out := providerPublic(store.CreativeProvider{ID: 1, Name: "media", APIKey: "abcd-secret-wxyz"})
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if strings.Contains(text, "abcd-secret-wxyz") || strings.Contains(text, "secret") {
		t.Fatalf("provider API leaked key: %s", text)
	}
	if out["api_key_configured"] != true || out["api_key_masked"] != "abcd****wxyz" {
		t.Fatalf("provider key metadata unexpected: %+v", out)
	}
}

func TestCreativeJobPublicDoesNotExposeUpstreamResult(t *testing.T) {
	out := creativeJobPublic(&store.CreativeJob{
		ID: 1, UserID: 7, MediaType: "image", ResultJSON: `{"data":[{"url":"https://upstream.example/secret.png"}]}`,
	})
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if strings.Contains(text, "upstream.example") || strings.Contains(text, "result_json") {
		t.Fatalf("public creative job leaked upstream result: %s", text)
	}
	if !strings.Contains(text, `"content_count":1`) {
		t.Fatalf("public creative job missing content count: %s", text)
	}
}
