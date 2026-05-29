# Workspace Telegram Notifications — Design Spec

**Date:** 2026-05-29  
**Status:** Approved (brainstorming)  
**Scope:** Web + desktop settings UI, Go notification pipeline, CLI field addition

## Problem

Workspace Telegram notifications today:

1. **Settings in the wrong place** — bot token and chat ID are edited under **My Account → Notifications**, but stored in `workspace.settings.telegram` (workspace-scoped data).
2. **Duplicate messages** — Telegram hooks `inbox:new`, so one comment with `@all` creates N inbox rows → N identical Telegram messages to the same chat.
3. **Poor formatting** — plain text, raw UUIDs, no deep links, generic “Inbox notification” header.
4. **Reactions** — `reaction_added` inbox items are unclear in Telegram; no workspace-level opt-out for the shared channel.
5. **Status transitions** — already sent once per event (direct call on `issue:updated`), but still use plain text.

## Goals

| Goal | Success criteria |
|------|------------------|
| Workspace-scoped settings UI | Admins configure Telegram under **Workspace → Integrations** |
| One Telegram per activity | `@all` comment → exactly **one** Telegram message |
| Clickable issues | HTML link to `{FRONTEND_ORIGIN}/{slug}/issues/{identifier\|uuid}` |
| Clear reactions | Dedicated template; workspace toggle `notify_reactions` (default on) |
| Richer messages | Telegram HTML (`parse_mode: HTML`) |

## Non-goals (v1)

- Per-workspace `app_url` override (use server `FRONTEND_ORIGIN` only)
- Mobile Integrations UI
- Telegram for assignments, priority/due-date changes, `task_failed`, or mention-only inbox events (inbox-only; extend later if needed)
- Comment deep-links in Telegram (`?comment=`)

## Decisions (brainstorming)

| Topic | Decision |
|-------|----------|
| Deduping model | **One message per source event** (not per inbox row) |
| Settings location | **Integrations** tab (workspace section) |
| Reaction opt-out | **Workspace** toggle on Telegram integration only |
| Issue links | **`FRONTEND_ORIGIN`** env on server |
| Message format | **Telegram HTML** |

---

## 1. Settings & data model

### UI

- **Add** Telegram card to `packages/views/settings/components/integrations-tab.tsx`:
  - Bot token (password input)
  - Chat / user ID
  - Toggle: **Send reaction notifications** (default on)
  - Save → `api.updateWorkspace` with merged `settings.telegram`
  - Admin/owner only (same rule as today)
- **Remove** Telegram section from `packages/views/settings/components/notifications-tab.tsx`
- **Update** tests: `notifications-tab.test.tsx` → move Telegram tests to `integrations-tab.test.tsx` (new)
- **i18n:** move/extend keys under `settings.integrations.telegram.*` in `en` + `zh-Hans`

### Storage

Extend `workspace.settings.telegram`:

```typescript
interface WorkspaceTelegramSettings {
  bot_token: string;
  user_id: string;
  notify_reactions?: boolean; // default true when omitted
}
```

Go struct mirrors JSON; `notify_reactions: false` skips reaction Telegram sends.

### CLI

- `multica workspace telegram` — add `--notify-reactions` / document JSON field (no breaking change)

---

## 2. Backend architecture

### Remove

- `bus.Subscribe(protocol.EventInboxNew, … sendWorkspaceTelegramMessage …)` in `notification_listeners.go`

### New module

`server/cmd/server/telegram_notify.go` (name flexible):

| Responsibility | Notes |
|----------------|-------|
| `frontendOrigin()` | `strings.TrimSpace(os.Getenv("FRONTEND_ORIGIN"))`; warn once if empty |
| `issueDeepLink(origin, slug, identifier, issueID)` | Prefer identifier |
| `htmlEscape(s)` | `&`, `<`, `>` |
| `stripMentionsForPreview(content, queries, wsID)` | Replace `mention://member/…` with display names where cheap |
| `sendWorkspaceTelegramHTML(ctx, queries, workspaceID, html)` | `parse_mode: "HTML"`; `disable_web_page_preview: false` |
| Formatters | `formatTelegramComment`, `formatTelegramStatus`, `formatTelegramReaction` |

Refactor existing `telegramSendMessage` to accept optional `parseMode` or add `telegramSendHTML`.

### Event wiring (one send each)

| Source | Action |
|--------|--------|
| `comment:created` | After non-system comment handling, load issue (identifier, title, slug), actor name → send comment template |
| `issue:updated` + `status_changed` | Replace `formatTelegramStatusTransitionMessage` plain text with HTML template (keep single send here) |
| `issue_reaction:added` | If `notify_reactions` → reaction template (issue-level) |
| `reaction:added` | If `notify_reactions` → reaction template (comment-level copy) |

Do **not** send Telegram for: `issue:created` mentions, assignee/priority/date subscriber notifications, `task_failed`, agent events.

### Actor resolution

Load human-readable name from `actor_type` + `actor_id` (member user, agent name) with fallback to “Someone”.

---

## 3. Message templates (Telegram HTML)

All dynamic strings escaped. Identifier in link text when available.

### New comment

```
💬 <b>{workspace}</b>
<a href="{url}">{identifier}</a> · {title}

<b>{actor}</b> commented:
{preview}
```

Preview: ≤300 chars, newlines preserved (or collapsed to space), mentions simplified.

### Status transition

```
✅ <b>{workspace}</b>
<a href="{url}">{identifier}</a> · {title}

{fromStatus} → {toStatus}
```

Use existing `statusLabel()` mapping.

### Reaction

```
👍 <b>{workspace}</b>
<b>{actor}</b> reacted {emoji} on <a href="{url}">{identifier}</a>
<i>{title}</i>
```

Comment reaction: append “on a comment” in plain text (no comment URL v1).

### No `FRONTEND_ORIGIN`

Same layout; issue line is `{identifier} · {title}` without `<a>`.

---

## 4. Testing & rollout

### Unit tests (`telegram_notify_test.go`)

- HTML escape edge cases (`<script>`, `&`)
- Deep link with identifier vs UUID fallback
- `notify_reactions: false` → reaction handlers do not call send
- Comment listener: N inbox publishes, **one** `telegramSendMessage` call (mock)

### Integration / manual

1. Configure Telegram on Integrations (workspace admin).
2. Post comment with `@all` → **one** Telegram, linked identifier.
3. Change status → one transition message (HTML).
4. Add reaction with toggle off → no Telegram; inbox unchanged.
5. Unset `FRONTEND_ORIGIN` → messages still send without links.

### Rollout

- No migration; JSON field optional.
- Self-hosted: ensure `FRONTEND_ORIGIN` set for links (document in SELF_HOSTING if needed).

### Risks

| Risk | Mitigation |
|------|------------|
| HTML injection from issue titles/comments | Strict `htmlEscape` on all user content |
| Telegram API rejects malformed HTML | Unit tests; fallback to plain text send on 400 (optional v1.1) |
| Missing origin | Degrade gracefully, log warning |

---

## 5. Files touched (summary)

| Area | Files |
|------|-------|
| UI | `integrations-tab.tsx`, `notifications-tab.tsx`, locales, tests |
| Types | `packages/core/types/workspace.ts` |
| Server | `telegram_notify.go`, `notification_listeners.go`, tests |
| CLI | `cmd_workspace.go` (flags/help) |
