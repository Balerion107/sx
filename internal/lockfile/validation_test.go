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

// TestExtractInvalidSourceGitAssetsTakesDependents: an asset depending
// (transitively) on a skipped asset can never install, and leaving it
// would make Validate fail on the dangling dependency — the wholesale
// failure extraction exists to prevent. The whole chain moves to
// SkippedAssets and the remainder validates.
func TestExtractInvalidSourceGitAssetsTakesDependents(t *testing.T) {
	sha := strings.Repeat("a", 40)
	lf := &LockFile{
		LockVersion: "1.0",
		Version:     "1",
		CreatedBy:   "test",
		Assets: []Asset{
			{Name: "a-bad", Version: "1.0.0", Type: asset.TypeSkill, SourceGit: &SourceGit{URL: "https://x/y", Ref: "main"}},
			{Name: "b-dependent", Version: "1.0.0", Type: asset.TypeSkill, SourceGit: &SourceGit{URL: "https://x/y", Ref: sha},
				Dependencies: []Dependency{{Name: "a-bad"}}},
			{Name: "c-transitive", Version: "1.0.0", Type: asset.TypeSkill, SourceGit: &SourceGit{URL: "https://x/y", Ref: sha},
				Dependencies: []Dependency{{Name: "b-dependent"}}},
			{Name: "d-clean", Version: "1.0.0", Type: asset.TypeSkill, SourceGit: &SourceGit{URL: "https://x/y", Ref: sha}},
		},
	}

	invalid := lf.ExtractInvalidSourceGitAssets()
	if len(invalid) != 3 {
		t.Fatalf("expected the full chain extracted, got %+v", invalid)
	}
	if len(lf.Assets) != 1 || lf.Assets[0].Name != "d-clean" {
		t.Fatalf("expected only d-clean kept, got %+v", lf.Assets)
	}
	if err := lf.Validate(); err != nil {
		t.Fatalf("remaining assets must validate: %v", err)
	}
}

// TestExtractInvalidSourceGitAssetsSparesSatisfiedDependents: dropping
// one bad version of a name must not take down dependents that a
// surviving good version still satisfies. The superseded bad row is
// extracted (it cannot be processed) but not reported — the surviving
// version installs, so warnings and cleanup protection keyed on it
// would mislead.
func TestExtractInvalidSourceGitAssetsSparesSatisfiedDependents(t *testing.T) {
	sha := strings.Repeat("a", 40)
	lf := &LockFile{
		LockVersion: "1.0",
		Version:     "1",
		CreatedBy:   "test",
		Assets: []Asset{
			{Name: "foo", Version: "1.0.0", Type: asset.TypeSkill, SourceGit: &SourceGit{URL: "https://x/y", Ref: sha}},
			{Name: "foo", Version: "2.0.0", Type: asset.TypeSkill, SourceGit: &SourceGit{URL: "https://x/y", Ref: "main"}},
			{Name: "dependent", Version: "1.0.0", Type: asset.TypeSkill, SourceGit: &SourceGit{URL: "https://x/y", Ref: sha},
				Dependencies: []Dependency{{Name: "foo"}}},
		},
	}

	reported := lf.ExtractInvalidSourceGitAssets()
	if len(reported) != 0 {
		t.Fatalf("superseded bad row must not be reported, got %+v", reported)
	}
	if len(lf.SkippedAssets) != 0 {
		t.Fatalf("superseded bad row must not enter SkippedAssets, got %+v", lf.SkippedAssets)
	}
	if len(lf.Assets) != 2 {
		t.Fatalf("expected foo@1.0.0 and dependent kept, got %+v", lf.Assets)
	}
	if err := lf.Validate(); err != nil {
		t.Fatalf("remaining assets must validate: %v", err)
	}
}

// TestExtractInvalidSourceGitAssetsVersionPinnedDependent: a dependency
// pinned to the extracted version is unsatisfied even when another
// version of the name survives — the dependent must be extracted too,
// or Validate rejects the whole lock for the version mismatch.
func TestExtractInvalidSourceGitAssetsVersionPinnedDependent(t *testing.T) {
	sha := strings.Repeat("a", 40)
	lf := &LockFile{
		LockVersion: "1.0",
		Version:     "1",
		CreatedBy:   "test",
		Assets: []Asset{
			{Name: "foo", Version: "1.0.0", Type: asset.TypeSkill, SourceGit: &SourceGit{URL: "https://x/y", Ref: "main"}},
			{Name: "foo", Version: "2.0.0", Type: asset.TypeSkill, SourceGit: &SourceGit{URL: "https://x/y", Ref: sha}},
			{Name: "bar", Version: "1.0.0", Type: asset.TypeSkill, SourceGit: &SourceGit{URL: "https://x/y", Ref: sha},
				Dependencies: []Dependency{{Name: "foo", Version: "1.0.0"}}},
		},
	}

	reported := lf.ExtractInvalidSourceGitAssets()
	if len(reported) != 1 || reported[0].Name != "bar" {
		t.Fatalf("expected only bar reported (foo superseded), got %+v", reported)
	}
	if len(lf.Assets) != 1 || lf.Assets[0].Version != "2.0.0" {
		t.Fatalf("expected only foo@2.0.0 kept, got %+v", lf.Assets)
	}
	if err := lf.Validate(); err != nil {
		t.Fatalf("remaining assets must validate: %v", err)
	}
}

// TestExtractInvalidSourceGitAssetsMultiVersionDependent: when the
// dependent itself has a surviving version, the chain stops there —
// consumers of the surviving version are untouched.
func TestExtractInvalidSourceGitAssetsMultiVersionDependent(t *testing.T) {
	sha := strings.Repeat("a", 40)
	lf := &LockFile{
		LockVersion: "1.0",
		Version:     "1",
		CreatedBy:   "test",
		Assets: []Asset{
			{Name: "a", Version: "1.0.0", Type: asset.TypeSkill, SourceGit: &SourceGit{URL: "https://x/y", Ref: "main"}},
			{Name: "b", Version: "1.0.0", Type: asset.TypeSkill, SourceGit: &SourceGit{URL: "https://x/y", Ref: sha},
				Dependencies: []Dependency{{Name: "a"}}},
			{Name: "b", Version: "2.0.0", Type: asset.TypeSkill, SourceGit: &SourceGit{URL: "https://x/y", Ref: sha}},
			{Name: "c", Version: "1.0.0", Type: asset.TypeSkill, SourceGit: &SourceGit{URL: "https://x/y", Ref: sha},
				Dependencies: []Dependency{{Name: "b"}}},
		},
	}

	reported := lf.ExtractInvalidSourceGitAssets()
	if len(reported) != 1 || reported[0].Name != "a" {
		t.Fatalf("expected only a reported (b@1 superseded, c satisfied), got %+v", reported)
	}
	names := []string{}
	for _, x := range lf.Assets {
		names = append(names, x.Name+"@"+x.Version)
	}
	if len(lf.Assets) != 2 || lf.Assets[0].Key() != "b@2.0.0" || lf.Assets[1].Name != "c" {
		t.Fatalf("expected b@2.0.0 and c kept, got %v", names)
	}
	if err := lf.Validate(); err != nil {
		t.Fatalf("remaining assets must validate: %v", err)
	}
}
