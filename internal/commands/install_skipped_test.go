package commands

import (
	"bytes"
	"context"
	"strings"
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

// TestPrintDryRunSkippedLockAssets: dry-run is where users ask "why
// isn't my asset installing" — the skipped set must appear with the
// same reason strings the install warning uses, deduped across
// profiles.
func TestPrintDryRunSkippedLockAssets(t *testing.T) {
	badRef := lockfile.Asset{
		Name: "bad-ref", Version: "1.0.0",
		SourceGit: &lockfile.SourceGit{URL: "https://x/y", Ref: "main"},
	}
	dependent := lockfile.Asset{Name: "dependent", Version: "1.0.0"}

	a := buildProfileLock("default")
	a.LockFile.SkippedAssets = []lockfile.Asset{badRef, dependent}
	b := buildProfileLock("work")
	b.LockFile.SkippedAssets = []lockfile.Asset{badRef} // duplicate across profiles

	var buf bytes.Buffer
	printDryRunSkippedLockAssets(&buf, []profileLockFile{a, b})
	out := buf.String()

	if !strings.Contains(out, `skipped bad-ref: source-git ref "main" is not a pinned`) {
		t.Fatalf("missing invalid-ref reason:\n%s", out)
	}
	if !strings.Contains(out, "skipped dependent: it depends on a skipped asset") {
		t.Fatalf("missing dependent reason:\n%s", out)
	}
	if strings.Count(out, "skipped bad-ref:") != 1 {
		t.Fatalf("skips must be deduped across profiles:\n%s", out)
	}
}

// TestSkipStatusFor: an installed copy of a skipped vault entry keeps
// its real status (cleanup deliberately spares it — the files are on
// disk and working); everything else reads as skipped.
func TestSkipStatusFor(t *testing.T) {
	cases := map[AssetStatus]AssetStatus{
		StatusInstalled:    StatusInstalled,
		StatusOutdated:     StatusOutdated,
		StatusNotInstalled: StatusSkipped,
	}
	for in, want := range cases {
		if got := skipStatusFor(in); got != want {
			t.Errorf("skipStatusFor(%s) = %s, want %s", in, got, want)
		}
	}
}
