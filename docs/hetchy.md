# sx + hetchy

[hetchy](https://github.com/sleuth-io/hetchy) runs coding agents in isolated
sandboxes and opens reviewable pull requests. `sx` is where those agents get
their skills.

The split is clean: `sx` owns authoring, versioning, and distribution of AI
assets. hetchy owns execution — sandboxes, credentials, streaming, pull requests,
validation. Neither needs to know how the other does its half.

## Why they pair

An agent running unattended against a real repository needs to know your
conventions: how you write migrations, what your review standards are, which
commands to run before claiming done. That knowledge is exactly what an `sx`
skill is.

Without a vault, you would bake those instructions into the runner's image and
redeploy to change them. With one, you publish a skill and the next run has it.

## The loop

**1. Author and publish a skill**

```bash
sx add ~/.claude/skills/migration-reviewer
```

Or drag it into the app. Either way it lands in your vault as a versioned asset.

**2. Point hetchy at that vault**

hetchy draws on two independent sources, not one fallback chain.

A **public vault**, set process-wide for the deployment:

| `HETCHY_SX_PUBLIC_VAULT_URL` | Result |
|---|---|
| empty (default) | hetchy's own public vault |
| a clone URL | your fork, or any public Git vault |
| `disabled` (also `off`, `none`, `-`) | skip the public install |

And a **per-organization vault** for your team's own assets, resolved in this
order:

1. A Skills.new SX key, added by an org admin under **Organization settings →
   Integrations → SX skills vault**. Stored per organization and encrypted at
   rest, so each org in a deployment reads from its own vault.
2. Otherwise, a configured GitHub Git vault repository.

The per-org Skills.new key is the one that matters for a team — it is what makes
"publish a skill, next run has it" true for your private assets rather than just
the public set.

**3. Run an agent**

Send a request from hetchy's web UI, Slack, Linear, a GitHub mention, or a
scheduled job. Before the agent starts, hetchy installs the personas and scoped
skills from the vault into the sandbox. The agent then works with your
conventions already in context.

**4. Iterate on the skill, not the deployment**

Publish a new version with `sx` and the next hetchy run picks it up. No image
rebuild, no restart.

## Scoping

`sx` scoping does real work here. Skills scoped to an org, repo, or path only
install where they apply, which keeps sandbox context from bloating with skills
that are irrelevant to the repository being worked on. See
[scoping.md](scoping.md).

## Under the hood

hetchy imports [`pkg/sxvault`](library.md) directly rather than shelling out to
the `sx` binary, so vault reads happen in-process with the rest of the run. It
also maintains a server-side Git cache for vault clones — see hetchy's
[SX setup guide](https://github.com/sleuth-io/hetchy/blob/main/docs/sx-setup.md)
for the cache and timeout settings.

## Getting started

- Install `sx`: see the [README quickstart](../README.md#quickstart)
- Run hetchy: `cp .env.example .env && docker compose up --build`, per its
  [README](https://github.com/sleuth-io/hetchy#quickstart)
