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
		Preview:       "hello",
	})
	if !strings.Contains(html, "RD-65") || !strings.Contains(html, `href="https://app.test/rd/issues/RD-65"`) {
		t.Fatalf("missing link: %s", html)
	}
	if strings.Contains(html, "<test>") {
		t.Fatal("title must be escaped")
	}
}
