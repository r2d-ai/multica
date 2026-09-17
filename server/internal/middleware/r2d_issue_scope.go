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
// lookup. It owns body-aware mutation authorization and the POST /query twin
// of a Project-filtered issue list. Task-token actors stay on the legacy
// Workspace boundary until P06 defines Agent/Squad Project execution policy.
func tryR2DIssueSpecialScope(queries *db.Queries, w http.ResponseWriter, r *http.Request, next http.Handler, userID string) bool {
	if r.Header.Get("X-Actor-Source") == "task_token" {
		return false
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
	return r2dHandleDirectIssueMutation(queries, w, r, next, userID, issueID)
}

func r2dReadJSONFields(r *http.Request) (map[string]json.RawMessage, error) {
	if r.Body == nil {
		return nil, errors.New("missing request body")
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, r2dIssueBodyLimit+1))
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

func r2dLoadWorkspaceMember(queries *db.Queries, r *http.Request, userID, workspaceID string) (db.Member, bool, error) {
	userUUID, err := util.ParseUUID(userID)
	if err != nil {
		return db.Member{}, false, err
	}
	workspaceUUID, err := util.ParseUUID(workspaceID)
	if err != nil {
		return db.Member{}, false, err
	}
	member, err := queries.GetMemberByUserAndWorkspace(r.Context(), db.GetMemberByUserAndWorkspaceParams{
		UserID: userUUID, WorkspaceID: workspaceUUID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Member{}, false, nil
	}
	if err != nil {
		return db.Member{}, false, err
	}
	return member, true, nil
}

func r2dServeAuthorizedWorkspace(w http.ResponseWriter, r *http.Request, next http.Handler, workspaceID string, member db.Member, hasMember bool) {
	ctx := SetWorkspaceIDContext(r.Context(), workspaceID)
	if hasMember {
		ctx = SetMemberContext(r.Context(), workspaceID, member)
	}
	next.ServeHTTP(w, r.WithContext(ctx))
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
	for _, key := range []string{
		"assignee_type", "assignee_id", "attachment_ids", "label_ids",
		"origin_type", "origin_id",
	} {
		if raw, ok := fields[key]; ok && !r2dRawNull(raw) {
			return true
		}
	}
	return false
}

// r2dAssigneeFieldDecision reports whether a non-member may submit these
// fields. A member-type assignee is allowed only when the target user is
// assignable on the Project; Agent/Squad inventory and attachment/label/origin
// fields stay Workspace-member-only. The second return value reports whether
// the caller must be answered 403.
func r2dAssigneeFieldDecision(fields map[string]json.RawMessage, assignable bool) (bool, bool) {
	rawType, hasType := fields["assignee_type"]
	rawID, hasID := fields["assignee_id"]
	typeNull := !hasType || r2dRawNull(rawType)
	idNull := !hasID || r2dRawNull(rawID)
	if !(typeNull && idNull) {
		var assigneeType string
		if hasType && !r2dRawNull(rawType) {
			if err := json.Unmarshal(rawType, &assigneeType); err != nil {
				return false, true
			}
		}
		if assigneeType != "member" || !assignable {
			return false, true
		}
	}
	for _, key := range []string{"attachment_ids", "label_ids", "origin_type", "origin_id"} {
		if raw, ok := fields[key]; ok && !r2dRawNull(raw) {
			return false, true
		}
	}
	return true, false
}

// r2dAssigneeAssignableFor resolves whether the submitted assignee_id is
// assignable on projectID. A missing, cleared, malformed, or non-member
// assignee resolves to false so the caller's decision helper rejects it.
func r2dAssigneeAssignableFor(queries *db.Queries, r *http.Request, projectID string, fields map[string]json.RawMessage) (bool, error) {
	if projectID == "" {
		return false, nil
	}
	rawAssignee, ok := fields["assignee_id"]
	if !ok || r2dRawNull(rawAssignee) {
		return false, nil
	}
	assigneeID, err := r2dRawUUID(rawAssignee)
	if err != nil || assigneeID == "" {
		return false, nil
	}
	return queries.R2DIsAssignableMember(r.Context(), projectID, assigneeID)
}

func r2dHandleIssueCreateScope(queries *db.Queries, w http.ResponseWriter, r *http.Request, next http.Handler, userID string) bool {
	fields, err := r2dReadJSONFields(r)
	if err != nil {
		return false // preserve the handler's canonical malformed-body response
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

	member, isMember, err := r2dLoadWorkspaceMember(queries, r, userID, ownerWorkspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to authorize workspace")
		return true
	}
	if !isMember {
		// A Project grant never grants Workspace-owned Agent/Squad/label/file
		// inventory, nor trusted task/origin provenance. A member-type assignee
		// is allowed when the target user is assignable on this Project — which
		// includes another Workspace's grant holders.
		assignable, assignErr := r2dAssigneeAssignableFor(queries, r, projectID, fields)
		if assignErr != nil {
			writeError(w, http.StatusInternalServerError, "failed to authorize assignee")
			return true
		}
		if _, forbidden := r2dAssigneeFieldDecision(fields, assignable); forbidden {
			writeError(w, http.StatusForbidden, "project access does not grant workspace resource access")
			return true
		}
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

	// Destination ACL is already authoritative. Dispatch directly instead of
	// falling through the temporary fail-closed collection guard.
	r2dServeAuthorizedWorkspace(w, r, next, ownerWorkspaceID, member, isMember)
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
		_, member, err := r2dLoadWorkspaceMember(queries, r, userID, workspaceID)
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
	if requireSameProject && target.ProjectID != effectiveProjectID {
		writeError(w, http.StatusForbidden, "cross-project parent relationship requires workspace membership")
		return true
	}
	return false
}

func r2dGuardMoveAnchorReference(queries *db.Queries, w http.ResponseWriter, r *http.Request, userID, anchorID, workspaceID, effectiveProjectID string, isMember bool) bool {
	target, err := queries.R2DLoadIssueACLTarget(r.Context(), anchorID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusBadRequest, "move anchor not found in this workspace")
		return true
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to authorize move anchor")
		return true
	}
	if target.WorkspaceID != workspaceID {
		writeError(w, http.StatusBadRequest, "move anchor not found in this workspace")
		return true
	}
	if target.ProjectID == "" {
		if !isMember {
			writeError(w, http.StatusForbidden, "workspace membership required for projectless move anchor")
			return true
		}
		return false
	}
	_, _, handled := r2dRequireProjectOperation(queries, w, r, userID, target.ProjectID, r2dauth.OperationRead)
	if handled {
		return true
	}
	if !isMember && target.ProjectID != effectiveProjectID {
		writeError(w, http.StatusForbidden, "cross-project move anchor requires workspace membership")
		return true
	}
	return false
}

// r2dHandleDirectIssueMutation authorizes the source before inspecting any
// destination references. That ordering prevents hidden source IDs from being
// used as an oracle for Project/parent/anchor metadata.
func r2dHandleDirectIssueMutation(queries *db.Queries, w http.ResponseWriter, r *http.Request, next http.Handler, userID, issueID string) bool {
	fields, err := r2dReadJSONFields(r)
	if err != nil {
		return false // source authorization continues in the legacy path
	}
	target, err := queries.R2DLoadIssueACLTarget(r.Context(), issueID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to authorize issue")
		return true
	}

	member, isMember, err := r2dLoadWorkspaceMember(queries, r, userID, target.WorkspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to authorize workspace")
		return true
	}
	if target.ProjectID != "" {
		sourceWorkspaceID, _, handled := r2dRequireProjectOperation(queries, w, r, userID, target.ProjectID, r2dauth.OperationContribute)
		if handled {
			return true
		}
		if sourceWorkspaceID != target.WorkspaceID {
			writeError(w, http.StatusNotFound, "issue not found")
			return true
		}
	} else if !isMember {
		// Projectless issue existence is Workspace-private. Let the normal member
		// gate return its non-disclosing Workspace not-found response.
		return false
	}

	if !isMember {
		assignable, assignErr := r2dAssigneeAssignableFor(queries, r, target.ProjectID, fields)
		if assignErr != nil {
			writeError(w, http.StatusInternalServerError, "failed to authorize assignee")
			return true
		}
		if _, forbidden := r2dAssigneeFieldDecision(fields, assignable); forbidden {
			writeError(w, http.StatusForbidden, "project access does not grant workspace resource access")
			return true
		}
	}

	effectiveProjectID := target.ProjectID
	if rawProject, touched := fields["project_id"]; touched {
		if r2dRawNull(rawProject) {
			effectiveProjectID = ""
			if target.ProjectID != "" && !isMember {
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
			if destinationWorkspaceID != target.WorkspaceID {
				writeError(w, http.StatusBadRequest, "project not found in this workspace")
				return true
			}
			effectiveProjectID = destinationProjectID
		}
	}

	if rawParent, touched := fields["parent_issue_id"]; touched && !r2dRawNull(rawParent) {
		parentID, parseErr := r2dRawUUID(rawParent)
		if parseErr != nil {
			writeError(w, http.StatusBadRequest, "invalid parent_issue_id")
			return true
		}
		if r2dGuardParentReference(queries, w, r, userID, parentID, target.WorkspaceID, effectiveProjectID, !isMember) {
			return true
		}
	}
	for _, key := range []string{"before_id", "after_id"} {
		rawAnchor, touched := fields[key]
		if !touched || r2dRawNull(rawAnchor) {
			continue
		}
		anchorID, parseErr := r2dRawUUID(rawAnchor)
		if parseErr != nil {
			writeError(w, http.StatusBadRequest, "invalid "+key)
			return true
		}
		if r2dGuardMoveAnchorReference(queries, w, r, userID, anchorID, target.WorkspaceID, effectiveProjectID, isMember) {
			return true
		}
	}

	r2dServeAuthorizedWorkspace(w, r, next, target.WorkspaceID, member, isMember)
	return true
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
	member, isMember, err := r2dLoadWorkspaceMember(queries, r, userID, ownerWorkspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to authorize workspace")
		return true
	}
	r2dServeAuthorizedWorkspace(w, r, next, ownerWorkspaceID, member, isMember)
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
			// Do not expose that all supplied IDs exist across multiple tenants.
			writeError(w, http.StatusNotFound, "issue not found")
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

	member, isMember, err := r2dLoadWorkspaceMember(queries, r, userID, workspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to authorize workspace")
		return true
	}
	if hasProjectless && !isMember {
		writeError(w, http.StatusNotFound, "issue not found")
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
				writeError(w, http.StatusNotFound, "issue not found")
			}
			return true
		}
	}

	if update {
		rawUpdates, ok := fields["updates"]
		if ok && !r2dRawNull(rawUpdates) {
			var updates map[string]json.RawMessage
			if json.Unmarshal(rawUpdates, &updates) == nil {
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
				if !isMember {
					// A member-type assignee is allowed when the target user is
					// assignable on the (single) destination Project; a
					// multi-Project batch keeps the Workspace boundary.
					assignable, assignErr := r2dAssigneeAssignableFor(queries, r, effectiveProjectID, updates)
					if assignErr != nil {
						writeError(w, http.StatusInternalServerError, "failed to authorize assignee")
						return true
					}
					if _, forbidden := r2dAssigneeFieldDecision(updates, assignable); forbidden {
						writeError(w, http.StatusForbidden, "project access does not grant workspace resource access")
						return true
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

	// Source and destination ACLs have already been checked in batches. Direct
	// dispatch avoids the temporary fail-closed collection guard while retaining
	// the real member context for normal owner-Workspace callers.
	r2dServeAuthorizedWorkspace(w, r, next, workspaceID, member, isMember)
	return true
}
