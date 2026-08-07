# Environment Variables

sx honors a small set of environment variables that relocate its own
state — config and cache. They work on every platform and are the
supported way to redirect that state in CI, Docker, test harnesses,
demo recordings, or any sandbox.

Note their scope: these variables isolate **sx's own state only**.
Client install targets (`~/.claude`, `~/.codex`, …) are derived from
the home directory, so fully sandboxing `sx install` also requires
overriding `$HOME` — see the recipe below.

## State-relocating variables

### `SX_CONFIG_DIR`

Overrides the **config directory** — where sx reads and writes
`config.json` (vault URL, profile, auth). Point it at an empty
directory and sx starts unconfigured, leaving real user config
untouched.

When unset, sx uses the platform default:

| Platform | Default |
|----------|---------|
| Linux    | `$XDG_CONFIG_HOME/sx` (falls back to `~/.config/sx`) |
| macOS    | `~/Library/Application Support/sx` |
| Windows  | `%AppData%\sx` |

`SKILLS_CONFIG_DIR` is the legacy alias, checked only when
`SX_CONFIG_DIR` is unset. Prefer `SX_CONFIG_DIR` in new setups.

### `SX_CACHE_DIR`

Overrides the **cache directory** — downloaded assets, cloned git
repositories, ETags, and lock files. When unset, sx uses the platform
cache default (e.g. `~/Library/Caches/sx` on macOS, `$XDG_CACHE_HOME/sx`
or `~/.cache/sx` on Linux).

`SKILLS_CACHE_DIR` is the legacy alias, checked only when
`SX_CACHE_DIR` is unset.

## Sandboxing sx completely

Config and cache cover sx's own state, but `sx install` writes assets
into client directories under the home directory. A complete sandbox
overrides all three:

```bash
export HOME="$SANDBOX/home"          # client install targets (~/.claude, …)
export SX_CONFIG_DIR="$SANDBOX/config"
export SX_CACHE_DIR="$SANDBOX/cache"
mkdir -p "$HOME"

sx init --type git --repo-url "$VAULT_URL"
sx install
sx config   # verify: reported config path is under $SX_CONFIG_DIR
```

Always verify isolation with `sx config` — it prints the effective
config path and directories. Also make sure the sandbox environment
doesn't inherit a stray `SX_PROFILE` (below), which would change which
profile a sandboxed run resolves.

## Other variables

These behave as flag or setting equivalents rather than relocating
state:

| Variable | Effect |
|----------|--------|
| `SX_PROFILE` | Selects the active config profile, overriding the one saved in config |
| `SX_STRICT` | `1` makes hook installs that soft-skip count as failures (same as `--strict`) |
| `SX_SSH_KEY` | SSH key path (or inline key content) for git operations (same as `--ssh-key`; legacy alias `SKILLS_SSH_KEY`) |
| `SX_SYNC_SILENT` | `true` silences background sync output (legacy alias `SKILLS_SYNC_SILENT`) |
| `SX_CLOUD_URL` | Overrides the cloud relay URL for `sx cloud` |
| `SX_CLI_PATH` | Overrides which sx binary gets written into client hook/MCP configs |

## What is *not* an sx environment variable

`SX_CONFIG` is **not** read by sx on any platform. A variable of that
name previously appeared inside the generated vault `install.sh` as a
script-local file path (it has since been renamed `SX_CONFIG_FILE`
there). Setting `SX_CONFIG` in the environment has no effect — use
`SX_CONFIG_DIR` (a directory, not a file path) to relocate config.
