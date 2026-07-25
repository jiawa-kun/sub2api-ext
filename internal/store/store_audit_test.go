package store_test

import (
	"context"
	"path/filepath"
	"testing"

	"sub2api-ext/internal/store"
)

func TestSettingsAudit(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	id, err := st.InsertSettingsAudit(ctx, store.SettingsAudit{
		ActorType: "server_admin",
		ActorName: "server-admin",
		Source:    "api",
		OldJSON:   `{"enabled":true}`,
		NewJSON:   `{"enabled":false}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if id <= 0 {
		t.Fatalf("id=%d", id)
	}
	list, err := st.ListSettingsAudit(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("len=%d", len(list))
	}
	if list[0].ActorName != "server-admin" {
		t.Fatalf("name=%s", list[0].ActorName)
	}
}
