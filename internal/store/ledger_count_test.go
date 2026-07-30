package store_test

import (
	"context"
	"path/filepath"
	"testing"

	"sub2api-ext/internal/store"
)

func TestCountLedger(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		_, err := st.InsertLedger(ctx, store.LedgerEntry{
			UserID: int64(i + 1), Source: store.LedgerSourceCheckin, Amount: 1,
			IdempotencyKey: "k-" + string(rune('a'+i)), Status: store.LedgerStatusSuccess,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	n, err := st.CountLedger(ctx, store.LedgerFilter{Source: store.LedgerSourceCheckin})
	if err != nil || n != 3 {
		t.Fatalf("n=%d err=%v", n, err)
	}
}
