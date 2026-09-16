package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/multica-ai/multica/server/internal/r2dauth"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	r2dProjectSearchChunk   = 50
	r2dProjectSearchMaxScan = 500
)

func r2dProjectSearchWindow(r *http.Request) (limit, offset, need int, ok bool) {
	limit = 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if value, err := strconv.Atoi(raw); err == nil && value > 0 {
			limit = value
		}
	}
	if limit > 50 {
		limit = 50
	}
	offset = 0
	if raw := r.URL.Query().Get("offset"); raw != "" {
		if value, err := strconv.Atoi(raw); err == nil && value >= 0 {
			offset = value
		}
	}
	need = offset + limit
	return limit, offset, need, need <= r2dProjectSearchMaxScan
}

// r2dFilterProjectSearchPage keeps only rows that explicitly name the already
// authorized Project. Projectless, malformed and unrelated rows are dropped.
// Removal preserves upstream ranking because relative order never changes.
func r2dFilterProjectSearchPage(issues []json.RawMessage, projectID string) []json.RawMessage {
	out := make([]json.RawMessage, 0, len(issues))
	for _, raw := range issues {
		var identity struct {
			ProjectID *string `json:"project_id"`
		}
		if json.Unmarshal(raw, &identity) != nil || identity.ProjectID == nil || *identity.ProjectID != projectID {
			continue
		}
		out = append(out, raw)
	}
	return out
}

// r2dFilterWorkspaceSearchPage is the Workspace-member counterpart: projectless
// Issues are Team-private but readable after membership, while Project Issues
// survive only when their Project is in the caller's precomputed readable set.
func r2dFilterWorkspaceSearchPage(issues []json.RawMessage, readableProjectIDs []string) []json.RawMessage {
	readable := make(map[string]struct{}, len(readableProjectIDs))
	for _, id := range readableProjectIDs {
		readable[id] = struct{}{}
	}
	out := make([]json.RawMessage, 0, len(issues))
	for _, raw := range issues {
		var identity struct {
			ProjectID *string `json:"project_id"`
		}
		if json.Unmarshal(raw, &identity) != nil {
			continue
		}
		if identity.ProjectID == nil || *identity.ProjectID == "" {
			out = append(out, raw)
			continue
		}
		if _, ok := readable[*identity.ProjectID]; ok {
			out = append(out, raw)
		}
	}
	return out
}

type r2dIssueSearchPageFilter func([]json.RawMessage) []json.RawMessage

// r2dServeBoundedIssueSearch reuses the upstream SearchIssues ranking engine
// but applies ACL before caller-visible pagination. Filtering each one-page
// response is safe but sparse: hidden rows consume LIMIT/OFFSET positions. A
// bounded scan preserves upstream ordering while making offset/limit relative
// to the authorized result set.
func r2dServeBoundedIssueSearch(w http.ResponseWriter, r *http.Request, next http.Handler, ctx context.Context, filter r2dIssueSearchPageFilter, scopeName string) bool {
	limit, offset, need, ok := r2dProjectSearchWindow(r)
	if !ok {
		writeError(w, http.StatusBadRequest, scopeName+" search offset is too large; refine the query")
		return true
	}

	matches := make([]json.RawMessage, 0, need)
	var last *r2dResponseBuffer
	lastPageSize := 0
	for scanOffset := 0; scanOffset < r2dProjectSearchMaxScan && len(matches) < need; scanOffset += r2dProjectSearchChunk {
		page := r.Clone(ctx)
		query := page.URL.Query()
		query.Del("project_id") // upstream SearchIssues has no Project filter
		query.Set("limit", strconv.Itoa(r2dProjectSearchChunk))
		query.Set("offset", strconv.Itoa(scanOffset))
		page.URL.RawQuery = query.Encode()

		buf := newR2DResponseBuffer()
		next.ServeHTTP(buf, page)
		last = buf
		if buf.status < 200 || buf.status >= 300 {
			copyR2DResponse(w, buf, buf.body.Bytes())
			return true
		}

		var envelope struct {
			Issues []json.RawMessage `json:"issues"`
		}
		if err := json.Unmarshal(buf.body.Bytes(), &envelope); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to apply project visibility")
			return true
		}
		lastPageSize = len(envelope.Issues)
		matches = append(matches, filter(envelope.Issues)...)
		if lastPageSize < r2dProjectSearchChunk {
			break
		}
	}

	if len(matches) < need && last != nil && lastPageSize == r2dProjectSearchChunk {
		// More Workspace-ranked rows may exist beyond the bounded scan. Returning
		// a short page here would lie about the authorized offset, so fail
		// explicitly and ask for a narrower search phrase.
		writeError(w, http.StatusServiceUnavailable, scopeName+" search scope is too broad; refine the query")
		return true
	}

	start := offset
	if start > len(matches) {
		start = len(matches)
	}
	end := start + limit
	if end > len(matches) {
		end = len(matches)
	}
	body, err := json.Marshal(map[string]any{"issues": matches[start:end]})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to apply project visibility")
		return true
	}
	if last == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
		return true
	}
	copyR2DResponse(w, last, body)
	return true
}

// r2dServeProjectSearch adapts the upstream Workspace-scoped search into an
// explicitly Project-scoped read without copying its ranking SQL.
func r2dServeProjectSearch(queries *db.Queries, w http.ResponseWriter, r *http.Request, next http.Handler, userID, projectID string) bool {
	ownerWorkspaceID, _, handled := r2dRequireProjectOperation(
		queries, w, r, userID, projectID, r2dauth.OperationRead,
	)
	if handled {
		return true
	}
	ctx := SetWorkspaceIDContext(r.Context(), ownerWorkspaceID)
	return r2dServeBoundedIssueSearch(w, r, next, ctx, func(issues []json.RawMessage) []json.RawMessage {
		return r2dFilterProjectSearchPage(issues, projectID)
	}, "project")
}

// r2dServeWorkspaceSearch moves generic Workspace search before the ordinary
// membership middleware only to fix pagination semantics. It performs the same
// membership check itself, then computes readable Project IDs once and reuses
// the upstream search handler under the real Member context. Non-members fall
// through so the canonical Workspace non-disclosure response stays unchanged.
func r2dServeWorkspaceSearch(queries *db.Queries, w http.ResponseWriter, r *http.Request, next http.Handler, userID string) bool {
	workspaceID := ResolveWorkspaceIDFromRequest(r, queries)
	if workspaceID == "" {
		return false
	}
	member, isMember, err := r2dLoadWorkspaceMember(queries, r, userID, workspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to authorize workspace")
		return true
	}
	if !isMember {
		return false
	}
	readableProjectIDs, err := r2dReadableWorkspaceProjectIDs(queries, r, userID, workspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to apply project visibility")
		return true
	}
	ctx := SetMemberContext(r.Context(), workspaceID, member)
	return r2dServeBoundedIssueSearch(w, r, next, ctx, func(issues []json.RawMessage) []json.RawMessage {
		return r2dFilterWorkspaceSearchPage(issues, readableProjectIDs)
	}, "workspace")
}
