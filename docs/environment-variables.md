# Environment Variables

sx honors a small set of environment variables that relocate its state.
They are the supported way to isolate sx — in CI, Docker, test
harnesses, demo recordings, or any sandbox — without overriding `$HOME`.
All of them work on every platform.

## Supported variables

### `SX_CONFIG_DIR`

Overrides the **config directory** — where sx reads and writes
`config.json` (vault URL, profile, auth). This is the config-isolation
boundary: point it at an empty directory and sx starts unconfigured,
leaving real user state untouched.

When unset, sx uses the platform default:

| Platform | Default |
|----------|---------|
| Linux    | `$XDG_CONFIG_HOME/sx` (falls back to `~/.config/sx`) |
| macOS    | `~/Library/Application Support/sx` |
| Windows  | `%AppData%\sx` |

### `SKILLS_CONFIG_DIR`

Legacy alias for `SX_CONFIG_DIR`, checked only when `SX_CONFIG_DIR` is
unset. Prefer `SX_CONFIG_DIR` in new setups.

### `SX_CACHE_DIR`

Overrides the **cache directory** — downloaded assets, cloned git
repositories, ETags, and lock files. When unset, sx uses the platform
cache default (e.g. `~/Library/Caches/sx` on macOS, `$XDG_CACHE_HOME/sx`
or `~/.cache/sx` on Linux).

## Isolating sx completely

Set both variables to sandbox directories:

```bash
export SX_CONFIG_DIR="$SANDBOX/config"
export SX_CACHE_DIR="$SANDBOX/cache"

sx init --type git --repo-url "$VAULT_URL"
sx install
sx config   # verify: reported config path is under $SX_CONFIG_DIR
```

Always verify isolation with `sx config` — it prints the effective
config path.

## What is *not* an sx environment variable

`SX_CONFIG` is **not** read by sx on any platform. A variable of that
name previously appeared inside the generated vault `install.sh` as a
script-local file path (it has since been renamed `SX_CONFIG_FILE`
there). Setting `SX_CONFIG` in the environment has no effect — use
`SX_CONFIG_DIR` (a directory, not a file path) to relocate config.
