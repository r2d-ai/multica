package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	telegramTitleMaxRunes  = 80
	telegramPreviewMaxRunes = 120
)

var (
	telegramMarkdownLinkRe = regexp.MustCompile(`\[([^\]]+)\]\([^)]+\)`)
	telegramBoldRe         = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	telegramItalicRe       = regexp.MustCompile(`(?m)\*([^*]+)\*`)
	telegramCodeRe         = regexp.MustCompile("`([^`]+)`")
	telegramWhitespaceRe   = regexp.MustCompile(`\s+`)
)

// formatTelegramInput is shared by Telegram HTML message formatters.
type formatTelegramInput struct {
	WorkspaceName string
	Origin        string
	Slug          string
	Identifier    string
	IssueID       string
	Title         string
	ActorName     string
	AssigneeName  string
	ProjectName   string
	DueDate       string
	Preview       string
}

type telegramSettings struct {
	BotToken        string `json:"bot_token"`
	UserID          string `json:"user_id"`
	NotifyReactions *bool  `json:"notify_reactions,omitempty"`
}

type workspaceNotificationSettings struct {
	Telegram *telegramSettings `json:"telegram"`
}

var telegramSendMessage = func(ctx context.Context, botToken, userID, text, parseMode string) error {
	payload := map[string]any{
		"chat_id":                  userID,
		"text":                     text,
		"disable_web_page_preview": true,
	}
	if parseMode != "" {
		payload["parse_mode"] = parseMode
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal telegram payload: %w", err)
	}

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", botToken),
		bytes.NewReader(body),
	)
	if err != nil {
		return fmt.Errorf("build telegram request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("telegram request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("telegram sendMessage read response: %w", err)
	}
	return parseTelegramSendResponse(resp.StatusCode, respBody)
}

func parseTelegramSendResponse(statusCode int, body []byte) error {
	if statusCode >= 400 {
		return fmt.Errorf("telegram sendMessage returned %d: %s", statusCode, strings.TrimSpace(string(body)))
	}

	var apiResp struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return fmt.Errorf("telegram sendMessage decode response: %w", err)
	}
	if !apiResp.OK {
		desc := strings.TrimSpace(apiResp.Description)
		if desc == "" {
			desc = "unknown error"
		}
		return fmt.Errorf("telegram sendMessage: %s", desc)
	}
	return nil
}

func workspaceTelegramConfig(ctx context.Context, queries *db.Queries, workspaceID string) *telegramSettings {
	ws, err := queries.GetWorkspace(ctx, parseUUID(workspaceID))
	if err != nil || len(ws.Settings) == 0 {
		return nil
	}

	var settings workspaceNotificationSettings
	if err := json.Unmarshal(ws.Settings, &settings); err != nil || settings.Telegram == nil {
		return nil
	}
	if strings.TrimSpace(settings.Telegram.BotToken) == "" || strings.TrimSpace(settings.Telegram.UserID) == "" {
		return nil
	}
	return settings.Telegram
}

func sendWorkspaceTelegramHTML(ctx context.Context, queries *db.Queries, workspaceID, htmlText string) {
	cfg := workspaceTelegramConfig(ctx, queries, workspaceID)
	if cfg == nil {
		return
	}
	if err := telegramSendMessage(ctx, cfg.BotToken, cfg.UserID, htmlText, "HTML"); err != nil {
		slog.Error("failed to send telegram notification", "workspace_id", workspaceID, "error", err)
	}
}

func htmlEscape(s string) string {
	return html.EscapeString(s)
}

func issueDeepLink(origin, slug, identifier, issueID string) string {
	origin = strings.TrimSpace(origin)
	if origin == "" {
		return ""
	}
	segment := strings.TrimSpace(identifier)
	if segment == "" {
		segment = strings.TrimSpace(issueID)
	}
	if segment == "" {
		return ""
	}
	return fmt.Sprintf("%s/%s/issues/%s", strings.TrimRight(origin, "/"), slug, segment)
}

func frontendOrigin() string {
	return strings.TrimSpace(os.Getenv("FRONTEND_ORIGIN"))
}

func telegramReactionsEnabled(cfg *telegramSettings) bool {
	if cfg == nil || cfg.NotifyReactions == nil {
		return true
	}
	return *cfg.NotifyReactions
}

func truncateWithEllipsis(s string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	if max <= 1 {
		return string(runes[:max])
	}
	return string(runes[:max-1]) + "…"
}

func formatTelegramIssueLine(in formatTelegramInput) string {
	title := htmlEscape(truncateWithEllipsis(in.Title, telegramTitleMaxRunes))
	segment := strings.TrimSpace(in.Identifier)
	if segment == "" {
		segment = strings.TrimSpace(in.IssueID)
	}
	link := issueDeepLink(in.Origin, in.Slug, in.Identifier, in.IssueID)
	if link != "" && segment != "" {
		return fmt.Sprintf(`<a href="%s">%s</a> · %s`, htmlEscape(link), htmlEscape(segment), title)
	}
	if segment != "" {
		return fmt.Sprintf("%s · %s", htmlEscape(segment), title)
	}
	return title
}

func writeTelegramIssueMeta(b *strings.Builder, in formatTelegramInput) {
	assignee := strings.TrimSpace(in.AssigneeName)
	if assignee == "" {
		assignee = "Unassigned"
	}
	b.WriteString("\nAssignee: ")
	b.WriteString(htmlEscape(assignee))

	project := strings.TrimSpace(in.ProjectName)
	if project != "" {
		b.WriteString("\nProject: ")
		b.WriteString(htmlEscape(project))
	}

	due := strings.TrimSpace(in.DueDate)
	if due != "" {
		b.WriteString("\nDue: ")
		b.WriteString(htmlEscape(due))
	}
}

func formatTelegramCreatedHTML(in formatTelegramInput) string {
	var b strings.Builder
	b.WriteString("🆕 <b>")
	b.WriteString(htmlEscape(in.WorkspaceName))
	b.WriteString("</b>\n")
	b.WriteString(formatTelegramIssueLine(in))
	writeTelegramIssueMeta(&b, in)
	b.WriteString("\nCreated by <b>")
	b.WriteString(htmlEscape(in.ActorName))
	b.WriteString("</b>")
	return b.String()
}

func formatTelegramCommentHTML(in formatTelegramInput) string {
	var b strings.Builder
	b.WriteString("💬 <b>")
	b.WriteString(htmlEscape(in.WorkspaceName))
	b.WriteString("</b>\n")
	b.WriteString(formatTelegramIssueLine(in))
	writeTelegramIssueMeta(&b, in)
	b.WriteString("\n\n<b>")
	b.WriteString(htmlEscape(in.ActorName))
	b.WriteString("</b> commented:\n")
	b.WriteString(htmlEscape(in.Preview))
	return b.String()
}

func formatTelegramStatusHTML(in formatTelegramInput, fromStatus, toStatus string) string {
	var b strings.Builder
	b.WriteString("✅ <b>")
	b.WriteString(htmlEscape(in.WorkspaceName))
	b.WriteString("</b>\n")
	b.WriteString(formatTelegramIssueLine(in))
	writeTelegramIssueMeta(&b, in)
	b.WriteString("\n\n<b>")
	b.WriteString(htmlEscape(in.ActorName))
	b.WriteString("</b> changed status: ")
	b.WriteString(htmlEscape(statusLabel(fromStatus)))
	b.WriteString(" → ")
	b.WriteString(htmlEscape(statusLabel(toStatus)))
	return b.String()
}

func formatTelegramReactionHTML(in formatTelegramInput, emoji string, onComment bool) string {
	segment := strings.TrimSpace(in.Identifier)
	if segment == "" {
		segment = strings.TrimSpace(in.IssueID)
	}
	link := issueDeepLink(in.Origin, in.Slug, in.Identifier, in.IssueID)

	var issuePart string
	if link != "" && segment != "" {
		issuePart = fmt.Sprintf(`<a href="%s">%s</a>`, htmlEscape(link), htmlEscape(segment))
	} else if segment != "" {
		issuePart = htmlEscape(segment)
	}

	var b strings.Builder
	b.WriteString("👍 <b>")
	b.WriteString(htmlEscape(in.WorkspaceName))
	b.WriteString("</b>\n<b>")
	b.WriteString(htmlEscape(in.ActorName))
	b.WriteString("</b> reacted ")
	b.WriteString(htmlEscape(emoji))
	b.WriteString(" on ")
	b.WriteString(issuePart)
	if onComment {
		b.WriteString(" on a comment")
	}
	b.WriteString("\n<i>")
	b.WriteString(htmlEscape(in.Title))
	b.WriteString("</i>")
	return b.String()
}

func stripMentionsForPreview(content string) string {
	s := util.MentionRe.ReplaceAllString(content, "@$1")
	s = telegramMarkdownLinkRe.ReplaceAllString(s, "$1")
	s = telegramBoldRe.ReplaceAllString(s, "$1")
	s = telegramItalicRe.ReplaceAllString(s, "$1")
	s = telegramCodeRe.ReplaceAllString(s, "$1")
	s = strings.ReplaceAll(s, "\n", " ")
	s = telegramWhitespaceRe.ReplaceAllString(strings.TrimSpace(s), " ")
	return truncateWithEllipsis(s, telegramPreviewMaxRunes)
}

func telegramIssuePrefix(ws db.Workspace) string {
	if p := strings.TrimSpace(ws.IssuePrefix); p != "" {
		return p
	}
	var letters []rune
	for _, r := range ws.Name {
		if unicode.IsLetter(r) {
			letters = append(letters, unicode.ToUpper(r))
		}
	}
	if len(letters) == 0 {
		return "WS"
	}
	if len(letters) > 3 {
		letters = letters[:3]
	}
	return string(letters)
}

func telegramIssueIdentifier(ws db.Workspace, issue db.Issue) string {
	return telegramIssuePrefix(ws) + "-" + strconv.Itoa(int(issue.Number))
}

func telegramActorDisplayName(ctx context.Context, queries *db.Queries, actorType, actorID string) string {
	if strings.TrimSpace(actorID) == "" {
		return "Someone"
	}
	switch actorType {
	case "member":
		u, err := queries.GetUser(ctx, parseUUID(actorID))
		if err == nil && strings.TrimSpace(u.Name) != "" {
			return u.Name
		}
	case "agent":
		a, err := queries.GetAgent(ctx, parseUUID(actorID))
		if err == nil && strings.TrimSpace(a.Name) != "" {
			return a.Name
		}
	}
	return "Someone"
}

func telegramAssigneeDisplayName(ctx context.Context, queries *db.Queries, issue db.Issue) string {
	if !issue.AssigneeType.Valid || !issue.AssigneeID.Valid {
		return ""
	}
	return telegramActorDisplayName(ctx, queries, issue.AssigneeType.String, util.UUIDToString(issue.AssigneeID))
}

func telegramProjectName(ctx context.Context, queries *db.Queries, issue db.Issue) string {
	if !issue.ProjectID.Valid {
		return ""
	}
	project, err := queries.GetProject(ctx, issue.ProjectID)
	if err != nil || strings.TrimSpace(project.Title) == "" {
		return ""
	}
	return project.Title
}

func telegramDueDateDisplay(issue db.Issue) string {
	if !issue.DueDate.Valid {
		return ""
	}
	return issue.DueDate.Time.UTC().Format("2006-01-02")
}

func buildTelegramInput(
	ctx context.Context,
	queries *db.Queries,
	workspaceID string,
	issue db.Issue,
	actorType, actorID, preview string,
) formatTelegramInput {
	workspaceName := workspaceID
	slug := ""
	identifier := ""
	if ws, err := queries.GetWorkspace(ctx, parseUUID(workspaceID)); err == nil {
		if strings.TrimSpace(ws.Name) != "" {
			workspaceName = ws.Name
		}
		slug = ws.Slug
		identifier = telegramIssueIdentifier(ws, issue)
	}
	return formatTelegramInput{
		WorkspaceName: workspaceName,
		Origin:        frontendOrigin(),
		Slug:          slug,
		Identifier:    identifier,
		IssueID:       util.UUIDToString(issue.ID),
		Title:         issue.Title,
		ActorName:     telegramActorDisplayName(ctx, queries, actorType, actorID),
		AssigneeName:  telegramAssigneeDisplayName(ctx, queries, issue),
		ProjectName:   telegramProjectName(ctx, queries, issue),
		DueDate:       telegramDueDateDisplay(issue),
		Preview:       preview,
	}
}
