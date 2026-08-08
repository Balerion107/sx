package commands

import (
	"bytes"
	"context"
	"testing"

	"github.com/sleuth-io/sx/v2/internal/assets"
	"github.com/sleuth-io/sx/v2/internal/lockfile"
	"github.com/sleuth-io/sx/v2/internal/scope"
	"github.com/sleuth-io/sx/v2/internal/ui"
)

// TestSkippedAssetNames collects skipped names across profiles; cleanup
// consults this set so a fetch-time skip is never read as a removal.
func TestSkippedAssetNames(t *testing.T) {
	a := buildProfileLock("default", "kept")
	a.LockFile.SkippedAssets = []lockfile.Asset{{Name: "bad-a"}}
	b := buildProfileLock("work", "other")
	b.LockFile.SkippedAssets = []lockfile.Asset{{Name: "bad-b"}}
	failed := profileLockFile{ProfileName: "broken"} // no lock file

	names := skippedAssetNames([]profileLockFile{a, b, failed})
	if len(names) != 2 || !names["bad-a"] || !names["bad-b"] {
		t.Fatalf("expected {bad-a, bad-b}, got %v", names)
	}
}

// TestCleanupRemovedAssetsSpares SkippedAssets: an installed asset that
// was skipped at fetch time is broken, not removed — cleanup must leave
// its install (and tracker entry) alone, while a genuinely absent asset
// is still cleaned up.
func TestCleanupRemovedAssetsSparesSkipped(t *testing.T) {
	newTracker := func() *assets.Tracker {
		tr := &assets.Tracker{Version: assets.TrackerFormatVersion}
		tr.UpsertAsset(assets.InstalledAsset{Name: "bad", Version: "1.0.0", Type: "skill"})
		return tr
	}
	styledOut := ui.NewOutput(&bytes.Buffer{}, &bytes.Buffer{})
	currentScope := &scope.Scope{}

	// Skipped: the tracker entry must survive.
	tr := newTracker()
	cleanupRemovedAssets(context.Background(), tr, nil, map[string]bool{"bad": true}, nil, currentScope, nil, styledOut)
	if len(tr.Assets) != 1 {
		t.Fatalf("skipped asset was cleaned up: tracker = %+v", tr.Assets)
	}

	// Not skipped (genuinely absent from the lock): cleaned up.
	tr = newTracker()
	cleanupRemovedAssets(context.Background(), tr, nil, nil, nil, currentScope, nil, styledOut)
	if len(tr.Assets) != 0 {
		t.Fatalf("absent asset was not cleaned up: tracker = %+v", tr.Assets)
	}
}
