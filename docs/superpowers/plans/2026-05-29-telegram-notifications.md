# Workspace Telegram Notifications — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move Telegram config to Workspace → Integrations, send one HTML Telegram message per comment/status/reaction (fix @all spam), add issue deep links and reaction opt-out.

**Architecture:** Remove `inbox:new` → Telegram hook. New `telegram_notify.go` formats HTML and sends from source events (`comment:created`, `issue:updated` status, reaction events). UI reads/writes `workspace.settings.telegram` including `notify_reactions`.

**Tech Stack:** Go (`server/cmd/server`), React (`packages/views`), Vitest, existing `events.Bus`, Telegram Bot API `sendMessage` with `parse_mode: HTML`.

**Spec:** `docs/superpowers/specs/2026-05-29-telegram-notifications-design.md`

---

## File map

| File | Responsibility |
|------|----------------|
| `server/cmd/server/telegram_notify.go` | HTML format, origin, send, mention preview |
| `server/cmd/server/telegram_notify_test.go` | Unit tests (no DB) |
| `server/cmd/server/notification_listeners.go` | Remove inbox hook; call telegram from events |
| `packages/core/types/workspace.ts` | `notify_reactions?` on `WorkspaceTelegramSettings` |
| `packages/views/settings/components/integrations-tab.tsx` | Telegram card + save |
| `packages/views/settings/components/integrations-tab.test.tsx` | UI save test |
| `packages/views/settings/components/notifications-tab.tsx` | Remove Telegram section |
| `packages/views/settings/components/notifications-tab.test.tsx` | Remove Telegram test |
| `packages/views/locales/en/settings.json` | Move telegram strings under `integrations.telegram` |
| `packages/views/locales/zh-Hans/settings.json` | Same |
| `server/cmd/multica/cmd_workspace.go` | `--notify-reactions` flag |

---

### Task 1: Extend workspace Telegram types

**Files:**
- Modify: `packages/core/types/workspace.ts`

- [ ] **Step 1: Add optional field**

```typescript
export interface WorkspaceTelegramSettings {
  bot_token: string;
  user_id: string;
  /** When false, skip Telegram for reactions. Default: send. */
  notify_reactions?: boolean;
}
```

- [ ] **Step 2: Typecheck**

Run: `pnpm typecheck --filter @multica/core`  
Expected: PASS

---

### Task 2: Telegram notify helpers (TDD)

**Files:**
- Create: `server/cmd/server/telegram_notify.go`
- Create: `server/cmd/server/telegram_notify_test.go`
- Modify: `server/cmd/server/notification_listeners.go` (move shared send/config here later in Task 4)

- [ ] **Step 1: Write failing tests**

Create `telegram_notify_test.go`:

```go
package main

import "testing"

func TestHTMLEscape(t *testing.T) {
	if got := htmlEscape(`a & b <c>`); got != `a &amp; b &lt;c&gt;` {
		t.Fatalf("got %q", got)
	}
}

func TestIssueDeepLink(t *testing.T) {
	got := issueDeepLink("https://app.test", "acme", "MUL-1", "uuid-fallback")
	want := "https://app.test/acme/issues/MUL-1"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	got = issueDeepLink("https://app.test", "acme", "", "uuid-fallback")
	if got != "https://app.test/acme/issues/uuid-fallback" {
		t.Fatalf("fallback: got %q", got)
	}
}

func TestIssueDeepLinkNoOrigin(t *testing.T) {
	if got := issueDeepLink("", "acme", "MUL-1", "uuid"); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func TestTelegramNotifyReactionsEnabled(t *testing.T) {
	cfg := &telegramSettings{BotToken: "t", UserID: "u", NotifyReactions: boolPtr(false)}
	if telegramReactionsEnabled(cfg) {
		t.Fatal("expected disabled")
	}
	if !telegramReactionsEnabled(&telegramSettings{BotToken: "t", UserID: "u"}) {
		t.Fatal("default should enable reactions")
	}
}

func boolPtr(b bool) *bool { return &b }
```

- [ ] **Step 2: Run tests — expect FAIL**

Run: `cd server && go test ./cmd/server/ -run 'TestHTML|TestIssueDeep|TestTelegramNotify' -count=1`  
Expected: FAIL (undefined symbols)

- [ ] **Step 3: Implement minimal helpers in `telegram_notify.go`**

Move from `notification_listeners.go`:
- `telegramSettings` struct — add `NotifyReactions *bool \`json:"notify_reactions,omitempty"\``
- `workspaceNotificationSettings`, `workspaceTelegramConfig`, `telegramSendMessage` (extend payload with `parse_mode` when non-empty)
- Add: `htmlEscape`, `issueDeepLink`, `frontendOrigin`, `telegramReactionsEnabled`, `sendWorkspaceTelegramHTML(ctx, queries, workspaceID, html string)`

```go
func sendWorkspaceTelegramHTML(ctx context.Context, queries *db.Queries, workspaceID, html string) {
	cfg := workspaceTelegramConfig(ctx, queries, workspaceID)
	if cfg == nil {
		return
	}
	if err := telegramSendMessage(ctx, cfg.BotToken, cfg.UserID, html, "HTML"); err != nil {
		slog.Error("failed to send telegram notification", "workspace_id", workspaceID, "error", err)
	}
}

func telegramReactionsEnabled(cfg *telegramSettings) bool {
	if cfg == nil || cfg.NotifyReactions == nil {
		return true
	}
	return *cfg.NotifyReactions
}
```

Update `telegramSendMessage` signature to `func(ctx, botToken, userID, text, parseMode string) error` and include `parse_mode` in JSON only when `parseMode != ""`.

- [ ] **Step 4: Run tests — expect PASS**

Run: `cd server && go test ./cmd/server/ -run 'TestHTML|TestIssueDeep|TestTelegramNotify' -count=1`  
Expected: PASS

---

### Task 3: HTML message formatters (TDD)

**Files:**
- Modify: `server/cmd/server/telegram_notify.go`
- Modify: `server/cmd/server/telegram_notify_test.go`

- [ ] **Step 1: Add failing formatter tests**

```go
func TestFormatTelegramCommentHTML(t *testing.T) {
	html := formatTelegramCommentHTML(formatTelegramInput{
		WorkspaceName: "R&D Team",
		Origin:        "https://app.test",
		Slug:          "rd",
		Identifier:    "RD-65",
		IssueID:       "uuid",
		Title:         "Allow IP <test>",
		ActorName:     "Binh",
		Preview:       "hello",
	})
	if !strings.Contains(html, "RD-65") || !strings.Contains(html, `href="https://app.test/rd/issues/RD-65"`) {
		t.Fatalf("missing link: %s", html)
	}
	if strings.Contains(html, "<test>") {
		t.Fatal("title must be escaped")
	}
}
```

Define `formatTelegramInput` struct shared by formatters.

- [ ] **Step 2: Implement formatters**

- `formatTelegramCommentHTML(in formatTelegramInput) string`
- `formatTelegramStatusHTML(in formatTelegramInput, fromStatus, toStatus string) string` — use existing `statusLabel` from `notification_listeners.go` (same package)
- `formatTelegramReactionHTML(in formatTelegramInput, emoji string, onComment bool) string`

Issue line helper:
- If `origin != ""` && identifier or issueID: `<a href="...">...</a> · {escaped title}`
- Else: `{identifier} · {escaped title}`

- [ ] **Step 3: Add `stripMentionsForPreview`**

Regex or reuse `util.ParseMentions`: replace `[@Label](mention://type/id)` with `@Label` for Telegram preview (strip URL parens). Cap preview at 300 runes.

- [ ] **Step 4: Run formatter tests**

Run: `cd server && go test ./cmd/server/ -run TestFormatTelegram -count=1`  
Expected: PASS

---

### Task 4: Rewire notification listeners

**Files:**
- Modify: `server/cmd/server/notification_listeners.go`

- [ ] **Step 1: Delete `EventInboxNew` Telegram subscriber** (lines ~681–691)

- [ ] **Step 2: Delete old plain formatters** `formatTelegramInboxMessage`, `formatTelegramStatusTransitionMessage` and moved telegram types/send (now in `telegram_notify.go`)

- [ ] **Step 3: `comment:created` — one Telegram send**

At end of `EventCommentCreated` handler (after `notifyMentionedMembers`), load issue via `queries.GetIssue` for identifier/title/slug (`queries.GetWorkspace` for slug). Resolve actor name (member: `GetUser`, agent: `GetAgent` or existing handler helpers). Call:

```go
sendWorkspaceTelegramHTML(ctx, queries, e.WorkspaceID, formatTelegramCommentHTML(...))
```

- [ ] **Step 4: `issue:updated` status — HTML**

Replace `sendWorkspaceTelegramMessage(..., formatTelegramStatusTransitionMessage(...))` with `formatTelegramStatusHTML` + `sendWorkspaceTelegramHTML`.

- [ ] **Step 5: Reaction events**

In `EventIssueReactionAdded` and `EventReactionAdded` handlers, after `notifyDirect`:

```go
if !telegramReactionsEnabled(workspaceTelegramConfig(ctx, queries, e.WorkspaceID)) {
	return // or skip only telegram block
}
sendWorkspaceTelegramHTML(ctx, queries, e.WorkspaceID, formatTelegramReactionHTML(...))
```

Load issue + actor same as comment path. `onComment := true` for `EventReactionAdded`.

- [ ] **Step 6: Compile**

Run: `cd server && go build ./cmd/server/`  
Expected: success

---

### Task 5: Telegram send count test (TDD)

**Files:**
- Modify: `server/cmd/server/telegram_notify_test.go` or `notification_listeners_test.go`

- [ ] **Step 1: Mock `telegramSendMessage` and assert single send on comment**

In `notification_listeners_test.go` (or new test file in `package main`):

```go
func TestTelegram_OneMessagePerCommentDespiteMentions(t *testing.T) {
	var sendCount int
	old := telegramSendMessage
	telegramSendMessage = func(ctx context.Context, botToken, userID, text, parseMode string) error {
		sendCount++
		return nil
	}
	t.Cleanup(func() { telegramSendMessage = old })

	// Configure workspace settings.telegram in DB OR mock workspaceTelegramConfig
	// Publish EventCommentCreated with @all mention content
	// Assert sendCount == 1
}
```

Use existing integration fixtures (`testWorkspaceID`, `newNotificationBus`) if available; otherwise unit-test bus handler in isolation with mocked queries.

Minimum bar: test that publishing one `EventCommentCreated` does not increment send count per inbox row (mock send, stub config returning valid telegram settings).

- [ ] **Step 2: Run test**

Run: `cd server && go test ./cmd/server/ -run TestTelegram_OneMessage -count=1`  
Expected: PASS

---

### Task 6: Integrations UI — move Telegram settings

**Files:**
- Modify: `packages/views/settings/components/integrations-tab.tsx`
- Create: `packages/views/settings/components/integrations-tab.test.tsx`
- Modify: `packages/views/settings/components/notifications-tab.tsx`
- Modify: `packages/views/settings/components/notifications-tab.test.tsx`
- Modify: `packages/views/locales/en/settings.json`
- Modify: `packages/views/locales/zh-Hans/settings.json`

- [ ] **Step 1: Move locale keys**

Under `integrations.telegram`, copy strings from `notifications.telegram` and add:

```json
"notify_reactions_label": "Send reaction notifications",
"notify_reactions_hint": "Post to Telegram when someone reacts to an issue or comment."
```

Remove `notifications.telegram` block from en + zh-Hans (keep inbox/system under `notifications`).

- [ ] **Step 2: Write failing Integrations test**

Port `notifications-tab.test.tsx` Telegram save test to `integrations-tab.test.tsx`; expect `notify_reactions` preserved/default on save.

- [ ] **Step 3: Implement `IntegrationsTab` Telegram card**

Mirror logic from `notifications-tab.tsx`:
- State: `botToken`, `userId`, `notifyReactions` (default `workspace?.settings.telegram?.notify_reactions !== false`)
- `canManageWorkspace` from member list
- Save merges `settings.telegram` including `notify_reactions: notifyReactions`
- Replace empty state OR show card above empty state when configuring (spec: show card; keep empty state below for future integrations)

- [ ] **Step 4: Remove Telegram section from `NotificationsTab`**

- [ ] **Step 5: Remove Telegram test from `notifications-tab.test.tsx`**

- [ ] **Step 6: Run view tests**

Run: `pnpm --filter @multica/views exec vitest run settings/components/integrations-tab.test.tsx settings/components/notifications-tab.test.tsx`  
Expected: PASS

---

### Task 7: CLI `notify_reactions` flag

**Files:**
- Modify: `server/cmd/multica/cmd_workspace.go`

- [ ] **Step 1: Add flag**

```go
workspaceTelegramCmd.Flags().Bool("notify-reactions", true, "Send reaction notifications to Telegram")
```

When not `--clear`, merge into `nextSettings["telegram"]`:

```go
notifyReactions, _ := cmd.Flags().GetBool("notify-reactions")
nextSettings["telegram"] = map[string]any{
	"bot_token": botToken,
	"user_id":   userID,
	"notify_reactions": notifyReactions,
}
```

- [ ] **Step 2: Build CLI**

Run: `cd server && go build ./cmd/multica/`  
Expected: success

---

### Task 8: End-to-end verification

- [ ] **Step 1: Go tests**

Run: `cd server && go test ./cmd/server/ -count=1`  
Expected: PASS (integration tests may need DB; run full `make test` if CI requires)

- [ ] **Step 2: Frontend**

Run: `pnpm typecheck && pnpm --filter @multica/views exec vitest run settings/`  
Expected: PASS

- [ ] **Step 3: Manual smoke (if dev env up)**

1. Workspace → Integrations → save bot + chat ID  
2. Comment with `@all` → one Telegram, HTML link on identifier  
3. Status change → one transition message  
4. Toggle reactions off → reaction produces inbox only  

---

## Plan self-review (spec coverage)

| Spec requirement | Task |
|------------------|------|
| Integrations UI | Task 6 |
| Remove account Telegram | Task 6 |
| `notify_reactions` storage | Tasks 1, 6, 7 |
| Remove inbox:new hook | Task 4 |
| One send per comment | Tasks 4, 5 |
| HTML templates | Tasks 3, 4 |
| Status + reactions | Tasks 4 |
| FRONTEND_ORIGIN links | Tasks 2, 3 |
| v1 excludes assignee/priority inbox | Task 4 (no new hooks) |
| Unit tests | Tasks 2, 3, 5 |

No placeholders remain in task steps above.
