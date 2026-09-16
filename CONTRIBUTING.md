# Contributing

[English](CONTRIBUTING.md) | [简体中文](CONTRIBUTING.zh-CN.md)

Submit public-core changes to <https://github.com/amuxos/amux>. Read
[AGENTS.md](AGENTS.md) for engineering and validation rules. Maintain English
and Simplified Chinese documentation together.

## Contribution license

Under section 5 of [Apache-2.0](LICENSE), unless explicitly stated otherwise,
contributions intentionally submitted for inclusion in amux are submitted under
Apache-2.0. You must have the right to contribute the material under those terms,
including any necessary authorization from an employer or other rights holder.
Contributing does not transfer copyright ownership.

Preserve existing author, license, and attribution notices. Identify third-party
material and its original license in your change; do not label another author's
code as amux-owned or silently relicense it. See [Third-party notices](THIRD_PARTY_NOTICES.md).

When Go dependencies change, run `python3 scripts/update-go-notices.py` and review
the resulting notice collection with the dependency update. `--check` checks it
without rewriting the file. The full gate uses this check and therefore requires
Python 3 in addition to the tools documented in AGENTS.md. Changing the Claude
sidecar's dependencies also requires reviewing its lockfile and applicable terms.

Licensing and integration examples are in the [licensing guide](docs/licensing.md).
