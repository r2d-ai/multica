package main

import (
	"strings"
	"testing"
)

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

func TestParseTelegramSendResponseOKFalse(t *testing.T) {
	err := parseTelegramSendResponse(200, []byte(`{"ok":false,"description":"chat not found"}`))
	if err == nil || !strings.Contains(err.Error(), "chat not found") {
		t.Fatalf("expected chat not found error, got %v", err)
	}
}

func TestParseTelegramSendResponseOKTrue(t *testing.T) {
	if err := parseTelegramSendResponse(200, []byte(`{"ok":true}`)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFormatTelegramCommentHTML(t *testing.T) {
	html := formatTelegramCommentHTML(formatTelegramInput{
		WorkspaceName: "R&D Team",
		Origin:        "https://app.test",
		Slug:          "rd",
		Identifier:    "RD-65",
		IssueID:       "uuid",
		Title:         "Allow IP <test>",
		ActorName:     "Binh",
		AssigneeName:  "Alex",
		ProjectName:   "Sandbox",
		Preview:       "hello",
	})
	if !strings.Contains(html, "RD-65") || !strings.Contains(html, `href="https://app.test/rd/issues/RD-65"`) {
		t.Fatalf("missing link: %s", html)
	}
	if strings.Contains(html, "<test>") {
		t.Fatal("title must be escaped")
	}
	if !strings.Contains(html, "Assignee: Alex") || !strings.Contains(html, "Project: Sandbox") {
		t.Fatalf("missing meta lines: %s", html)
	}
}

func TestFormatTelegramStatusHTMLIncludesActorAndAssignee(t *testing.T) {
	html := formatTelegramStatusHTML(formatTelegramInput{
		WorkspaceName: "R&D Team",
		Identifier:    "RD-77",
		Title:         "Sandbox task",
		ActorName:     "Alex",
		AssigneeName:  "Alex",
	}, "todo", "backlog")
	if !strings.Contains(html, "Alex") || !strings.Contains(html, "changed status") {
		t.Fatalf("missing actor line: %s", html)
	}
	if !strings.Contains(html, "Assignee: Alex") {
		t.Fatalf("missing assignee: %s", html)
	}
	if !strings.Contains(html, "Todo → Backlog") {
		t.Fatalf("missing transition: %s", html)
	}
}

func TestFormatTelegramCreatedHTML(t *testing.T) {
	html := formatTelegramCreatedHTML(formatTelegramInput{
		WorkspaceName: "R&D Team",
		Origin:        "https://app.test",
		Slug:          "rd",
		Identifier:    "RD-73",
		Title:         "New task",
		ActorName:     "Alex",
		AssigneeName:  "Alex",
		ProjectName:   "DevSecOps",
		DueDate:       "2026-06-15",
	})
	for _, want := range []string{"🆕", "RD-73", "Project: DevSecOps", "Assignee: Alex", "Due: 2026-06-15", "Created by <b>Alex</b>"} {
		if !strings.Contains(html, want) {
			t.Fatalf("missing %q in %s", want, html)
		}
	}
}

func TestStripMentionsForPreviewTruncatesAndStripsMarkdown(t *testing.T) {
	long := strings.Repeat("word ", 40)
	got := stripMentionsForPreview("**Hello** @Alex\n- item one\n" + long)
	if strings.Contains(got, "**") || strings.Contains(got, "\n") {
		t.Fatalf("markdown/newlines should be stripped: %q", got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("expected ellipsis truncation, got %q", got)
	}
	if len([]rune(got)) > telegramPreviewMaxRunes {
		t.Fatalf("preview too long: %d runes", len([]rune(got)))
	}
}

func TestTruncateWithEllipsisTitle(t *testing.T) {
	longTitle := strings.Repeat("a", 100)
	line := formatTelegramIssueLine(formatTelegramInput{
		Identifier: "RD-1",
		Title:      longTitle,
	})
	if strings.Contains(line, strings.Repeat("a", 90)) {
		t.Fatalf("title should be truncated: %s", line)
	}
	if !strings.Contains(line, "…") {
		t.Fatalf("expected ellipsis in title: %s", line)
	}
}
