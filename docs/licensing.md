# License and Attribution

[English](licensing.md) | [简体中文](licensing.zh-CN.md)

Unless a file is marked otherwise, amux's original source code, documentation,
and examples are licensed under the unmodified [Apache License, Version 2.0](../LICENSE)
(`Apache-2.0`). The project attribution is in [NOTICE](../NOTICE). Existing
copyright notices remain with their respective owners. The project's copyright
notice names the amuxos organization; the license does not transfer ownership.

## Commercial use and integration

Apache-2.0 permits commercial use, modification, distribution, and integration
into proprietary products. An independent distribution may license its own code
separately while fulfilling its obligations for amux and other dependencies.
Apache-2.0 includes a contributor patent grant subject to its terms; it does not
grant general rights to use the project's trademarks or imply endorsement.

When redistributing amux or a derivative work, comply with section 4 of LICENSE:

- Give recipients a copy of the license.
- Put prominent notices on files you modify, stating that you changed them.
- Retain applicable copyright, patent, trademark, and attribution notices in
  distributed derivative source.
- Preserve applicable attribution from NOTICE in a readable form. For distributed
  derivative works, section 4(d) permits a NOTICE file, accompanying source or
  documentation, or a display where third-party notices normally appear.

An integration can use the following entry alongside the full license and the
applicable NOTICE text:

> This product includes amux (https://github.com/amuxos/amux), licensed under the
> Apache License, Version 2.0.

A third-party notices document or an appropriate About component list is
sufficient as the attribution location; there is no extra requirement for a
prominent logo, a homepage badge, or an advertising credit. Running a hosted
service alone does not trigger Apache-2.0's redistribution obligations. Merely
calling a separately operated amux service does not by itself redistribute amux.

NOTICE is informational and adds no license conditions. This guide summarizes
the license rather than modifying it; the original English LICENSE is the
authoritative license text. The Chinese guide explains the same rules.

## Releases and third-party components

`scripts/package-release.sh` includes LICENSE, NOTICE, both third-party notice
guides, go.mod, and the Go dependency notice collection under `licenses/amux/` in every
release archive. The same location is used when a dependent distribution sets
`AMUX_PACKAGE_BUILD_ROOT`; the license material there covers the public core and
its listed dependencies, not the distribution's own code or added dependencies.
Distributors must supply the additional notices their actual artifacts require.

The public release packager contains Go binaries and these notices. It does not
bundle the optional Claude SDK sidecar or separately installed agents. Their
terms remain separate from amux's Apache-2.0 grant. See
[Third-party notices](../THIRD_PARTY_NOTICES.md), including the MPL source links
and Anthropic SDK terms, before assembling a distribution with extra components.

Contribution licensing is described in [CONTRIBUTING.md](../CONTRIBUTING.md).
