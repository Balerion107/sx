# Using sx as a Go library

Everything `sx` does to a vault — publishing skills and agents, managing
bots and teams, browsing and downloading assets — is also available as a
Go package. The CLI is one consumer of that package; your own program can
be another.

The package is [`pkg/sxvault`](../pkg/sxvault/sxvault.go). It exposes a
small, stable management facade over the three vault backends (Skills.new,
Git, and local Path) behind a single `Client` type, so you write the same
code regardless of where the vault lives.

```bash
go get github.com/sleuth-io/sx/v2/pkg/sxvault
```

```go
import "github.com/sleuth-io/sx/v2/pkg/sxvault"
```

## Module path and versions

The module path carries a `/v2` suffix, as Go's
[semantic import versioning](https://go.dev/ref/mod#major-version-suffixes)
requires for major version 2 and above.

Releases before this change were tagged `v2.x` without the suffix, which meant
Go could not resolve them at all — `go get` topped out at `v1.8.1` no matter
which `v2` tag was published. If you are pinned to a `v1.x` version, upgrading
is an import-path rewrite and nothing more:

```bash
go mod edit -droprequire github.com/sleuth-io/sx
go get github.com/sleuth-io/sx/v2/pkg/sxvault@latest
# then rewrite imports:
#   github.com/sleuth-io/sx/pkg/... -> github.com/sleuth-io/sx/v2/pkg/...
```

The `pkg/sxvault` API is unchanged by the suffix.

## Scope of the facade

This package intentionally covers the **publish / manage write path** plus
read-only browse and download. As of today that is:

- **Constructors** — `OpenSkillsNew`, `OpenSkillsNewWithOptions`,
  `OpenGit`, `OpenPath`
- **Bots** — `EnsureBot`, `ListBots`, `DeleteBot`, `AddBotTeam`,
  `RemoveBotTeam`
- **Bot runtime tokens** (Skills.new only) — `CreateBotRuntimeToken`,
  `RevokeBotRuntimeTokens`
- **Publishing** — `PutAgent`, `PutSkillZip`
- **Installs** — `InstallAssetToBot`, `UninstallAssetFromBot`
- **Teams** — `ListTeams`
- **Browse / download** — `ListAssets`, `ListAssetsWithOptions`,
  `GetAssetZip`

Read-side primitives the internal vault interface supports (arbitrary
metadata reads, asset removal/rename, broad uninstall) are **not**
re-exported yet — treat their absence as "not yet" rather than "never."

## Opening a client

All three constructors return a `*sxvault.Client`. Every constructor takes
an `Actor` (name + email) used for audit/identity context; the Git backend
also uses it for commit attribution.

### Skills.new (hosted)

```go
client, err := sxvault.OpenSkillsNew("https://app.skills.new", authToken)
if err != nil {
    log.Fatal(err)
}
```

Or with explicit options and an actor:

```go
client, err := sxvault.OpenSkillsNewWithOptions("https://app.skills.new", sxvault.SkillsNewOptions{
    AuthToken: authToken,
    Actor:     sxvault.Actor{Email: "alice@acme.com"},
})
```

The auth token is required — `OpenSkillsNew*` returns an error when it is
empty. Pass an empty server URL to fall back to the default
(`https://app.skills.new`).

### Git vault

```go
client, err := sxvault.OpenGit("git@github.com:acme/skills.git", sxvault.GitOptions{
    Actor: sxvault.Actor{Name: "CI Bot", Email: "ci@acme.com"},
})
```

For HTTP(S) remotes, supply `AuthToken` (and optionally `AuthUsername`,
which defaults to `x-access-token`, or `oauth2` for gitlab.com). For SSH
remotes, set `SSHKeyPath` to scope the key to this client. When both a
token and an SSH key are set on an HTTP(S) URL, the key wins: the URL is
rewritten to SSH at clone time and the token is ignored.

### Local Path vault

```go
client, err := sxvault.OpenPath("/path/to/vault", sxvault.PathOptions{
    Actor: sxvault.Actor{Email: "alice@acme.com"},
})
```

The directory must already exist. A bare path or a `file://` URL both work.

## Common operations

Every method takes a `context.Context` as its first argument.

### Create or update a bot

`EnsureBot` creates the bot if missing and updates its description when you
pass a non-empty, changed value. A new bot requires a description; an empty
description on an existing bot preserves what's stored.

```go
botKey, err := client.EnsureBot(ctx, sxvault.Bot{
    Name:        "reviewer",
    Description: "Reviews pull requests.",
})
// botKey is a one-time raw bot API token, returned only when a Skills.new
// vault creates a new bot. Empty for existing bots and Git vaults.
```

### Publish a skill

Skills are uploaded as a zip. The zip must contain a `SKILL.md` (or the
prompt file named in an embedded `metadata.toml`). Set `BotName` to also
install the skill on a bot in the same call.

```go
err := client.PutSkillZip(ctx, sxvault.SkillZipSpec{
    Name:        "lint-helper",
    Version:     "1.0.0",
    Description: "Runs the project linter and explains failures.",
    ZipData:     zipBytes,
    BotName:     "reviewer", // optional
})
```

### Publish an agent

`PutAgent` ensures the bot, uploads the agent asset, installs it on the
bot, and optionally installs a list of existing vault skills alongside it.

```go
res, err := client.PutAgent(ctx, sxvault.AgentSpec{
    BotName:        "reviewer",
    BotDescription: "Reviews pull requests.",
    AssetName:      "pr-reviewer",
    Version:        "1.2.0",
    Description:    "Reviews PRs against team standards.",
    Prompt:         "You are a meticulous code reviewer...",
    Skills:         []string{"lint-helper"}, // must already exist in the vault
})
// res.BotKey carries the one-time bot token when a new bot was created.
```

`PutAgent` is idempotent but **not** transactional — every step (ensure
bot, upload, install) can be safely retried, so on a mid-way failure just
call it again with the same spec to converge.

### Browse and download assets

```go
skills, err := client.ListAssets(ctx, "skill") // empty type = all types

// Or with search / limit:
results, err := client.ListAssetsWithOptions(ctx, sxvault.ListOptions{
    Type:   "skill",
    Search: "lint",
    Limit:  20,
})

// Download a specific version (empty version = highest semver):
zip, err := client.GetAssetZip(ctx, "lint-helper", "")
// zip.Data holds the raw bytes; zip.Type, zip.Version, zip.Description describe it.
```

Note: Skills.new caps `ListAssets` results at 50 server-side regardless of
`Limit`.

### Bot runtime tokens (Skills.new only)

Short-lived tokens for runtime bot identities. These methods return
`ErrBotRuntimeTokensUnsupported` on Git and Path vaults — match it with
`errors.Is`.

```go
tok, err := client.CreateBotRuntimeToken(ctx, sxvault.BotRuntimeTokenSpec{
    BotName:    "reviewer",
    Label:      "ci-run-4821",
    TTLSeconds: 3600, // 60s–24h; 0 uses the backend default
})
if errors.Is(err, sxvault.ErrBotRuntimeTokensUnsupported) {
    // not a Skills.new vault
}
// tok.Token + tok.ExpiresAt

revoked, err := client.RevokeBotRuntimeTokens(ctx, "reviewer")
```

### Teams

```go
teams, err := client.ListTeams(ctx)            // sorted by name
err = client.AddBotTeam(ctx, "reviewer", "platform")
err = client.RemoveBotTeam(ctx, "reviewer", "platform")
```

## Backend differences worth knowing

- **Re-publishing the same `name@version`** is idempotent for the manifest.
  Stored bytes follow the backend: Skills.new preserves the original
  upload; Git overwrites. Bump the version for a guaranteed update
  everywhere.
- **Bot tokens** are issued by Skills.new only — Git and Path vaults return
  an empty bot key.
- **`ListAssets` search** is a case-insensitive substring match on name or
  description for Git/Path; Skills.new delegates to backend GraphQL search,
  which may rank or fuzzy-match. Don't depend on ordering across backends.
- **Git publish cost** — each skill install on a bot is a separate commit +
  push, so an agent with N skills is roughly N+2 commits per `PutAgent`.

See the package source and its doc comments in
[`pkg/sxvault/sxvault.go`](../pkg/sxvault/sxvault.go) for the full,
authoritative per-field semantics.
