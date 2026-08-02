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
