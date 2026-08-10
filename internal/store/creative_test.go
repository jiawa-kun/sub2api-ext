package store_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"sub2api-ext/internal/store"
)

func TestCreativeProviderSecretAndJobIsolation(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "creative.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()

	p, err := st.SaveCreativeProvider(ctx, store.CreativeProvider{
		Name: "media", Kind: "openai_compatible", BaseURL: "https://media.example.com/",
		APIKey: "secret-key", SourceGroup: "grok", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.BaseURL != "https://media.example.com" || p.APIKey != "secret-key" {
		t.Fatalf("provider normalization failed: %+v", p)
	}
	p.Name = "media-updated"
	p.APIKey = ""
	p, err = st.SaveCreativeProvider(ctx, *p)
	if err != nil {
		t.Fatal(err)
	}
	if p.APIKey != "secret-key" {
		t.Fatal("blank provider key update must preserve the configured key")
	}

	job, err := st.CreateCreativeJob(ctx, store.CreativeJob{
		OrderNo: "cr_test_1", RequestKey: "request-1", UserID: 101, ProviderID: p.ID,
		ModelID: "grok-imagine-image", MediaType: "image", Prompt: "test",
		ChargeAmount: 0.02, ChargeStatus: "charged", Status: store.CreativeJobProcessing,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetCreativeJob(ctx, job.ID, 101); err != nil {
		t.Fatalf("owner should read job: %v", err)
	}
	if _, err := st.GetCreativeJob(ctx, job.ID, 202); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("other user must not read job, got %v", err)
	}
	if _, err := st.GetCreativeJobByRequestKey(ctx, 101, "request-1"); err != nil {
		t.Fatalf("request key lookup failed: %v", err)
	}
	if _, err := st.CreateCreativeJob(ctx, store.CreativeJob{
		OrderNo: "cr_test_2", RequestKey: "request-1", UserID: 101, ProviderID: p.ID,
		ModelID: "grok-imagine-image", MediaType: "image",
	}); err == nil {
		t.Fatal("duplicate request key for the same user must fail")
	}
	if _, err := st.CreateCreativeJob(ctx, store.CreativeJob{
		OrderNo: "cr_test_3", RequestKey: "request-1", UserID: 202, ProviderID: p.ID,
		ModelID: "grok-imagine-image", MediaType: "image",
	}); err != nil {
		t.Fatalf("different users may use the same request key: %v", err)
	}

	items, total, err := st.ListCreativeJobs(ctx, store.CreativeJobFilter{UserID: 101, Limit: 10})
	if err != nil || total != 1 || len(items) != 1 || items[0].UserID != 101 {
		t.Fatalf("user job list isolation failed: total=%d items=%+v err=%v", total, items, err)
	}
	job.Status = store.CreativeJobFailed
	job.ChargeStatus = "refund_pending"
	if err := st.UpdateCreativeJob(ctx, *job); err != nil {
		t.Fatal(err)
	}
	pending, err := st.ListPendingCreativeRefunds(ctx, 10)
	if err != nil || len(pending) != 1 || pending[0].ID != job.ID {
		t.Fatalf("pending refunds=%+v err=%v", pending, err)
	}
}

func TestCreativeJobLogicalDeletePreservesAuditRecord(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "creative-delete.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	p, err := st.SaveCreativeProvider(ctx, store.CreativeProvider{Name: "media", Kind: "openai_compatible", BaseURL: "https://media.example.com", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	job, err := st.CreateCreativeJob(ctx, store.CreativeJob{OrderNo: "cr_delete", RequestKey: "delete-1", UserID: 101, ProviderID: p.ID, ModelID: "image", MediaType: "image", Status: store.CreativeJobCompleted})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.HideCreativeJob(ctx, job.ID, 202); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("other user delete error=%v", err)
	}
	if err := st.HideCreativeJob(ctx, job.ID, 101); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetCreativeJob(ctx, job.ID, 101); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("hidden job remains visible to owner: %v", err)
	}
	audit, err := st.GetCreativeJob(ctx, job.ID, 0)
	if err != nil || audit.DeletedAt == nil {
		t.Fatalf("audit job=%+v err=%v", audit, err)
	}
	items, total, err := st.ListCreativeJobs(ctx, store.CreativeJobFilter{UserID: 101, Limit: 10})
	if err != nil || total != 0 || len(items) != 0 {
		t.Fatalf("user list total=%d items=%+v err=%v", total, items, err)
	}
	items, total, err = st.ListCreativeJobs(ctx, store.CreativeJobFilter{UserID: 101, Limit: 10, IncludeDeleted: true})
	if err != nil || total != 1 || len(items) != 1 || items[0].DeletedAt == nil {
		t.Fatalf("audit list total=%d items=%+v err=%v", total, items, err)
	}
}
