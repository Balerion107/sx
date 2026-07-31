package commands

import (
	"fmt"
	"strings"

	"github.com/sleuth-io/sx/v2/internal/scope"
	"github.com/sleuth-io/sx/v2/internal/ui"
	"github.com/sleuth-io/sx/v2/internal/ui/components"
	vaultpkg "github.com/sleuth-io/sx/v2/internal/vault"
)

// resolveAddScope decides where an asset is scoped during `sx add`, given the
// asset's current installation (current/installed, resolved by the caller):
//   - scope flags given → pre-fill that scope and confirm (unless --yes),
//     exactly as if the user had navigated the menu to it;
//   - --yes (or other non-interactive flag) with NO scope flags → inherit the
//     existing scope;
//   - otherwise → open the interactive scope editor.
func resolveAddScope(out *outputHelper, v vaultpkg.Vault, name, version string, current []vaultpkg.InstallTarget, installed bool, opts addOptions) (*scopeResult, error) {
	if opts.hasScopeFlags() {
		return resolveScopeFromFlags(out, v, name, current, installed, opts.toScopeFlags(), opts.Yes)
	}
	if opts.isNonInteractive() {
		return opts.getScopes()
	}
	return promptForRepositories(out, name, version, current, installed, v)
}

// resolveScopeFromFlags turns the unified scope flags into a proposed change,
// shows the same diff preview the interactive editor shows, and—unless autoYes—
// asks for confirmation before returning a scopeResult to apply. Flags are a
// shortcut to the menu's outcome, not a way to skip the human's approval. Both
// `sx add` and `sx install` feed their (already-folded) scopeFlags through here,
// so the two commands resolve and confirm scope identically.
func resolveScopeFromFlags(out *outputHelper, v vaultpkg.Vault, name string, current []vaultpkg.InstallTarget, installed bool, flags scopeFlags, autoYes bool) (*scopeResult, error) {
	change, err := resolveScopeFlags(flags)
	if err != nil {
		return nil, err
	}

	styledOut := ui.NewOutput(out.cmd.OutOrStdout(), out.cmd.ErrOrStderr())
	ioc := components.NewIOContext(out.cmd.InOrStdin(), out.cmd.OutOrStdout())

	// The set the asset ends up with: replace swaps the whole set; add merges.
	after := change.Targets
	if change.Mode == scopeAdd {
		after = unionTargets(current, change.Targets)
	}

	styledOut.Newline()
	styledOut.Header("Scope for " + name)
	displayCurrentTargets(current, installed, styledOut)
	if vaultpkg.PersistsRepoHost(v) {
		warnAliasScopeTargets(change.Targets, styledOut)
	}

	if !displayScopeChanges(current, after, styledOut) {
		styledOut.Info("No changes to apply.")
		return &scopeResult{Inherit: true}, nil
	}

	// autoYes skips the approval (for CI/scripts); otherwise the human confirms,
	// just as they would after editing scopes in the menu.
	if !autoYes {
		ok, err := ioc.Confirm("Continue with these changes?", true)
		if err != nil {
			return nil, err
		}
		if !ok {
			styledOut.Info("No changes made")
			return &scopeResult{Inherit: true}, nil
		}
	}

	return &scopeResult{
		ApplyTargets: true,
		Targets:      change.Targets,
		Append:       change.Mode == scopeAdd,
	}, nil
}

// warnAliasScopeTargets notes repo/path scope targets whose host is an
// SSH alias in this machine's ~/.ssh/config. What gets stored stays
// literal — the note makes the trap visible while the user can still
// pass the real hostname so teammates' remotes match the stored scope.
// Callers gate on vaultpkg.PersistsRepoHost: on backends that resolve
// URLs to server-side entities the "storing …" claim would be false.
func warnAliasScopeTargets(targets []vaultpkg.InstallTarget, styledOut *ui.Output) {
	for _, t := range targets {
		if t.Repo == "" {
			continue
		}
		warnAliasRepoURL(t.Repo, styledOut)
	}
}

// warnAliasRepoURL notes when ~/.ssh/config remaps a repo URL's host
// on this machine. The note states the mapping and the stored value
// without prescribing a change: the same two-candidate signal covers
// both a genuine alias (workgit → github.com — the user should pass
// the real hostname) and a transport rewrite of the real host
// (Host github.com / HostName ssh.github.com — the input is already
// correct), and only the user can tell which one theirs is.
func warnAliasRepoURL(repoURL string, styledOut *ui.Output) {
	if mapping, ok := aliasNoteParts(repoURL); ok {
		styledOut.Info(fmt.Sprintf(
			"%s; storing %s — pass the real hostname if %q is a local-only alias",
			mapping.prefix, mapping.literal, mapping.literalHost))
	}
}

// warnAliasRepoURLForRemoval is the removal-path variant: nothing is
// stored by a removal, so the note only states what the input will be
// matched against — which explains a subsequent "no repository
// matching" error when the row was written under a different form.
func warnAliasRepoURLForRemoval(repoURL string, styledOut *ui.Output) {
	if mapping, ok := aliasNoteParts(repoURL); ok {
		styledOut.Info(fmt.Sprintf("%s; matching against %s", mapping.prefix, mapping.literal))
	}
}

type aliasNote struct {
	prefix      string
	literal     string
	literalHost string
}

func aliasNoteParts(repoURL string) (aliasNote, bool) {
	resolved := scope.AliasResolvedForm(repoURL)
	if resolved == "" {
		return aliasNote{}, false
	}
	literal := scope.NormalizeRepoURL(repoURL)
	literalHost, _, _ := strings.Cut(literal, "/")
	resolvedHost, _, _ := strings.Cut(resolved, "/")
	return aliasNote{
		prefix:      fmt.Sprintf("~/.ssh/config maps %q to %q on this machine", literalHost, resolvedHost),
		literal:     literal,
		literalHost: literalHost,
	}, true
}

// unionTargets concatenates two target lists, deduping by display identity so
// the add-mode preview doesn't list a scope the asset already has.
func unionTargets(a, b []vaultpkg.InstallTarget) []vaultpkg.InstallTarget {
	seen := make(map[string]bool)
	out := make([]vaultpkg.InstallTarget, 0, len(a)+len(b))
	for _, t := range a {
		if k := formatTarget(t); !seen[k] {
			seen[k] = true
			out = append(out, t)
		}
	}
	for _, t := range b {
		if k := formatTarget(t); !seen[k] {
			seen[k] = true
			out = append(out, t)
		}
	}
	return out
}
