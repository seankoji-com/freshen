package git

import (
	"context"
	"testing"
)

// ReadLocalCheckout reports the branch's divergence from its upstream, and
// updates it after the checkout creates or receives commits.
func TestReadLocalCheckoutReadsAheadBehind(t *testing.T) {
	f := newSyncFixture(t)
	ctx := context.Background()

	f.pushUpstream()
	runGit(t, f.work, "fetch", "origin")

	got, err := ReadLocalCheckout(ctx, f.work)
	if err != nil {
		t.Fatal(err)
	}
	if !got.HasUpstream || got.Behind != 1 || got.Ahead != 0 {
		t.Fatalf("behind checkout: %+v, want HasUpstream with 0 ahead, 1 behind", got)
	}

	f.commit(f.work, "ahead.txt", "local\n", "local change")
	got, err = ReadLocalCheckout(ctx, f.work)
	if err != nil {
		t.Fatal(err)
	}
	if !got.HasUpstream || got.Ahead != 1 || got.Behind != 1 {
		t.Fatalf("diverged checkout: %+v, want 1 ahead, 1 behind", got)
	}
}

// A branch with no upstream has no divergence to report and does not fail the
// checkout read.
func TestReadLocalCheckoutWithoutUpstream(t *testing.T) {
	f := newSyncFixture(t)
	runGit(t, f.work, "checkout", "-b", "local-only")
	got, err := ReadLocalCheckout(context.Background(), f.work)
	if err != nil {
		t.Fatal(err)
	}
	if got.HasUpstream || got.Ahead != 0 || got.Behind != 0 {
		t.Fatalf("local-only branch: %+v, want no upstream and no divergence", got)
	}
}

// The sync's final emitted snapshot carries the refreshed divergence, so a
// pulled branch stops showing a stale behind count in the TUI.
func TestSyncPublishesFinalDivergence(t *testing.T) {
	f := newSyncFixture(t)
	f.pushUpstream()
	runGit(t, f.work, "fetch", "origin")

	item := &RepoItem{Name: "work", Path: f.work, HasUpstream: true, Behind: 3, Logs: []string{}}
	var last *RepoItem
	SyncRepository(context.Background(), item, func(snapshot *RepoItem) {
		last = snapshot
	}, true)

	if last == nil {
		t.Fatal("sync emitted no snapshot")
	}
	if !last.HasUpstream || last.Behind != 0 || last.Ahead != 0 {
		t.Fatalf("final snapshot divergence: %+v, want up to date", last)
	}
}
