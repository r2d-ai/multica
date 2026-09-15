package middleware

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/r2dauth"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const r2dIssueBodyLimit = 2 << 20

// tryR2DIssueSpecialScope runs before the ordinary Workspace membership
// lookup. It handles the mutation cases where the destination Project is in the
// request body, plus the POST /query twin of a Project-filtered issue list.
// Entity source authorization remains in tryR2DProjectScope; this layer closes
// destination/batch escalation without duplicating that policy.
func tryR2DIssueSpecialScope(queries *db.Queries, w http.ResponseWriter, r *http.Request, next http.Handler, userID string) bool {
	if r.Header.Get("X-Actor-Source") == "task_token" {
		return false // Agent/Squad execution policy remains P06.
	}

	path := strings.TrimSuffix(r.URL.Path, "/")
	switch {
	case path == "/api/issues" && r.Method == http.MethodPost:
		return r2dHandleIssueCreateScope(queries, w, r, next, userID)
	case path == "/api/issues/query" && r.Method == http.MethodPost:
		return r2dHandleProjectFilteredIssueQuery(queries, w, r, next, userID)
	case path == "/api/issues/batch-update" && r.Method == http.MethodPost:
		return r2dHandleIssueBatchScope(queries, w, r, next, userID, true)
	case path == "/api/issues/batch-delete" && r.Method == http.MethodPost:
		return r2dHandleIssueBatchScope(queries, w, r, next, userID, false)
	}

	if !strings.HasPrefix(path, "/api/issues/") {
		return false
	}
	rest := strings.TrimPrefix(path, "/api/issues/")
	parts := strings.SplitN(rest, "/", 2)
	issueID := parts[0]
	if _, err := parseR2DUUID(issueID); err != nil {
		return false
	}
	suffix := ""
	if len(parts) == 2 {
		suffix = "/" + parts[1]
	}
	isUpdate := (r.Method == http.MethodPut || r.Method == http.MethodPatch) && suffix == ""
	isMove := r.Method == http.MethodPost && suffix == "/move"
	if !isUpdate && !isMove {
		return false
	}

	blocked, err := r2dGuardDirectIssueDestination(queries, w, r, userID, issueID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to authorize issue destination")
		return true
	}
	return blocked
}

func r2dReadJSONFields(r *http.Request) (map[string]json.RawMessage, error) {
	if r.Body == nil {
		return nil, errors.New("missing request body")
	}
	limited := io.LimitReader(r.Body, r2dIssueBodyLimit+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	if len(body) > r2dIssueBodyLimit {
		return nil, errors.New("request body too large")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil, err
	}
	return fields, nil
}

func r2dRawNull(raw json.RawMessage) bool {
	return len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func r2dRawUUID(raw json.RawMessage) (string, error) {
	if r2dRawNull(raw) {
		return "", nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil || strings.TrimSpace(value) == "" {
		return "", errors.New("invalid uuid")
	}
	value = strings.TrimSpace(value)
	if _, err := parseR2DUUID(value); err != nil {
		return "", err
	}
	return value, nil
}

func r2dWorkspaceMember(queries *db.Queries, r *http.Request, userID, workspaceID string) (bool, error) {
	userUUID, err := util.ParseUUID(userID)
	if err != nil {
		return false, err
	}
	workspaceUUID, err := util.ParseUUID(workspaceID)
	if err != nil {
		return false, err
	}
	_, err = queries.GetMemberByUserAndWorkspace(r.Context(), db.GetMemberByUserAndWorkspaceParams{
		UserID: userUUID, WorkspaceID: workspaceUUID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func r2dRequireProjectOperation(queries *db.Queries, w http.ResponseWriter, r *http.Request, userID, projectID string, op r2dauth.Operation) (workspaceID string, allowed bool, handled bool) {
	workspaceID, allowed, exists, err := r2dAuthorizeProject(queries, r, userID, projectID, op)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to authorize project")
		return "", false, true
	}
	if !exists {
		writeError(w, http.StatusNotFound, "project not found")
		return "", false, true
	}
	if allowed {
		return workspaceID, true, false
	}
	_, readable, _, readErr := r2dAuthorizeProject(queries, r, userID, projectID, r2dauth.OperationRead)
	if readErr != nil {
		writeError(w, http.StatusInternalServerError, "failed to authorize project")
		return "", false, true
	}
	if readable {
		writeError(w, http.StatusForbidden, "insufficient project permissions")
	} else {
		writeError(w, http.StatusNotFound, "project not found")
	}
	return "", false, true
}

func r2dForeignProjectRestrictedFields(fields map[string]json.RawMessage) bool {
	for _, key := range []string{"assignee_type", "assignee_id", "attachment_ids", "label_ids"} {
		if raw, ok := fields[key]; ok && !r2dRawNull(raw) {
			return true
		}
	}
	return false
}

func r2dHandleIssueCreateScope(queries *db.Queries, w http.ResponseWriter, r *http.Request, next http.Handler, userID string) bool {
	fields, err := r2dReadJSONFields(r)
	if err != nil {
		// Preserve the handler's normal validation/error wording.
		return false
	}
	rawProject, touched := fields["project_id"]
	if !touched || r2dRawNull(rawProject) {
		return false // Projectless create retains ordinary Workspace membership.
	}
	projectID, err := r2dRawUUID(rawProject)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid project_id")
		return true
	}
	ownerWorkspaceID, _, handled := r2dRequireProjectOperation(queries, w, r, userID, projectID, r2dauth.OperationContribute)
	if handled {
		return true
	}

	isMember, err := r2dWorkspaceMember(queries, r, userID, ownerWorkspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to authorize workspace")
		return true
	}
	if !isMember && r2dForeignProjectRestrictedFields(fields) {
		// Project grants never imply discovery/use of Workspace-owned Agents,
		// Squads, labels or attachment inventory. P06 may add a deliberately
		// scoped execution model; until then this fails closed.
		writeError(w, http.StatusForbidden, "project access does not grant workspace resource access")
		return true
	}
	if rawParent, ok := fields["parent_issue_id"]; ok && !r2dRawNull(rawParent) {
		parentID, err := r2dRawUUID(rawParent)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid parent_issue_id")
			return true
		}
		if r2dGuardParentReference(queries, w, r, userID, parentID, ownerWorkspaceID, projectID, !isMember) {
			return true
		}
	}

	if isMember {
		// Destination ACL has been checked; let the normal Workspace middleware
		// inject the real member row expected by ordinary in-workspace writes.
		return false
	}
	ctx := SetWorkspaceIDContext(r.Context(), ownerWorkspaceID)
	next.ServeHTTP(w, r.WithContext(ctx))
	return true
}

func r2dGuardParentReference(queries *db.Queries, w http.ResponseWriter, r *http.Request, userID, parentID, workspaceID, effectiveProjectID string, requireSameProject bool) bool {
	target, err := queries.R2DLoadIssueACLTarget(r.Context(), parentID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusBadRequest, "parent issue not found in this workspace")
		return true
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to authorize parent issue")
		return true
	}
	if target.WorkspaceID != workspaceID {
		writeError(w, http.StatusBadRequest, "parent issue not found in this workspace")
		return true
	}
	if target.ProjectID == "" {
		member, err := r2dWorkspaceMember(queries, r, userID, workspaceID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to authorize workspace")
			return true
		}
		if !member {
			writeError(w, http.StatusForbidden, "workspace membership required for projectless parent")
			return true
		}
		return false
	}
	_, _, handled := r2dRequireProjectOperation(queries, w, r, userID, target.ProjectID, r2dauth.OperationContribute)
	if handled {
		return true
	}
	if requireSameProject && effectiveProjectID != "" && target.ProjectID != effectiveProjectID {
		writeError(w, http.StatusForbidden, "cross-project parent relationship requires workspace membership")
		return true
	}
	return false
}

func r2dGuardDirectIssueDestination(queries *db.Queries, w http.ResponseWriter, r *http.Request, userID, issueID string) (bool, error) {
	fields, err := r2dReadJSONFields(r)
	if err != nil {
		return false, nil // handler owns malformed-body validation
	}
	target, err := queries.R2DLoadIssueACLTarget(r.Context(), issueID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil // source path will render canonical not-found
	}
	if err != nil {
		return false, err
	}
	isMember, err := r2dWorkspaceMember(queries, r, userID, target.WorkspaceID)
	if err != nil {
		return false, err
	}
	if !isMember {
		if _, ok := fields["assignee_type"]; ok {
			writeError(w, http.StatusForbidden, "project access does not grant workspace resource access")
			return true, nil
		}
		if _, ok := fields["assignee_id"]; ok {
			writeError(w, http.StatusForbidden, "project access does not grant workspace resource access")
			return true, nil
		}
	}

	effectiveProjectID := target.ProjectID
	if rawProject, touched := fields["project_id"]; touched {
		if r2dRawNull(rawProject) {
			effectiveProjectID = ""
			if target.ProjectID != "" && !isMember {
				writeError(w, http.StatusForbidden, "workspace membership required to remove issue from project")
				return true, nil
			}
		} else {
			destinationProjectID, parseErr := r2dRawUUID(rawProject)
			if parseErr != nil {
				writeError(w, http.StatusBadRequest, "invalid project_id")
				return true, nil
			}
			destinationWorkspaceID, _, handled := r2dRequireProjectOperation(queries, w, r, userID, destinationProjectID, r2dauth.OperationContribute)
			if handled {
				return true, nil
			}
			if destinationWorkspaceID != target.WorkspaceID {
				writeError(w, http.StatusBadRequest, "project not found in this workspace")
				return true, nil
			}
			effectiveProjectID = destinationProjectID
		}
	}

	if rawParent, touched := fields["parent_issue_id"]; touched && !r2dRawNull(rawParent) {
		parentID, parseErr := r2dRawUUID(rawParent)
		if parseErr != nil {
			writeError(w, http.StatusBadRequest, "invalid parent_issue_id")
			return true, nil
		}
		if r2dGuardParentReference(queries, w, r, userID, parentID, target.WorkspaceID, effectiveProjectID, !isMember) {
			return true, nil
		}
	}
	return false, nil
}

func r2dHandleProjectFilteredIssueQuery(queries *db.Queries, w http.ResponseWriter, r *http.Request, next http.Handler, userID string) bool {
	fields, err := r2dReadJSONFields(r)
	if err != nil {
		return false
	}
	rawProject, ok := fields["project_id"]
	if !ok || r2dRawNull(rawProject) {
		return false
	}
	var projectID string
	if err := json.Unmarshal(rawProject, &projectID); err != nil || strings.TrimSpace(projectID) == "" {
		return false // QueryIssues expects map[string]string; let it report 400.
	}
	projectID = strings.TrimSpace(projectID)
	if _, err := parseR2DUUID(projectID); err != nil {
		writeError(w, http.StatusBadRequest, "invalid project_id")
		return true
	}
	ownerWorkspaceID, _, handled := r2dRequireProjectOperation(queries, w, r, userID, projectID, r2dauth.OperationRead)
	if handled {
		return true
	}
	ctx := SetWorkspaceIDContext(r.Context(), ownerWorkspaceID)
	next.ServeHTTP(w, r.WithContext(ctx))
	return true
}

func r2dHandleIssueBatchScope(queries *db.Queries, w http.ResponseWriter, r *http.Request, next http.Handler, userID string, update bool) bool {
	fields, err := r2dReadJSONFields(r)
	if err != nil {
		return false
	}
	var issueIDs []string
	if raw, ok := fields["issue_ids"]; !ok || json.Unmarshal(raw, &issueIDs) != nil || len(issueIDs) == 0 {
		return false
	}
	if len(issueIDs) > 1000 {
		writeError(w, http.StatusBadRequest, "too many issue_ids")
		return true
	}
	unique := make([]string, 0, len(issueIDs))
	seen := make(map[string]struct{}, len(issueIDs))
	for _, issueID := range issueIDs {
		if _, err := parseR2DUUID(issueID); err != nil {
			writeError(w, http.StatusBadRequest, "invalid issue_id")
			return true
		}
		if _, ok := seen[issueID]; ok {
			continue
		}
		seen[issueID] = struct{}{}
		unique = append(unique, issueID)
	}
	targets, err := queries.R2DListIssueACLTargets(r.Context(), unique)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to authorize issues")
		return true
	}
	if len(targets) != len(unique) {
		writeError(w, http.StatusNotFound, "issue not found")
		return true
	}

	workspaceID := targets[0].WorkspaceID
	projectIDs := make([]string, 0)
	projectSeen := map[string]struct{}{}
	hasProjectless := false
	for _, target := range targets {
		if target.WorkspaceID != workspaceID {
			writeError(w, http.StatusBadRequest, "batch issues must belong to one workspace")
			return true
		}
		if target.ProjectID == "" {
			hasProjectless = true
			continue
		}
		if _, ok := projectSeen[target.ProjectID]; !ok {
			projectSeen[target.ProjectID] = struct{}{}
			projectIDs = append(projectIDs, target.ProjectID)
		}
	}

	isMember, err := r2dWorkspaceMember(queries, r, userID, workspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to authorize workspace")
		return true
	}
	if hasProjectless && !isMember {
		writeError(w, http.StatusForbidden, "workspace membership required for projectless issues")
		return true
	}

	facts, err := queries.R2DListProjectAccessFacts(r.Context(), userID, projectIDs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to authorize projects")
		return true
	}
	decisions := make(map[string]r2dauth.Decision, len(facts))
	for _, fact := range facts {
		decisions[fact.ProjectID] = r2dauth.Resolve(r2dFacts(fact))
	}
	for _, projectID := range projectIDs {
		decision, ok := decisions[projectID]
		if !ok || !decision.Can(r2dauth.OperationContribute) {
			if ok && decision.Can(r2dauth.OperationRead) {
				writeError(w, http.StatusForbidden, "insufficient project permissions")
			} else {
				writeError(w, http.StatusNotFound, "project not found")
			}
			return true
		}
	}

	if update {
		rawUpdates, ok := fields["updates"]
		if ok && !r2dRawNull(rawUpdates) {
			var updates map[string]json.RawMessage
			if json.Unmarshal(rawUpdates, &updates) == nil {
				if !isMember {
					if _, touched := updates["assignee_type"]; touched {
						writeError(w, http.StatusForbidden, "project access does not grant workspace resource access")
						return true
					}
					if _, touched := updates["assignee_id"]; touched {
						writeError(w, http.StatusForbidden, "project access does not grant workspace resource access")
						return true
					}
				}

				effectiveProjectID := ""
				if len(projectIDs) == 1 {
					effectiveProjectID = projectIDs[0]
				}
				if rawProject, touched := updates["project_id"]; touched {
					if r2dRawNull(rawProject) {
						effectiveProjectID = ""
						if len(projectIDs) > 0 && !isMember {
							writeError(w, http.StatusForbidden, "workspace membership required to remove issue from project")
							return true
						}
					} else {
						destinationProjectID, parseErr := r2dRawUUID(rawProject)
						if parseErr != nil {
							writeError(w, http.StatusBadRequest, "invalid project_id")
							return true
						}
						destinationWorkspaceID, _, handled := r2dRequireProjectOperation(queries, w, r, userID, destinationProjectID, r2dauth.OperationContribute)
						if handled {
							return true
						}
						if destinationWorkspaceID != workspaceID {
							writeError(w, http.StatusBadRequest, "project not found in this workspace")
							return true
						}
						effectiveProjectID = destinationProjectID
					}
				}
				if rawParent, touched := updates["parent_issue_id"]; touched && !r2dRawNull(rawParent) {
					parentID, parseErr := r2dRawUUID(rawParent)
					if parseErr != nil {
						writeError(w, http.StatusBadRequest, "invalid parent_issue_id")
						return true
					}
					if r2dGuardParentReference(queries, w, r, userID, parentID, workspaceID, effectiveProjectID, !isMember) {
						return true
					}
				}
			}
		}
	}

	ctx := SetWorkspaceIDContext(r.Context(), workspaceID)
	if isMember {
		// Let the ordinary middleware inject the actual member object. The ACL
		// checks above still protect private Projects/destination transitions.
		return false
	}
	next.ServeHTTP(w, r.WithContext(ctx))
	return true
}
