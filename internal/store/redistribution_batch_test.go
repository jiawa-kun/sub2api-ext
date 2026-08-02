package store_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"sub2api-ext/internal/store"
)

func TestRedistributionDraftCancelAndDelete(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "redistribution.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()

	cancelID, err := st.CreateRedistributionBatch(ctx, store.RedistributionBatch{
		Status: store.RedistributionBatchDraft, ConfigJSON: "{}", CreatedAt: time.Now().UTC(),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := st.CancelRedistributionBatch(ctx, cancelID); err != nil || !ok {
		t.Fatalf("cancel ok=%v err=%v", ok, err)
	}
	if ok, err := st.DeleteRedistributionBatch(ctx, cancelID); err != nil || ok {
		t.Fatalf("delete cancelled ok=%v err=%v", ok, err)
	}

	deleteID, err := st.CreateRedistributionBatch(ctx, store.RedistributionBatch{
		Status: store.RedistributionBatchDraft, ConfigJSON: "{}", CreatedAt: time.Now().UTC(),
	}, []store.RedistributionEntry{{UserID: 1, Role: store.RedistributionRoleDonor, Status: store.RedistributionEntryPlanned}})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := st.DeleteRedistributionBatch(ctx, deleteID); err != nil || !ok {
		t.Fatalf("delete ok=%v err=%v", ok, err)
	}
	if _, err := st.GetRedistributionBatch(ctx, deleteID); err == nil {
		t.Fatal("deleted batch still exists")
	}
	if entries, err := st.ListRedistributionEntries(ctx, deleteID, ""); err != nil || len(entries) != 0 {
		t.Fatalf("entries=%d err=%v", len(entries), err)
	}
}

func TestRedistributionPoolLotDrawAndRefundAccounting(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "pool.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC)
	batchID, err := st.CreateRedistributionBatch(ctx, store.RedistributionBatch{Status: store.RedistributionBatchSuccess, CreatedAt: now}, nil)
	if err != nil {
		t.Fatal(err)
	}
	lotID, err := st.CreateRedistributionPoolLot(ctx, store.RedistributionPoolLot{SourceBatchID: batchID, SourceUserID: 7, OriginalAmount: 1, CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ReserveRedistributionPoolDraw(ctx, store.RedistributionPoolDraw{UserID: 8, DrawDate: "2026-08-02", Mode: "fixed", Amount: .4, IdempotencyKey: "draw-8"}, now); err != nil {
		t.Fatal(err)
	}
	if got, err := st.RedistributionAvailablePool(ctx); err != nil || got != .6 {
		t.Fatalf("available=%v err=%v", got, err)
	}
	if err := st.CompleteRedistributionPoolDraw(ctx, 1, 10, ""); err != nil {
		t.Fatal(err)
	}
	refund, err := st.ReserveRedistributionPoolRefund(ctx, lotID, store.PoolRefundManual, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if refund.Amount != .6 {
		t.Fatalf("refund=%+v", refund)
	}
	if err := st.CompleteRedistributionPoolRefund(ctx, refund.ID, 11); err != nil {
		t.Fatal(err)
	}
	if got, err := st.RedistributionAvailablePool(ctx); err != nil || got != 0 {
		t.Fatalf("refunded available=%v err=%v", got, err)
	}
}

func TestRedistributionPoolDrawUniquePerDay(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "draw-unique.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now().UTC()
	if _, err := st.CreateRedistributionPoolLot(ctx, store.RedistributionPoolLot{SourceBatchID: 0, SourceUserID: 7, OriginalAmount: .2, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}); err == nil {
		t.Fatal("pool lot must reference a batch")
	}
	batchID, err := st.CreateRedistributionBatch(ctx, store.RedistributionBatch{Status: store.RedistributionBatchSuccess, CreatedAt: now}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateRedistributionPoolLot(ctx, store.RedistributionPoolLot{SourceBatchID: batchID, SourceUserID: 7, OriginalAmount: .2, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ReserveRedistributionPoolDraw(ctx, store.RedistributionPoolDraw{UserID: 1, DrawDate: "2026-08-02", Mode: "fixed", Amount: .1, IdempotencyKey: "one"}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ReserveRedistributionPoolDraw(ctx, store.RedistributionPoolDraw{UserID: 1, DrawDate: "2026-08-02", Mode: "fixed", Amount: .1, IdempotencyKey: "two"}, now); err == nil {
		t.Fatal("duplicate daily draw must fail")
	}
}
