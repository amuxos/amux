# Third-Party Notices

[English](THIRD_PARTY_NOTICES.md) | [简体中文](THIRD_PARTY_NOTICES.zh-CN.md)

amux's [Apache-2.0 license](LICENSE) applies to its original material. Dependencies,
third-party material, separately installed agents, and external services retain
their own licenses and terms. Attribution to amux does not replace their notices.

## Go dependencies

[third_party/go-notices.txt](third_party/go-notices.txt) preserves the upstream
license, copyright, attribution, and patent notice files for the versions pinned
in [go.mod](go.mod), together with the Go runtime and standard library license.
It includes all required modules, so an individual binary may contain a subset.
Each module entry links to its exact source archive. The collection includes
additional notices found in production subdirectories; dependency test fixtures,
fuzz harnesses, and vendored trees are excluded from this inventory.

The collection is generated from the downloaded upstream modules by
`python3 scripts/update-go-notices.py` and checked by `make check`. The generator
does not infer license compatibility or replace review of copied source notices.
Review new or changed dependencies and preserve any additional notices applicable
to the files actually redistributed. Regenerate the collection when go.mod changes.

The following pinned modules use MPL-2.0:

| Module | Version | Corresponding source |
| --- | --- | --- |
| `github.com/hashicorp/hcl` | `v1.0.0` | [Source archive](https://proxy.golang.org/github.com/hashicorp/hcl/@v/v1.0.0.zip) |
| `github.com/hashicorp/golang-lru/v2` | `v2.0.7` | [Source archive](https://proxy.golang.org/github.com/hashicorp/golang-lru/v2/@v/v2.0.7.zip) |

These modules are used from their unmodified upstream releases. Their license
texts are included in the collection. If you distribute an executable containing
MPL-covered code, make its corresponding source available under MPL-2.0 and tell
recipients how to obtain it, as required by section 3.2. If you modify covered
files, provide the corresponding modified source; an upstream link alone does
not provide your changes. This obligation does not relicense amux's own files.

## Optional Claude sidecar

The amux-owned adapter source in `sidecars/claude-sdk/` is Apache-2.0. Its dependency
`@anthropic-ai/claude-agent-sdk`, pinned in the lockfile to `0.3.296`, and the eight
platform packages at that version use Anthropic's own terms. Their license text
states “All rights reserved” and refers to the
[Claude legal and compliance documentation](https://code.claude.com/docs/en/legal-and-compliance);
the SDK README also refers to [Anthropic's Commercial Terms](https://www.anthropic.com/legal/commercial-terms).

The pinned [SDK license](https://unpkg.com/@anthropic-ai/claude-agent-sdk@0.3.296/LICENSE.md)
and [SDK README](https://unpkg.com/@anthropic-ai/claude-agent-sdk@0.3.296/README.md)
are available upstream. npm availability is not a redistribution grant, and amux's
license does not grant rights to Anthropic's software. Review the applicable terms
and your permissions before bundling it into a distribution.

`make sidecar` installs packages from the lockfile for a local build. Its other
npm dependencies retain the licenses and notices shipped in their packages.
The public Go release archive does not include this sidecar or `node_modules`.
If you distribute a sidecar, include the applicable npm dependency notices and
license texts for the actual production bundle, including native platform
packages; the Go notice collection does not cover them.

## Other agents and distributions

Codex, Claude, Pi, and their services are installed or accessed separately and
remain subject to their own terms. A downstream distribution must account for
its added dependencies, assets, and extensions. Files under `licenses/amux/` in
release archives cover the public core and its listed Go dependencies only.
