package commands

import (
	"testing"

	"github.com/sleuth-io/sx/v2/internal/asset"
	"github.com/sleuth-io/sx/v2/internal/assets"
	"github.com/sleuth-io/sx/v2/internal/lockfile"
	"github.com/sleuth-io/sx/v2/internal/scope"
)

func TestDetermineAssetStatus_LegacyPortedScopeRow(t *testing.T) {
	stubSSHHostLookup(t)

	// An asset scoped by a legacy ported row installs via the
	// stored-side reading (scope.MatchStoredRepoURL), and the tracker
	// records the live remote. Status lookup must reconcile the same
	// way or the asset reads as permanently not installed.
	legacyRow := "gitea.corp.com:3000/acme/x"
	art := &lockfile.Asset{
		Name:    "infra-skill",
		Version: "1.0.0",
		Type:    asset.TypeSkill,
		Scopes:  []lockfile.Scope{{Repo: legacyRow}},
	}
	tracker := &assets.Tracker{
		Version: assets.TrackerFormatVersion,
		Assets: []assets.InstalledAsset{{
			Name:       "infra-skill",
			Version:    "1.0.0",
			Type:       asset.TypeSkill.Key,
			Repository: "https://gitea.corp.com:3000/acme/x",
			Clients:    []string{"claude-code"},
		}},
	}

	status, _, _ := determineAssetStatus(art, legacyRow, tracker)
	if status != StatusInstalled {
		t.Fatalf("status = %s, want %s (legacy row must reconcile with live remote)", status, StatusInstalled)
	}

	// Sanity: an SSH-remote install of an https-scoped asset also
	// reconciles in the status view.
	art.Scopes = []lockfile.Scope{{Repo: "https://github.com/acme/x"}}
	tracker.Assets[0].Repository = "git@github.com:acme/x.git"
	status, _, _ = determineAssetStatus(art, "https://github.com/acme/x", tracker)
	if status != StatusInstalled {
		t.Fatalf("status = %s, want %s for ssh-remote tracker entry", status, StatusInstalled)
	}
}

// Guard the argument order contract: FindAssetWithMatcher passes
// (installedRepo, storedScopeRepo), and trackerRepoMatch must apply the
// legacy reading to the stored side only.
func TestTrackerRepoMatch_Direction(t *testing.T) {
	stubSSHHostLookup(t)

	if !trackerRepoMatch("https://gitea.corp.com:3000/acme/x", "gitea.corp.com:3000/acme/x") {
		t.Error("live remote should match legacy stored row")
	}
	if !scope.MatchStoredRepoURL("gitea.corp.com:3000/acme/x", "https://gitea.corp.com:3000/acme/x") {
		t.Error("stored-side reading missing from MatchStoredRepoURL")
	}
}
