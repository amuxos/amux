# Public binary releases

[English](public-releases.md) | [简体中文](public-releases.zh-CN.md)

The application source repository can remain private. Public artifacts live in
`amuxos/amux-releases`; downloads need no GitHub login. That repository contains
documentation and release assets, not application source history.

## Maintainer setup

Enable GitHub Actions in the source repository. Create a fine-grained token
owned by the publishing account with access to only `amuxos/amux-releases`,
repository Contents read/write, and an explicit expiration. Store it as the
source repository Actions secret `AMUX_RELEASE_TOKEN`. Renew that secret before
the credential expires. Do not reuse a developer's broadly scoped CLI token.

The workflow uses read-only source access. Only the publish step receives the
cross-repository secret. The release repository's own workflows are not needed.

## Publish

After reviewing and merging a source change, run the complete gate with the CI
toolchain, then create a stable `vX.Y.Z` tag on the checked main commit and push
it. `.github/workflows/release.yml` checks that the tag belongs to main, reruns
the gate, builds five platform packages (macOS/Linux amd64+arm64 native, Windows amd64 cross-compiled with Setup.exe), and verifies the complete set.
It uploads all assets to a draft release before publishing it as latest.

The public tag is created against the release repository's documentation branch.
It never points to or imports the application's private Git history. Release
notes are purpose-written; private commit messages are not copied into them.

The workflow can also be dispatched manually with an existing stable tag.
Failed drafts can be retried. An already published version is immutable; publish
a new version for corrections. Concurrency serializes releases.

## Download contract

Each version has `install.sh`, `version.txt`, `latest.json`, platform tarballs for
macOS/Linux, Windows `*_Setup.exe` plus its tarball, and their `.sha256` files. The installer resolves `version.txt` once and downloads
the pinned platform filename and checksum. `amux update --restart` and update
checks use `latest.json`; on that manifest path the updater verifies SHA256
before extraction (a custom `--release-url`/`AMUX_RELEASE_URL` pointing directly
at a `.tar.gz` archive skips checksum verification).
Both paths reject unsupported archive entries. Packages include license notices
and a platform-matched Claude SDK sidecar. Install and update also download the
managed Agent Pack Contract SCM release from
`amuxos/agent-pack-contract-releases` into `~/.amux/libexec/agent-pack-contract`
when `AGENT_PACK_CONTRACT_SCM_URL` is unset. Native agents and Node remain external
prerequisites. Linux packages also include the matching
`amux-isolation-helper`; packaging fails if it is missing. Its initial privileged
installation remains explicit, while later updates refresh an existing service.

For a mirror, `AMUX_RELEASE_BASE_URL` selects the installer's GitHub-style
releases directory; configure `AMUX_RELEASE_URL` for subsequent CLI and service
update checks (`AMUX_SCM_LATEST_URL` remains accepted as a legacy fallback).
Environment overrides are not automatically persisted into native
service environments. Local binary and source installation remain available.

Version rollback is not database rollback. File activation failures restore the
previous binaries and sidecar; errors after activation require diagnosis before
attempting an older version against current user data.
