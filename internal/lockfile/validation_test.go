package lockfile

import (
	"strings"
	"testing"

	"github.com/sleuth-io/sx/v2/internal/asset"
)

// TestExtractInvalidSourceGitAssets: a non-SHA source-git ref moves the
// asset into SkippedAssets (so cleanup treats it as present) while the
// remaining assets validate cleanly — one bad entry costs one asset,
// not the whole lock file.
func TestExtractInvalidSourceGitAssets(t *testing.T) {
	sha := strings.Repeat("a", 40)
	lf := &LockFile{
		LockVersion: "1.0",
		Version:     "1",
		CreatedBy:   "test",
		Assets: []Asset{
			{Name: "good", Version: "1.0.0", Type: asset.TypeSkill, SourceGit: &SourceGit{URL: "https://x/y", Ref: sha}},
			{Name: "bad", Version: "1.0.0", Type: asset.TypeSkill, SourceGit: &SourceGit{URL: "https://x/y", Ref: "main"}},
			{Name: "http", Version: "1.0.0", Type: asset.TypeSkill, SourceHTTP: &SourceHTTP{URL: "https://x/z.zip", Hashes: map[string]string{"sha256": strings.Repeat("b", 64)}}},
		},
	}

	invalid := lf.ExtractInvalidSourceGitAssets()
	if len(invalid) != 1 || invalid[0].Name != "bad" {
		t.Fatalf("expected only the bad asset extracted, got %+v", invalid)
	}
	if len(lf.Assets) != 2 {
		t.Fatalf("expected 2 assets kept, got %d", len(lf.Assets))
	}
	if len(lf.SkippedAssets) != 1 || lf.SkippedAssets[0].Name != "bad" {
		t.Fatalf("expected bad asset in SkippedAssets, got %+v", lf.SkippedAssets)
	}
	if err := lf.Validate(); err != nil {
		t.Fatalf("remaining assets must validate: %v", err)
	}
}
