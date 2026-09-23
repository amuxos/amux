# Maintain and regenerate documentation

[English](documentation-system.md) | [简体中文](documentation-system.zh-CN.md)

The documentation starts with Feishu workflows, then explains the objects behind
them. This page defines the implemented publishing structure and update process.

## Information architecture

| Layer | Reader's question | Source |
| --- | --- | --- |
| Start in Feishu | How do I get a working assistant? | `docs/tutorials/` |
| Work in Feishu | How do I converse, collaborate, delegate or automate? | `docs/guides/` |
| Concepts | What are an expert, Session, Task and instance? | `docs/concepts/` |
| Reference | Which commands, fields and options exist? | `docs/reference/` |
| Operations and integration | How do I install, diagnose and connect machines/tools? | Existing operating guides and selected CLI guides |
| Models and maintenance | Which behavior is authoritative and how is it maintained? | `docs/current/`, Task model, maintainer design records and project docs |

The explicit bilingual catalog `docs/navigation.json` owns page membership,
ordering, short navigation titles and status. `make docs-index` generates both
repository indexes from it; the website synchronizer consumes the same catalog.
Pages use stable source paths and published routes rather than moving with a
sidebar label. When consolidating or removing a page, update the catalog and
its incoming links together. Reports and planning notes remain
repository background, excluded from the published current-document set.

The catalog labels target specifications and historical records. These are visible
in indexes and website status notices; a target specification is not a shipped
feature claim. Product models remain detailed maintainer contracts and are not
the first tutorial for users.

Brief architectural principles belong in the relevant current model or guide.
Design records with distinct rationale, alternatives and tradeoffs live under
`docs/maintainers/`, with their historical status made explicit; they do not need
a separate architecture-decisions directory. Deleted records remain in Git history.

## What is generated, and from what

| Output | Actual input | Command |
| --- | --- | --- |
| Feishu command help, EN/ZH | `internal/bridge/command` + Bridge locale catalogs | `make feishu-doc` |
| CLI reference, EN/ZH | Assembled Cobra tree + documentation translations | `make cli-doc` |
| Configuration reference, EN/ZH | Configuration metadata + documentation translations | `make config-doc` |
| MCP reference, EN/ZH | Registered tool schemas + documentation translations | `make mcp-doc` |
| Repository indexes, EN/ZH | `docs/navigation.json` | `make docs-index` |
| Website pages, sidebars and edit links | Public Markdown pairs + the catalog | Website `sync-docs.py`, then VitePress |

Tutorials, guides, concepts and product contracts are authored against code and
behavior tests; they cannot be regenerated faithfully by dumping API comments.
Regeneration updates mechanical facts and navigation. Human review checks meaning,
readability, bilingual equivalence and the end-to-end reader journey.

## Update workflow

1. Identify the actual reader workflow and relevant implementation. Update the
   owning product model if behavior changes, then implementation and behavior tests.
2. Update both languages of the affected guide. A procedure needs prerequisites,
   steps, expected outcome, failure path and a useful next link. Mark natural
   language requests as Agent-dependent and show how to verify saved objects.
3. Register new pages in `docs/navigation.json` with both titles and an explicit
   status when they are generated, historical or a target specification.
4. Regenerate and verify from the public repository root:

```sh
make docs
make docs-check
```

The check covers reference drift, missing page pairs/language links, uncatalogued
published pages, broken local file links, and direct `amux` command names/flags
in tutorial and guide shell blocks. It never runs those examples against a live
installation. Command checks do not prove argument values, permissions or runtime
outcomes; file-link checks do not validate URL fragments or external sites.

Script/code changes also require the complete `scripts/check.sh` gate. Documentation
checks run within it. Keep executable prompts and Skills outside page-pair checks.

## Preview and publish

In the separate website checkout, use an explicit public checkout to avoid
accidentally syncing a stale source snapshot:

```sh
python3 sync-docs.py /path/to/amux
npm run docs:build
npm run docs:dev
```

Review both languages: Feishu entry → conversation guide → concept definitions →
command reference → troubleshooting. Check search and old deep links. The website
preserves `docs/upstream/index.md` and `zh/index.md` as authored landing pages;
other synced pages and navigation are generated and must not be edited there.

For rollout, merge the public documentation first: the existing website sync
can already discover the new pages. Then merge the website catalog consumer and
landing-page changes. This ensures their destination pages exist before new
navigation is published. The consumer also accepts older sources without a catalog.
Public-main docs changes trigger `sync-docs.yml`: sync, build with dead-link checks,
then commit changed site sources. The website builds on push; production deployment
remains a separate manual workflow. A successful sync is not a production release.
