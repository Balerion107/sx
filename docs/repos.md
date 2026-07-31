# Repo- and path-scoped installs

`--repo` and `--path` install an asset only for callers working inside
a specific git repository (or a subpath of one). Use repo scope when an
asset is meaningful to one project's codebase but not the whole
organization. Use path scope when a monorepo holds multiple projects
that each want different assets.

For the manifest schema, see
[manifest-spec.md](manifest-spec.md#assetsscopes--install-targets). For
the broader scope picker, see [scoping.md](scoping.md).

## Repo scope

```bash
sx install my-skill --repo git@github.com:acme/myapp.git
```

The asset installs to `<repo>/.claude/` (and the equivalent for any
other configured client) for any caller running `sx install` inside a
clone of the named repo. Outside that repo, the asset is invisible.

Repo URLs are normalized before comparison — `git@github.com:acme/x`,
`https://github.com/acme/x`, and `https://github.com/acme/x.git` all
resolve to the same scope row. This applies to any host (including
GitHub Enterprise and self-hosted git servers), and userinfo, ports,
and a trailing `.git` are ignored. Ignoring ports means sx assumes
one git server per hostname — two servers on different ports of
the same host resolve to the same scope.

SSH host aliases from `~/.ssh/config` are resolved too: with
`Host workgit` / `HostName github.com` configured, a clone whose
remote is `git@workgit:acme/x.git` matches a scope stored as
`https://github.com/acme/x`. Alias resolution reads `~/.ssh/config`
only — `Include` directives, `Match` blocks, and wildcard `Host`
patterns are ignored — and depends on each machine's local config,
so store vault scopes with real hostnames rather than aliases.

### Legacy scope rows

Scope rows written by older sx versions could keep the port
(`gitea.corp.com:3000/acme/x`). Such a stored row is matched under
both readings — with and without the port-like segment — rather
than rewritten, so a genuine numeric path segment is never dropped
from stored data. The details, none of which need action for
ordinary GitHub/GitLab-style `owner/repo` URLs:

- The portless reading applies only to the stored side of a
  comparison; a live remote's numeric segment is never
  reinterpreted.
- It requires at least `owner/repo` after the port-like segment. A
  one-segment legacy row like `ghe.corp:2222/tools` keeps its
  literal reading only and should be re-added with the current URL
  form.
- The flip side: a stored row whose numeric segment is a genuine
  path component (`gitea.corp.com:2024/team/app`) also matches
  clones of `gitea.corp.com/team/app`. If that distinction matters,
  store the row with an explicit scheme and slash form
  (`https://gitea.corp.com/2024/team/app`) so no scp reading
  applies.

> **Vault vs project:** the repo URL in `--repo` is your *project's*
> git remote — the codebase where you want the asset installed — not
> your sx vault, where assets are stored.

You can scope the same asset to multiple repos by passing `--repo` more
than once, either in one command or across several — scope changes
**append** by default, so each new repo is added to the existing set:

```bash
sx install my-skill --repo git@github.com:acme/app-a.git
sx install my-skill --repo git@github.com:acme/app-b.git
```

To make a set of repos the asset's *complete* scope (dropping anything
else it was scoped to), use `--replace-scope`:

```bash
sx install my-skill --replace-scope \
  --repo git@github.com:acme/app-a.git \
  --repo git@github.com:acme/app-b.git
```

## Path scope

Path scope narrows a repo install to one or more subdirectories.
Useful for monorepos where `services/api` wants Python tooling and
`services/web` wants TypeScript tooling.

```bash
sx install my-skill --path "git@github.com:acme/myapp.git#services/api"
```

The format is `<repo-url>#<path>`. Multiple paths in the same repo go
in a comma-separated list:

```bash
sx install my-skill --path "git@github.com:acme/myapp.git#services/api,services/web"
```

The asset installs to `<repo>/<path>/.claude/` for any caller running
`sx install` from inside one of the listed paths. From elsewhere in
the repo it's invisible.

## Setting scope at `sx add` time

`sx add` configures scope when an asset is first published — equivalent
to running `sx install` with the same flag right after:

```bash
# scope a new asset to one repo
sx add my-skill --scope-repo git@github.com:acme/myapp.git

# scope a new asset to specific paths
sx add my-skill --scope-repo "git@github.com:acme/myapp.git#services/api"
```

Already-added assets can have their scope changed by re-running
`sx install <name>` with new scope flags.

## Resolution model

When a caller runs `sx install` inside a working tree, sx reads the
git remote, normalizes the URL, and matches it against every asset's
scope rows:

* `kind = "repo"` matches when the normalized URLs are equal.
* `kind = "path"` matches when the URLs are equal **and** the caller's
  current working directory is inside one of the listed paths.

Outside any matching repo, the asset is filtered out — it doesn't
appear in the resolved lock file and isn't written to any client
directory. `sx install --dry-run` shows you the resolved set without
touching the filesystem.
