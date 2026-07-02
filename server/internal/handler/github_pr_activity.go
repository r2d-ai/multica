package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const (
	ghPREventIssueComment            = "issue_comment"
	ghPREventPullRequestReview       = "pull_request_review"
	ghPREventPullRequestReviewComment = "pull_request_review_comment"
	ghPREventPullRequestReviewThread = "pull_request_review_thread"
)

type ghRepoRef struct {
	Name  string `json:"name"`
	Owner struct {
		Login string `json:"login"`
	} `json:"owner"`
}

type ghInstallationRef struct {
	ID int64 `json:"id"`
}

type ghSender struct {
	Login string `json:"login"`
	Type  string `json:"type"`
}

type ghIssueCommentPayload struct {
	Action string `json:"action"`
	Issue  struct {
		Number      int32 `json:"number"`
		PullRequest *struct {
			URL string `json:"url"`
		} `json:"pull_request"`
	} `json:"issue"`
	Comment struct {
		ID      int64  `json:"id"`
		Body    string `json:"body"`
		HTMLURL string `json:"html_url"`
		User    ghSender `json:"user"`
	} `json:"comment"`
	Repository   ghRepoRef         `json:"repository"`
	Installation ghInstallationRef `json:"installation"`
}

type ghPullRequestReviewPayload struct {
	Action string `json:"action"`
	Review struct {
		ID      int64  `json:"id"`
		Body    string `json:"body"`
		State   string `json:"state"`
		HTMLURL string `json:"html_url"`
		User    ghSender `json:"user"`
	} `json:"review"`
	PullRequest struct {
		Number int32 `json:"number"`
	} `json:"pull_request"`
	Repository   ghRepoRef         `json:"repository"`
	Installation ghInstallationRef `json:"installation"`
}

type ghPullRequestReviewCommentPayload struct {
	Action string `json:"action"`
	Comment struct {
		ID           int64  `json:"id"`
		Body         string `json:"body"`
		HTMLURL      string `json:"html_url"`
		Path         string `json:"path"`
		Line         int32  `json:"line"`
		InReplyToID  *int64 `json:"in_reply_to_id"`
		User         ghSender `json:"user"`
	} `json:"comment"`
	PullRequest struct {
		Number int32 `json:"number"`
	} `json:"pull_request"`
	Repository   ghRepoRef         `json:"repository"`
	Installation ghInstallationRef `json:"installation"`
}

type ghPullRequestReviewThreadPayload struct {
	Action string `json:"action"`
	Thread struct {
		ID       int64 `json:"id"`
		Comments []struct {
			ID int64 `json:"id"`
		} `json:"comments"`
	} `json:"thread"`
	PullRequest struct {
		Number int32 `json:"number"`
	} `json:"pull_request"`
	Repository   ghRepoRef         `json:"repository"`
	Installation ghInstallationRef `json:"installation"`
}

type linkedPRContext struct {
	workspaceID pgtype.UUID
	pr          db.GithubPullRequest
	issueIDs    []pgtype.UUID
}

func (h *Handler) handleIssueCommentEvent(ctx context.Context, body []byte) {
	var p ghIssueCommentPayload
	if err := json.Unmarshal(body, &p); err != nil {
		slog.Warn("github: bad issue_comment payload", "err", err)
		return
	}
	if p.Action != "created" || p.Issue.PullRequest == nil {
		return
	}
	linked, ok := h.resolveLinkedPRContext(ctx, p.Installation.ID, p.Repository.Owner.Login, p.Repository.Name, p.Issue.Number)
	if !ok {
		slog.Info("github: skip issue_comment — PR not linked", "pr_number", p.Issue.Number)
		return
	}
	for _, issueID := range linked.issueIDs {
		h.mirrorIssueCommentToIssue(ctx, linked, issueID, p)
	}
}

func (h *Handler) handlePullRequestReviewEvent(ctx context.Context, body []byte) {
	var p ghPullRequestReviewPayload
	if err := json.Unmarshal(body, &p); err != nil {
		slog.Warn("github: bad pull_request_review payload", "err", err)
		return
	}
	if p.Action != "submitted" {
		return
	}
	state := strings.ToLower(strings.TrimSpace(p.Review.State))
	if state != "approved" && state != "changes_requested" && state != "commented" {
		return
	}
	linked, ok := h.resolveLinkedPRContext(ctx, p.Installation.ID, p.Repository.Owner.Login, p.Repository.Name, p.PullRequest.Number)
	if !ok {
		slog.Info("github: skip pull_request_review — PR not linked", "pr_number", p.PullRequest.Number)
		return
	}
	for _, issueID := range linked.issueIDs {
		h.mirrorPullRequestReviewToIssue(ctx, linked, issueID, p, state)
	}
}

func (h *Handler) handlePullRequestReviewCommentEvent(ctx context.Context, body []byte) {
	var p ghPullRequestReviewCommentPayload
	if err := json.Unmarshal(body, &p); err != nil {
		slog.Warn("github: bad pull_request_review_comment payload", "err", err)
		return
	}
	if p.Action != "created" {
		return
	}
	linked, ok := h.resolveLinkedPRContext(ctx, p.Installation.ID, p.Repository.Owner.Login, p.Repository.Name, p.PullRequest.Number)
	if !ok {
		slog.Info("github: skip pull_request_review_comment — PR not linked", "pr_number", p.PullRequest.Number)
		return
	}
	for _, issueID := range linked.issueIDs {
		h.mirrorPullRequestReviewCommentToIssue(ctx, linked, issueID, p)
	}
}

func (h *Handler) handlePullRequestReviewThreadEvent(ctx context.Context, body []byte) {
	var p ghPullRequestReviewThreadPayload
	if err := json.Unmarshal(body, &p); err != nil {
		slog.Warn("github: bad pull_request_review_thread payload", "err", err)
		return
	}
	action := strings.ToLower(strings.TrimSpace(p.Action))
	if action != "resolved" && action != "unresolved" {
		return
	}
	linked, ok := h.resolveLinkedPRContext(ctx, p.Installation.ID, p.Repository.Owner.Login, p.Repository.Name, p.PullRequest.Number)
	if !ok {
		slog.Info("github: skip pull_request_review_thread — PR not linked", "pr_number", p.PullRequest.Number)
		return
	}
	for _, issueID := range linked.issueIDs {
		h.handlePullRequestReviewThreadForIssue(ctx, linked, issueID, p, action)
	}
}

func (h *Handler) resolveLinkedPRContext(ctx context.Context, installationID int64, repoOwner, repoName string, prNumber int32) (linkedPRContext, bool) {
	var out linkedPRContext
	if installationID == 0 {
		return out, false
	}
	insts, err := h.Queries.ListGitHubInstallationsByInstallationID(ctx, installationID)
	if err != nil {
		slog.Warn("github: lookup installation failed", "err", err)
		return out, false
	}
	if len(insts) == 0 {
		return out, false
	}
	inst := insts[0]
	wsID := h.resolveWorkspaceForRepo(ctx, inst.WorkspaceID, inst.AccountLogin, repoOwner, repoName)
	pr, err := h.Queries.GetGitHubPullRequest(ctx, db.GetGitHubPullRequestParams{
		WorkspaceID: wsID,
		RepoOwner:   repoOwner,
		RepoName:    repoName,
		PrNumber:    prNumber,
	})
	if err != nil {
		if !errorsIsNoRows(err) {
			slog.Warn("github: lookup pr for activity failed", "err", err)
		}
		return out, false
	}
	issueIDs, err := h.Queries.ListIssueIDsForPullRequest(ctx, pr.ID)
	if err != nil {
		slog.Warn("github: list linked issues failed", "err", err)
		return out, false
	}
	if len(issueIDs) == 0 {
		return out, false
	}
	out.workspaceID = wsID
	out.pr = pr
	out.issueIDs = issueIDs
	return out, true
}

func errorsIsNoRows(err error) bool {
	return errors.Is(err, pgx.ErrNoRows)
}

func (h *Handler) mirrorIssueCommentToIssue(ctx context.Context, linked linkedPRContext, issueID pgtype.UUID, p ghIssueCommentPayload) {
	body := strings.TrimSpace(p.Comment.Body)
	if !isGitHubPRActivityActionable(body, "", "", 0) {
		slog.Info("github: skip issue_comment noise", "comment_id", p.Comment.ID, "issue_id", uuidToString(issueID))
		return
	}
	activity, inserted, err := h.insertGitHubPRActivity(ctx, linked, issueID, ghPREventIssueComment, p.Comment.ID, p.Action, nil, "", body, p.Comment.User, p.Comment.HTMLURL, false)
	if err != nil || !inserted {
		return
	}
	content := formatGitHubPRActivityComment("PR comment", linked.pr, p.Comment.User.Login, "", 0, body)
	comment, err := h.createGitHubMirroredComment(ctx, issueID, linked.workspaceID, content, pgtype.UUID{})
	if err != nil {
		return
	}
	h.finalizeGitHubPRActivity(ctx, activity.ID, comment, comment.ID, nil, true)
}

func (h *Handler) mirrorPullRequestReviewToIssue(ctx context.Context, linked linkedPRContext, issueID pgtype.UUID, p ghPullRequestReviewPayload, state string) {
	body := strings.TrimSpace(p.Review.Body)
	mirrorApproved := state == "approved"
	actionable := state == "changes_requested" || isGitHubPRActivityActionable(body, state, "", 0)
	if !mirrorApproved && !actionable {
		slog.Info("github: skip pull_request_review noise", "review_id", p.Review.ID, "state", state)
		return
	}
	activity, inserted, err := h.insertGitHubPRActivity(ctx, linked, issueID, ghPREventPullRequestReview, p.Review.ID, p.Action, nil, state, body, p.Review.User, p.Review.HTMLURL, false)
	if err != nil || !inserted {
		return
	}
	label := fmt.Sprintf("PR review (%s)", state)
	content := formatGitHubPRActivityComment(label, linked.pr, p.Review.User.Login, "", 0, body)
	comment, err := h.createGitHubMirroredComment(ctx, issueID, linked.workspaceID, content, pgtype.UUID{})
	if err != nil {
		return
	}
	h.finalizeGitHubPRActivity(ctx, activity.ID, comment, comment.ID, nil, actionable)
}

func (h *Handler) mirrorPullRequestReviewCommentToIssue(ctx context.Context, linked linkedPRContext, issueID pgtype.UUID, p ghPullRequestReviewCommentPayload) {
	body := strings.TrimSpace(p.Comment.Body)
	if !isGitHubPRActivityActionable(body, "", p.Comment.Path, int(p.Comment.Line)) {
		slog.Info("github: skip pull_request_review_comment noise", "comment_id", p.Comment.ID)
		return
	}
	activity, inserted, err := h.insertGitHubPRActivity(ctx, linked, issueID, ghPREventPullRequestReviewComment, p.Comment.ID, p.Action, nil, "", body, p.Comment.User, p.Comment.HTMLURL, false)
	if err != nil || !inserted {
		return
	}
	parentID := pgtype.UUID{}
	threadRootID := pgtype.UUID{}
	if p.Comment.InReplyToID != nil {
		if parentActivity, err := h.Queries.GetGitHubPRActivityByThreadComment(ctx, db.GetGitHubPRActivityByThreadCommentParams{
			PullRequestID:      linked.pr.ID,
			IssueID:            issueID,
			GithubExternalID:   *p.Comment.InReplyToID,
		}); err == nil && parentActivity.CommentID.Valid {
			parentID = parentActivity.CommentID
			if parentActivity.ThreadRootCommentID.Valid {
				threadRootID = parentActivity.ThreadRootCommentID
			} else {
				threadRootID = parentActivity.CommentID
			}
		}
	}
	content := formatGitHubPRActivityComment("PR review comment", linked.pr, p.Comment.User.Login, p.Comment.Path, int(p.Comment.Line), body)
	comment, err := h.createGitHubMirroredComment(ctx, issueID, linked.workspaceID, content, parentID)
	if err != nil {
		return
	}
	root := comment.ID
	if threadRootID.Valid {
		root = threadRootID
	}
	h.finalizeGitHubPRActivity(ctx, activity.ID, comment, root, nil, true)
}

func (h *Handler) handlePullRequestReviewThreadForIssue(ctx context.Context, linked linkedPRContext, issueID pgtype.UUID, p ghPullRequestReviewThreadPayload, action string) {
	if len(p.Thread.Comments) == 0 {
		return
	}
	firstCommentID := p.Thread.Comments[0].ID
	commentActivity, err := h.Queries.GetGitHubPRActivityByThreadComment(ctx, db.GetGitHubPRActivityByThreadCommentParams{
		PullRequestID:    linked.pr.ID,
		IssueID:          issueID,
		GithubExternalID: firstCommentID,
	})
	if err != nil {
		slog.Info("github: skip review thread — no mirrored comment", "thread_id", p.Thread.ID)
		return
	}
	for _, tc := range p.Thread.Comments {
		_ = h.Queries.SetGitHubPRActivityThreadIDForComment(ctx, db.SetGitHubPRActivityThreadIDForCommentParams{
			PullRequestID:    linked.pr.ID,
			IssueID:          issueID,
			GithubThreadID:   pgtype.Int8{Int64: p.Thread.ID, Valid: true},
			GithubExternalID: tc.ID,
		})
	}
	threadRootID := commentActivity.ThreadRootCommentID
	if !threadRootID.Valid {
		threadRootID = commentActivity.CommentID
	}
	if !threadRootID.Valid {
		return
	}
	switch action {
	case "resolved":
		activity, inserted, err := h.insertGitHubPRActivity(ctx, linked, issueID, ghPREventPullRequestReviewThread, p.Thread.ID, p.Action, &p.Thread.ID, "", "", ghSender{}, "", true)
		if err != nil || !inserted {
			return
		}
		if _, err := h.Queries.ResolveComment(ctx, db.ResolveCommentParams{
			ID:              threadRootID,
			ResolvedByType:  strToText("system"),
			ResolvedByID:    pgtype.UUID{Valid: true},
		}); err != nil {
			slog.Warn("github: resolve mirrored thread failed", "err", err, "comment_id", uuidToString(threadRootID))
			return
		}
		_, _ = h.Queries.MarkGitHubPRActivityResolved(ctx, db.MarkGitHubPRActivityResolvedParams{
			ID:       commentActivity.ID,
			Resolved: true,
		})
		_, _ = h.Queries.MarkGitHubPRActivityResolved(ctx, db.MarkGitHubPRActivityResolvedParams{
			ID:       activity.ID,
			Resolved: true,
		})
		h.publish(protocol.EventCommentUpdated, uuidToString(linked.workspaceID), "system", "", map[string]any{
			"comment_id": uuidToString(threadRootID),
			"issue_id":   uuidToString(issueID),
			"resolved":   true,
		})
	case "unresolved":
		if !commentActivity.Resolved {
			return
		}
		activity, inserted, err := h.insertGitHubPRActivity(ctx, linked, issueID, ghPREventPullRequestReviewThread, p.Thread.ID, p.Action, &p.Thread.ID, "", "", ghSender{}, "", false)
		if err != nil || !inserted {
			return
		}
		_ = activity
		if _, err := h.Queries.UnresolveComment(ctx, threadRootID); err != nil {
			slog.Warn("github: unresolve mirrored thread failed", "err", err)
			return
		}
		_, _ = h.Queries.MarkGitHubPRActivityResolved(ctx, db.MarkGitHubPRActivityResolvedParams{
			ID:       commentActivity.ID,
			Resolved: false,
		})
		h.publish(protocol.EventCommentUpdated, uuidToString(linked.workspaceID), "system", "", map[string]any{
			"comment_id": uuidToString(threadRootID),
			"issue_id":   uuidToString(issueID),
			"resolved":   false,
		})
		h.dispatchGitHubPRActivityTrigger(ctx, issueID, linked.workspaceID, threadRootID)
	}
}

func (h *Handler) insertGitHubPRActivity(
	ctx context.Context,
	linked linkedPRContext,
	issueID pgtype.UUID,
	eventKind string,
	externalID int64,
	action string,
	threadID *int64,
	reviewState, body string,
	sender ghSender,
	githubURL string,
	resolved bool,
) (db.GithubPrActivity, bool, error) {
	var thread pgtype.Int8
	if threadID != nil {
		thread = pgtype.Int8{Int64: *threadID, Valid: true}
	}
	row, err := h.Queries.InsertGitHubPRActivity(ctx, db.InsertGitHubPRActivityParams{
		WorkspaceID:       linked.workspaceID,
		PullRequestID:     linked.pr.ID,
		IssueID:           issueID,
		EventKind:         eventKind,
		GithubExternalID:  externalID,
		Action:            action,
		GithubThreadID:    thread,
		ReviewState:       ptrToText(strPtrOrNil(reviewState)),
		BodyHash:          ptrToText(strPtrOrNil(githubPRActivityBodyHash(body))),
		ActorLogin:        ptrToText(strPtrOrNil(sender.Login)),
		ActorType:         ptrToText(strPtrOrNil(sender.Type)),
		GithubUrl:         ptrToText(strPtrOrNil(githubURL)),
		CommentID:         pgtype.UUID{},
		ThreadRootCommentID: pgtype.UUID{},
		Resolved:          resolved,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			slog.Info("github: duplicate pr activity delivery", "event_kind", eventKind, "external_id", externalID)
			return db.GithubPrActivity{}, false, nil
		}
		slog.Warn("github: insert pr activity failed", "err", err)
		return db.GithubPrActivity{}, false, err
	}
	return row, true, nil
}

func (h *Handler) createGitHubMirroredComment(ctx context.Context, issueID, workspaceID pgtype.UUID, content string, parentID pgtype.UUID) (db.Comment, error) {
	comment, err := h.Queries.CreateComment(ctx, db.CreateCommentParams{
		IssueID:     issueID,
		WorkspaceID: workspaceID,
		AuthorType:  "system",
		AuthorID:    pgtype.UUID{Valid: true},
		Content:     content,
		Type:        "comment",
		ParentID:    parentID,
	})
	if err != nil {
		slog.Warn("github: create mirrored comment failed", "err", err)
		return db.Comment{}, err
	}
	return comment, nil
}

func (h *Handler) finalizeGitHubPRActivity(ctx context.Context, activityID pgtype.UUID, comment db.Comment, threadRootID pgtype.UUID, threadID *int64, trigger bool) {
	var thread pgtype.Int8
	if threadID != nil {
		thread = pgtype.Int8{Int64: *threadID, Valid: true}
	}
	_, err := h.Queries.UpdateGitHubPRActivityCommentMapping(ctx, db.UpdateGitHubPRActivityCommentMappingParams{
		ID:                  activityID,
		CommentID:           comment.ID,
		ThreadRootCommentID: pgtype.UUID{Bytes: threadRootID.Bytes, Valid: threadRootID.Valid},
		GithubThreadID:      thread,
	})
	if err != nil {
		slog.Warn("github: update pr activity mapping failed", "err", err)
	}
	issue, err := h.Queries.GetIssue(ctx, comment.IssueID)
	if err != nil {
		return
	}
	h.publish(protocol.EventCommentCreated, uuidToString(comment.WorkspaceID), "system", "", map[string]any{
		"comment":             commentToResponse(comment, nil, nil),
		"issue_title":         issue.Title,
		"issue_assignee_type": textToPtr(issue.AssigneeType),
		"issue_assignee_id":   uuidToPtr(issue.AssigneeID),
		"issue_status":        issue.Status,
	})
	if trigger {
		h.dispatchGitHubPRActivityTrigger(ctx, issue.ID, issue.WorkspaceID, comment.ID)
	}
}

func (h *Handler) dispatchGitHubPRActivityTrigger(ctx context.Context, issueID, workspaceID, triggerCommentID pgtype.UUID) {
	issue, err := h.Queries.GetIssue(ctx, issueID)
	if err != nil {
		return
	}
	if issue.WorkspaceID != workspaceID {
		return
	}
	if !issue.AssigneeType.Valid || !issue.AssigneeID.Valid {
		return
	}
	switch issue.AssigneeType.String {
	case "agent":
		agent, err := h.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
			ID:          issue.AssigneeID,
			WorkspaceID: issue.WorkspaceID,
		})
		if err != nil || !agent.RuntimeID.Valid || agent.ArchivedAt.Valid {
			return
		}
		hasPending, err := h.Queries.HasPendingTaskForIssueAndAgent(ctx, db.HasPendingTaskForIssueAndAgentParams{
			IssueID: issue.ID,
			AgentID: issue.AssigneeID,
		})
		if err != nil || hasPending {
			return
		}
		if _, err := h.TaskService.EnqueueTaskForIssue(ctx, issue, triggerCommentID); err != nil {
			slog.Warn("github: enqueue agent task on pr activity failed", "issue_id", uuidToString(issue.ID), "err", err)
		}
	case "squad":
		squad, err := h.Queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{
			ID:          issue.AssigneeID,
			WorkspaceID: issue.WorkspaceID,
		})
		if err != nil {
			return
		}
		agent, err := h.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
			ID:          squad.LeaderID,
			WorkspaceID: issue.WorkspaceID,
		})
		if err != nil || !agent.RuntimeID.Valid || agent.ArchivedAt.Valid {
			return
		}
		hasPending, err := h.Queries.HasPendingTaskForIssueAndAgent(ctx, db.HasPendingTaskForIssueAndAgentParams{
			IssueID: issue.ID,
			AgentID: squad.LeaderID,
		})
		if err != nil || hasPending {
			return
		}
		if _, err := h.TaskService.EnqueueTaskForSquadLeader(ctx, issue, squad.LeaderID, squad.ID, triggerCommentID); err != nil {
			slog.Warn("github: enqueue squad leader task on pr activity failed", "issue_id", uuidToString(issue.ID), "err", err)
		}
	}
}

func formatGitHubPRActivityComment(kind string, pr db.GithubPullRequest, actorLogin, path string, line int, body string) string {
	var b strings.Builder
	b.WriteString("**GitHub ")
	b.WriteString(kind)
	b.WriteString("**")
	if actorLogin != "" {
		b.WriteString(" from ")
		b.WriteString(actorLogin)
	}
	b.WriteString(" on [")
	b.WriteString(pr.RepoOwner)
	b.WriteString("/")
	b.WriteString(pr.RepoName)
	b.WriteString(" #")
	b.WriteString(strconvItoa(int(pr.PrNumber)))
	b.WriteString("](")
	b.WriteString(pr.HtmlUrl)
	b.WriteString(")")
	if path != "" {
		b.WriteString(" — `")
		b.WriteString(path)
		if line > 0 {
			b.WriteString(":")
			b.WriteString(strconvItoa(line))
		}
		b.WriteString("`")
	}
	b.WriteString("\n\n")
	if strings.TrimSpace(body) != "" {
		b.WriteString(body)
	}
	return b.String()
}

func strconvItoa(n int) string {
	return fmt.Sprintf("%d", n)
}

func githubPRActivityBodyHash(body string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(body)))
	return hex.EncodeToString(sum[:])
}

var (
	ghPRAckRe = regexp.MustCompile(`(?i)^\s*(thanks|thank you|thx|ty|ok|okay|done|fixed|lgtm|\+1|resolved|addressed|looks good|sgtm)\s*[!.,]?\s*$`)
	ghPRCIChurnRes = []*regexp.Regexp{
		regexp.MustCompile(`(?i)all checks have passed`),
		regexp.MustCompile(`(?i)some checks were not successful`),
		regexp.MustCompile(`(?i)required check`),
		regexp.MustCompile(`(?i)build (succeeded|failed|status)`),
		regexp.MustCompile(`(?i)coverage (report|delta|changed)`),
		regexp.MustCompile(`(?i)dependabot`),
		regexp.MustCompile(`(?i)renovate`),
	}
	ghPRFileLineRe = regexp.MustCompile(`(?i)\b[\w./-]+\.(go|ts|tsx|js|jsx|py|rs|java|md|yml|yaml|json|sql)\b`)
)

// isGitHubPRActivityActionable applies v1 content/state heuristics. Bot senders
// are not blanket-filtered — changes_requested, file/line refs, and non-ack
// bodies can still trigger.
func isGitHubPRActivityActionable(body, reviewState, path string, line int) bool {
	if strings.EqualFold(strings.TrimSpace(reviewState), "changes_requested") {
		return true
	}
	normalized := strings.TrimSpace(body)
	if normalized == "" && path == "" {
		return false
	}
	if ghPRAckRe.MatchString(normalized) {
		return false
	}
	if path != "" && line > 0 {
		return true
	}
	if path != "" {
		return true
	}
	if ghPRFileLineRe.MatchString(normalized) {
		return true
	}
	if isGitHubCIBotChurn(normalized) {
		return false
	}
	return normalized != ""
}

func isGitHubCIBotChurn(body string) bool {
	if body == "" {
		return false
	}
	for _, re := range ghPRCIChurnRes {
		if re.MatchString(body) {
			return true
		}
	}
	return false
}

func isGitHubAcknowledgement(body string) bool {
	return ghPRAckRe.MatchString(strings.TrimSpace(body))
}
