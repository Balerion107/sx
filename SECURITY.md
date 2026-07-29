# Security Policy

## Reporting A Vulnerability

Please report suspected vulnerabilities privately through GitHub's private
vulnerability reporting:

**https://github.com/sleuth-io/sx/security/advisories/new**

That channel is private to the maintainers and lets us coordinate a fix and a
release before anything is public. Please do not open a public issue for a
security problem.

Include what you can:

- Affected command, subsystem, or workflow.
- Steps to reproduce.
- Impact and the access an attacker would need.
- Any logs or proof-of-concept details that are safe to share.

We will acknowledge reports as quickly as practical and coordinate remediation
before public disclosure.

## Supported Versions

The `main` branch receives security fixes, which ship in the next tagged
release. Older tags are not patched in place — upgrade to the latest release.

## Verifying What You Install

`install.sh` downloads the release archive and verifies it against the
`checksums.txt` published with that release, refusing to install on a mismatch.

Releases from v2.2.9 onward also carry a signed build provenance attestation.
To confirm an archive was built by this repository's release workflow:

```bash
gh attestation verify sx_Darwin_arm64.tar.gz --repo sleuth-io/sx
```

Homebrew installs are verified by Homebrew against the formula's own checksum.

## Handling Secrets

`sx` stores a vault auth token in `~/.config/sx/config.json`. Never paste real
tokens, credentials, or private keys into issues, pull requests, or discussions
— redact them first. If you believe a token has been exposed, rotate it before
reporting.
