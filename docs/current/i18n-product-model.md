# Product language model

[English](i18n-product-model.md) | [简体中文](i18n-product-model.zh-CN.md)

Status: target specification for the product-wide migration. Management cards
already support both languages; remaining surfaces are being migrated. This
document must not be read as a claim of complete shipped coverage.

## Scope

Product-owned interface text supports English (`en`) and Simplified Chinese
(`zh-CN`): Feishu cards and notifications, command-line help and human-readable
operation feedback, terminal UI labels, and desktop windows, menus, dialogs,
notifications and accessibility labels. A translated success screen with an
untranslated error or permission prompt is incomplete coverage.

Agent replies, user content, names, paths, IDs, executable command names, flag
names, protocol fields, JSON output and raw third-party diagnostics retain their
original values. Product text around those values is translated. Translation
never searches or replaces substrings in rendered user content or JSON output.

## Language selection

- Feishu retains its per-bot, per-user preference. Private controls accept auto,
  English and Simplified Chinese. Shared cards use the viewer's native language;
  one person's setting cannot change another viewer's display. Product-owned
  plain-text fallbacks explicitly resolve the intended recipient's preference.
- CLI language is invocation-scoped: an explicit language option overrides
  `AMUX_LANGUAGE`, then the terminal locale (`LC_ALL`, `LC_MESSAGES`, `LANG`).
  English is the fallback. Structured output, completion tokens and exit codes
  do not depend on language. Changing language must not require restarting a Hub
  or Runtime or modifying an Agent's environment.
- Desktop language is a local user preference with auto, English and Simplified
  Chinese choices. Auto follows the client language. Changes update the renderer
  and native menus immediately and survive relaunch.

## Catalog and rendering contract

Use explicit product-copy entries and the common language normalization and
fallback rules. Keep catalogs near their owning surface; unrelated surfaces do
not need to import one another's UI implementation. Every message has both
languages and matching interpolation arguments. Missing entries and accidental
hardcoded product prose fail coverage checks.

Resolve language at the presentation boundary. Preserve errors' diagnostic
causes and machine-readable values. Never mutate process-global locale in the
multi-user Hub or Runtime. Formatting and truncation operate on localized
product text without translating literal arguments.

## Acceptance

The conversation-card migration includes status and timing, usage labels,
question/follow-up headings, plans, execution records, subtask summaries,
processing and final-result headings, viewer controls, and size-limit fallbacks.
Native per-viewer translations must survive CardKit creation, insertion,
replacement, streaming updates and legacy delivery. Keep the translated final
heading separate from the streamed Agent body so content-only updates cannot
overwrite translations or translate Agent output. Existing delivered cards are
not rewritten retroactively. This is a target until implementation is verified.

Verify both languages, unsupported-language fallback, persistence and isolation,
all product-owned success/error/empty/loading states, and unchanged payloads.
Feishu verification includes native card schema restrictions, not just JSON
decoding; a form-only field is forbidden on a standalone callback control.
CLI verification compares JSON, command behavior and exit codes across locales.
Desktop verification includes locale changes without reload and native menus.
