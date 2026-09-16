package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/r2dauth"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// SetWorkspaceIDContext injects only the workspace identity. Unlike
// SetMemberContext it does not pretend a cross-workspace Project collaborator
// owns a member row in the Project's Workspace.
func SetWorkspaceIDContext(ctx context.Context, workspaceID string) context.Context {
	return context.WithValue(ctx, ctxKeyWorkspaceID, workspaceID)
}

func r2dFacts(f db.R2DProjectAccessFacts) r2dauth.ProjectFacts {
	return r2dauth.ProjectFacts{
		ProjectID:          f.ProjectID,
		OwnerWorkspaceID:   f.OwnerWorkspaceID,
		Visibility:         r2dauth.Visibility(f.Visibility),
		OwnerWorkspaceRole: r2dauth.WorkspaceRole(f.OwnerWorkspaceRole),
		DirectGrantRole:    r2dauth.ProjectRole(f.DirectGrantRole),
		WorkspaceGrantRole: r2dauth.ProjectRole(f.WorkspaceGrantRole),
		GlobalObserver:     f.GlobalObserver,
	}
}

func r2dAuthorizeProject(queries *db.Queries, r *http.Request, userID, projectID string, op r2dauth.Operation) (ownerWorkspaceID string, allowed, exists bool, err error) {
	facts, err := queries.R2DLoadProjectAccessFacts(r.Context(), userID, projectID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, false, nil
	}
	if err != nil {
		return "", false, false, err
	}
	projectFacts := r2dFacts(facts)

	// Project resources are Workspace-owned execution metadata, not merely
	// Project content. They can expose repository URLs, daemon ids and local
	// filesystem paths. A cross-Workspace Project grant therefore never makes
	// the resource collection readable or writable. Owner-Workspace humans may
	// read it; mutations additionally require Project manage.
	if r2dProjectResourceRequest(r.URL.Path, projectID) {
		caps := r2dauth.ResolveCapabilities(projectFacts)
		switch op {
		case r2dauth.OperationRead:
			// On a mutating HTTP request this is the fallback read check used
			// below to choose 403 vs 404, so test Project readability rather
			// than resource visibility in that specific case.
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				return facts.OwnerWorkspaceID, caps.Read, true, nil
			}
			return facts.OwnerWorkspaceID, caps.ViewResources, true, nil
		case r2dauth.OperationManage:
			return facts.OwnerWorkspaceID, caps.ViewResources && caps.Manage, true, nil
		}
	}

	decision := r2dauth.Resolve(projectFacts)
	return facts.OwnerWorkspaceID, decision.Can(op), true, nil
}

func r2dProjectResourceRequest(path, projectID string) bool {
	path = strings.TrimSuffix(path, "/")
	prefix := "/api/projects/" + projectID + "/resources"
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

func r2dProjectOperation(r *http.Request, suffix string) r2dauth.Operation {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return r2dauth.OperationRead
	}
	if strings.HasPrefix(suffix, "/sharing") {
		return r2dauth.OperationShare
	}
	return r2dauth.OperationManage
}

func r2dIssueOperation(r *http.Request) r2dauth.Operation {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return r2dauth.OperationRead
	}
	return r2dauth.OperationContribute
}

// tryR2DProjectScope runs before the ordinary Workspace membership lookup.
// It handles only requests whose Project identity is unambiguous from the URL
// (or ListIssues' explicit project_id filter). Projectless work deliberately
// falls through to the original Workspace boundary.
//
// Task-token actors never enter this override. P02 intentionally grants
// workspace agents no implicit Project role; Agent/Squad execution policy is P06.
func tryR2DProjectScope(queries *db.Queries, w http.ResponseWriter, r *http.Request, next http.Handler, userID string) bool {
	if r.Header.Get("X-Actor-Source") == "task_token" {
		return false
	}

	path := strings.TrimSuffix(r.URL.Path, "/")
	if path == "" {
		path = "/"
	}

	if strings.HasPrefix(path, "/api/projects/") {
		rest := strings.TrimPrefix(path, "/api/projects/")
		parts := strings.SplitN(rest, "/", 2)
		projectID := parts[0]
		if projectID == "" || projectID == "search" {
			return false
		}
		if _, err := parseR2DUUID(projectID); err != nil {
			return false
		}
		suffix := ""
		if len(parts) == 2 {
			suffix = "/" + parts[1]
		}
		op := r2dProjectOperation(r, suffix)
		ownerWorkspaceID, allowed, exists, err := r2dAuthorizeProject(queries, r, userID, projectID, op)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to authorize project")
			return true
		}
		if !exists {
			writeError(w, http.StatusNotFound, "project not found")
			return true
		}
		if !allowed {
			// Preserve non-disclosure for a caller with no Project read access.
			_, readable, _, readErr := r2dAuthorizeProject(queries, r, userID, projectID, r2dauth.OperationRead)
			if readErr != nil {
				writeError(w, http.StatusInternalServerError, "failed to authorize project")
				return true
			}
			if !readable {
				writeError(w, http.StatusNotFound, "project not found")
			} else {
				writeError(w, http.StatusForbidden, "insufficient project permissions")
			}
			return true
		}
		ctx := SetWorkspaceIDContext(r.Context(), ownerWorkspaceID)
		next.ServeHTTP(w, r.WithContext(ctx))
		return true
	}

	if strings.HasPrefix(path, "/api/issues/") {
		rest := strings.TrimPrefix(path, "/api/issues/")
		parts := strings.SplitN(rest, "/", 2)
		issueID := parts[0]
		if issueID == "" {
			return false
		}
		if _, err := parseR2DUUID(issueID); err != nil {
			// Human-readable PREFIX-NUMBER identifiers remain bound to the
			// active Workspace because resolving them safely requires its prefix.
			return false
		}
		target, err := queries.R2DLoadIssueACLTarget(r.Context(), issueID)
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "issue not found")
			return true
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to authorize issue")
			return true
		}
		if target.ProjectID == "" {
			return false // projectless issue: original Workspace boundary
		}
		op := r2dIssueOperation(r)
		ownerWorkspaceID, allowed, exists, err := r2dAuthorizeProject(queries, r, userID, target.ProjectID, op)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to authorize issue")
			return true
		}
		if !exists || !allowed {
			_, readable, _, readErr := r2dAuthorizeProject(queries, r, userID, target.ProjectID, r2dauth.OperationRead)
			if readErr != nil {
				writeError(w, http.StatusInternalServerError, "failed to authorize issue")
				return true
			}
			if !readable {
				writeError(w, http.StatusNotFound, "issue not found")
			} else {
				writeError(w, http.StatusForbidden, "insufficient project permissions")
			}
			return true
		}
		if ownerWorkspaceID != target.WorkspaceID {
			// Project issues are expected to remain in the Project owner's
			// Workspace. Refuse corrupt/mismatched rows rather than widening scope.
			writeError(w, http.StatusNotFound, "issue not found")
			return true
		}
		ctx := SetWorkspaceIDContext(r.Context(), target.WorkspaceID)
		next.ServeHTTP(w, r.WithContext(ctx))
		return true
	}

	// Project-scoped issue list. This is the one collection path we can safely
	// widen without replacing the upstream query because ListIssues already
	// applies project_id in SQL.
	if path == "/api/issues" && r.Method == http.MethodGet {
		projectID := strings.TrimSpace(r.URL.Query().Get("project_id"))
		if projectID == "" {
			return false
		}
		if _, err := parseR2DUUID(projectID); err != nil {
			return false
		}
		ownerWorkspaceID, allowed, exists, err := r2dAuthorizeProject(queries, r, userID, projectID, r2dauth.OperationRead)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to authorize project")
			return true
		}
		if !exists || !allowed {
			writeError(w, http.StatusNotFound, "project not found")
			return true
		}
		ctx := SetWorkspaceIDContext(r.Context(), ownerWorkspaceID)
		next.ServeHTTP(w, r.WithContext(ctx))
		return true
	}

	return false
}

func r2dExplicitProjectGrant(role string) bool {
	switch r2dauth.ProjectRole(role) {
	case r2dauth.ProjectRoleViewer, r2dauth.ProjectRoleMember, r2dauth.ProjectRoleManager:
		return true
	default:
		return false
	}
}

// r2dProjectCollectionIDs builds the Project set shown while one Workspace is
// active. Membership in another Workspace by itself is NOT collaboration: a
// foreign Project is injected only through an explicit user/workspace grant.
// global_observer is the exception by design and receives deployment-wide read.
func r2dProjectCollectionIDs(ctx context.Context, queries *db.Queries, userID, activeWorkspaceID string) ([]string, error) {
	facts, err := queries.R2DListCandidateProjectAccessFacts(ctx, userID)
	if err != nil {
		return nil, err
	}

	ids := make([]string, 0, len(facts))
	seen := make(map[string]struct{}, len(facts))
	for _, fact := range facts {
		if fact.ProjectID == "" || !r2dauth.Resolve(r2dFacts(fact)).Can(r2dauth.OperationRead) {
			continue
		}
		include := fact.OwnerWorkspaceID == activeWorkspaceID || fact.GlobalObserver
		if !include && (r2dExplicitProjectGrant(fact.DirectGrantRole) || r2dExplicitProjectGrant(fact.WorkspaceGrantRole)) {
			include = true
		}
		if !include {
			continue
		}
		if _, ok := seen[fact.ProjectID]; ok {
			continue
		}
		seen[fact.ProjectID] = struct{}{}
		ids = append(ids, fact.ProjectID)
	}
	return ids, nil
}

func writeR2DJSON(w http.ResponseWriter, status int, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	body = append(body, '\n')
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	_, err = w.Write(body)
	return err
}

func r2dRawProjectRows(rows []string) ([]json.RawMessage, error) {
	out := make([]json.RawMessage, 0, len(rows))
	for _, row := range rows {
		raw := json.RawMessage(row)
		if !json.Valid(raw) {
			return nil, errors.New("invalid project json")
		}
		out = append(out, raw)
	}
	return out, nil
}

// serveR2DProjectCollection is the ACL-native Project discovery path. Unlike
// the P04-A response filter it starts from the complete authorized Project set,
// so explicitly shared foreign Projects actually appear in list/search.
func serveR2DProjectCollection(queries *db.Queries, w http.ResponseWriter, r *http.Request, userID string) error {
	activeWorkspaceID := WorkspaceIDFromContext(r.Context())
	if activeWorkspaceID == "" {
		return errors.New("missing active workspace context")
	}
	projectIDs, err := r2dProjectCollectionIDs(r.Context(), queries, userID, activeWorkspaceID)
	if err != nil {
		return err
	}

	path := strings.TrimSuffix(r.URL.Path, "/")
	if path == "/api/projects" {
		rows, err := queries.R2DListProjectsJSON(
			r.Context(), projectIDs,
			strings.TrimSpace(r.URL.Query().Get("status")),
			strings.TrimSpace(r.URL.Query().Get("priority")),
		)
		if err != nil {
			return err
		}
		projects, err := r2dRawProjectRows(rows)
		if err != nil {
			return err
		}
		return writeR2DJSON(w, http.StatusOK, map[string]any{
			"projects": projects,
			"total":    len(projects),
		})
	}

	if path != "/api/projects/search" {
		return errors.New("unsupported project collection path")
	}
	query := r.URL.Query().Get("q")
	if query == "" {
		writeError(w, http.StatusBadRequest, "q parameter is required")
		return nil
	}
	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if value, parseErr := strconv.Atoi(raw); parseErr == nil && value > 0 {
			limit = value
		}
	}
	if limit > 50 {
		limit = 50
	}
	offset := 0
	if raw := r.URL.Query().Get("offset"); raw != "" {
		if value, parseErr := strconv.Atoi(raw); parseErr == nil && value >= 0 {
			offset = value
		}
	}
	rows, err := queries.R2DSearchProjectsJSON(
		r.Context(), projectIDs, query,
		r.URL.Query().Get("include_closed") == "true",
		limit, offset,
	)
	if err != nil {
		return err
	}
	projects, err := r2dRawProjectRows(rows)
	if err != nil {
		return err
	}
	return writeR2DJSON(w, http.StatusOK, map[string]any{"projects": projects})
}

// r2dResponseBuffer captures bounded JSON collection responses so private
// Project rows can be filtered before bytes leave the server. It is used only
// for non-streaming GET list/search handlers.
type r2dResponseBuffer struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func newR2DResponseBuffer() *r2dResponseBuffer {
	return &r2dResponseBuffer{header: make(http.Header), status: http.StatusOK}
}

func (b *r2dResponseBuffer) Header() http.Header         { return b.header }
func (b *r2dResponseBuffer) WriteHeader(status int)      { b.status = status }
func (b *r2dResponseBuffer) Write(p []byte) (int, error) { return b.body.Write(p) }

func copyR2DResponse(dst http.ResponseWriter, src *r2dResponseBuffer, body []byte) {
	for k, values := range src.header {
		for _, value := range values {
			dst.Header().Add(k, value)
		}
	}
	dst.Header().Del("Content-Length")
	dst.Header().Set("Content-Length", itoaR2D(len(body)))
	dst.WriteHeader(src.status)
	_, _ = dst.Write(body)
}

// serveR2DFilteredCollection uses an ACL-native Project discovery path and
// keeps P04-A's response filtering only for Issue list/search until P04-C
// replaces those workspace-scoped queries as well.
func serveR2DFilteredCollection(queries *db.Queries, w http.ResponseWriter, r *http.Request, next http.Handler, userID string) {
	path := strings.TrimSuffix(r.URL.Path, "/")
	if path == "/api/projects" || path == "/api/projects/search" {
		if err := serveR2DProjectCollection(queries, w, r, userID); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to apply project visibility")
		}
		return
	}

	buf := newR2DResponseBuffer()
	next.ServeHTTP(buf, r)
	if buf.status < 200 || buf.status >= 300 {
		copyR2DResponse(w, buf, buf.body.Bytes())
		return
	}

	key := ""
	switch {
	case path == "/api/issues" || path == "/api/issues/search":
		key = "issues"
	case path == "/api/issues/query" && r.Method == http.MethodPost:
		openOnly, valid := r2dQueryIssuesOpenOnly(r)
		if !valid || !openOnly {
			copyR2DResponse(w, buf, buf.body.Bytes())
			return
		}
		key = "issues"
	default:
		copyR2DResponse(w, buf, buf.body.Bytes())
		return
	}

	filtered, err := filterR2DCollectionJSON(r.Context(), queries, userID, key, buf.body.Bytes())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to apply project visibility")
		return
	}
	copyR2DResponse(w, buf, filtered)
}

func filterR2DCollectionJSON(ctx context.Context, queries *db.Queries, userID, key string, body []byte) ([]byte, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, err
	}
	rawItems, ok := envelope[key]
	if !ok {
		return nil, errors.New("missing collection key")
	}
	var items []map[string]any
	if err := json.Unmarshal(rawItems, &items); err != nil {
		return nil, err
	}

	projectIDs := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		projectID := ""
		if key == "projects" {
			projectID, _ = item["id"].(string)
		} else if value, ok := item["project_id"].(string); ok {
			projectID = value
		}
		if projectID == "" {
			continue
		}
		if _, ok := seen[projectID]; ok {
			continue
		}
		seen[projectID] = struct{}{}
		projectIDs = append(projectIDs, projectID)
	}

	facts, err := queries.R2DListProjectAccessFacts(ctx, userID, projectIDs)
	if err != nil {
		return nil, err
	}
	readable := make(map[string]bool, len(facts))
	for _, fact := range facts {
		readable[fact.ProjectID] = r2dauth.Resolve(r2dFacts(fact)).Can(r2dauth.OperationRead)
	}

	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		projectID := ""
		if key == "projects" {
			projectID, _ = item["id"].(string)
		} else if value, ok := item["project_id"].(string); ok {
			projectID = value
		}
		if key == "issues" && projectID == "" {
			// Projectless rows are already protected by the successful Workspace
			// membership check surrounding this filter.
			out = append(out, item)
			continue
		}
		if projectID != "" && readable[projectID] {
			out = append(out, item)
		}
	}

	envelope[key], err = json.Marshal(out)
	if err != nil {
		return nil, err
	}
	if _, ok := envelope["total"]; ok {
		envelope["total"], _ = json.Marshal(len(out))
	}
	return json.Marshal(envelope)
}

func shouldR2DFilterCollection(r *http.Request) bool {
	path := strings.TrimSuffix(r.URL.Path, "/")
	if r.Method == http.MethodGet {
		return path == "/api/projects" || path == "/api/projects/search" || path == "/api/issues" || path == "/api/issues/search"
	}
	if r.Method == http.MethodPost && path == "/api/issues/query" {
		openOnly, valid := r2dQueryIssuesOpenOnly(r)
		return valid && openOnly
	}
	return false
}

// Tiny helpers keep this file independent of handler/util packages and avoid
// an import cycle (handler already imports middleware).
func parseR2DUUID(value string) ([16]byte, error) {
	var out [16]byte
	value = strings.ReplaceAll(value, "-", "")
	if len(value) != 32 {
		return out, errors.New("invalid uuid")
	}
	for i := 0; i < 16; i++ {
		var b byte
		for j := 0; j < 2; j++ {
			c := value[i*2+j]
			b <<= 4
			switch {
			case c >= '0' && c <= '9':
				b |= c - '0'
			case c >= 'a' && c <= 'f':
				b |= c - 'a' + 10
			case c >= 'A' && c <= 'F':
				b |= c - 'A' + 10
			default:
				return out, errors.New("invalid uuid")
			}
		}
		out[i] = b
	}
	return out, nil
}

func itoaR2D(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

var _ io.Writer = (*r2dResponseBuffer)(nil)
