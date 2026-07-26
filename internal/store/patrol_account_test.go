package store_test

import (
	"context"
	"path/filepath"
	"testing"

	"sub2api-ext/internal/store"
)

func TestPatrolAccountStateStreakAndReset(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()

	// first failure -> streak 1
	n, err := st.UpsertPatrolAccountFail(ctx, 7, "acc-7", "group-a", "模型 gpt-5.4 异常：timeout")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("first streak = %d, want 1", n)
	}

	// second failure -> streak 2
	n, err = st.UpsertPatrolAccountFail(ctx, 7, "acc-7", "group-a", "模型 gpt-5.4 异常：timeout")
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("second streak = %d, want 2", n)
	}

	// success resets the streak
	if err := st.ResetPatrolAccountOK(ctx, 7, "acc-7", "group-a"); err != nil {
		t.Fatal(err)
	}
	n, err = st.UpsertPatrolAccountFail(ctx, 7, "acc-7", "group-a", "again")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("streak after reset = %d, want 1", n)
	}
}

func TestListPatrolAccountStatesOnlyProblem(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()

	if _, err := st.UpsertPatrolAccountFail(ctx, 1, "bad", "g", "boom"); err != nil {
		t.Fatal(err)
	}
	if err := st.ResetPatrolAccountOK(ctx, 2, "good", "g"); err != nil {
		t.Fatal(err)
	}

	all, err := st.ListPatrolAccountStates(ctx, false, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("all = %d, want 2", len(all))
	}

	problem, err := st.ListPatrolAccountStates(ctx, true, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(problem) != 1 || problem[0].AccountID != 1 {
		t.Fatalf("problem = %+v", problem)
	}
	if problem[0].LastReason != "boom" || problem[0].LastStatus != "fail" {
		t.Fatalf("unexpected row: %+v", problem[0])
	}
}

func TestMarkAndDeletePatrolAccountState(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()

	if _, err := st.UpsertPatrolAccountFail(ctx, 9, "acc", "g", "boom"); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkPatrolAccountAction(ctx, 9, "pending"); err != nil {
		t.Fatal(err)
	}
	rows, err := st.ListPatrolAccountStates(ctx, true, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].LastAction != "pending" {
		t.Fatalf("rows = %+v", rows)
	}

	if err := st.DeletePatrolAccountState(ctx, 9); err != nil {
		t.Fatal(err)
	}
	rows, err = st.ListPatrolAccountStates(ctx, false, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("rows after delete = %+v", rows)
	}
}

// Reopening an existing database must add the new table without data loss,
// covering the upgrade path for already-deployed instances.
func TestPatrolAccountStateMigrationIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "upgrade.db")
	ctx := context.Background()

	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertPatrolAccountFail(ctx, 5, "acc", "g", "boom"); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	// re-run migrations against the existing file
	st2, err := store.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer st2.Close()

	rows, err := st2.ListPatrolAccountStates(ctx, false, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].AccountID != 5 || rows[0].ConsecutiveFail != 1 {
		t.Fatalf("rows after reopen = %+v", rows)
	}
}
