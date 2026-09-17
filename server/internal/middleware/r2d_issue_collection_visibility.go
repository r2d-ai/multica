package middleware

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/multica-ai/multica/server/internal/r2dauth"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const r2dNoProjectUUID = "00000000-0000-0000-0000-000000000000"

// r2dReadableIssueProjectIDs returns the readable Projects an issue collection
// unions while activeWorkspaceID is active: Workspace-owned Projects, foreign
// Projects with an explicit grant, and a global observer's readable set. SQL
// supplies facts only; r2dauth.ProjectIDsForIssueCollection is the policy.
func r2dReadableIssueProjectIDs(queries *db.Queries, r *http.Request, userID, activeWorkspaceID string) ([]string, error) {
	facts, err := queries.R2DListCandidateProjectAccessFacts(r.Context(), userID)
	if err != nil {
		return nil, err
	}
	policyFacts := make([]r2dauth.ProjectFacts, 0, len(facts))
	for _, fact := range facts {
		policyFacts = append(policyFacts, r2dFacts(fact))
	}
	return r2dauth.ProjectIDsForIssueCollection(policyFacts, activeWorkspaceID), nil
}

func r2dIntersectProjectIDs(raw string, readable []string) []string {
	allowed := make(map[string]struct{}, len(readable))
	for _, id := range readable {
		allowed[id] = struct{}{}
	}
	seen := make(map[string]struct{})
	out := make([]string, 0)
	for _, part := range strings.Split(raw, ",") {
		id := strings.TrimSpace(part)
		if id == "" {
			continue
		}
		if _, ok := allowed[id]; !ok {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// r2dApplyReadableProjectValues constrains an upstream Issue query without
// changing its public filter semantics:
//   - an explicit project_ids filter is intersected with readable Projects;
//   - otherwise the complete readable Project set is injected and projectless
//     Issues are included because the caller has already passed Workspace
//     membership;
//   - an empty intersection uses a valid zero UUID so "no readable Project"
//     never degrades into upstream's "no project filter" behavior.
func r2dApplyReadableProjectValues(values url.Values, readable []string) {
	if raw, present := values["project_ids"]; present && strings.TrimSpace(strings.Join(raw, ",")) != "" {
		ids := r2dIntersectProjectIDs(strings.Join(raw, ","), readable)
		if len(ids) == 0 {
			values.Set("project_ids", r2dNoProjectUUID)
		} else {
			values.Set("project_ids", strings.Join(ids, ","))
		}
		// Preserve include_no_project: it is an explicit caller filter when
		// project_ids was supplied.
		return
	}

	if len(readable) == 0 {
		values.Set("project_ids", r2dNoProjectUUID)
	} else {
		values.Set("project_ids", strings.Join(readable, ","))
	}
	values.Set("include_no_project", "true")
}

func r2dIssueCollectionNeedsVisibility(r *http.Request) bool {
	path := strings.TrimSuffix(r.URL.Path, "/")
	if r.Method == http.MethodGet {
		return path == "/api/issues" || path == "/api/issues/grouped"
	}
	return r.Method == http.MethodPost && path == "/api/issues/query"
}

// r2dApplyIssueCollectionVisibility rewrites only request filters. Existing
// handlers therefore apply Project ACL inside their own SQL window before
// pagination/count/grouping, without forking the high-churn Issue handlers.
// POST /query is its GET twin, so the same project_ids/include_no_project
// contract is rewritten in its map[string]string body.
func r2dApplyIssueCollectionVisibility(queries *db.Queries, r *http.Request, userID, workspaceID string) error {
	if !r2dIssueCollectionNeedsVisibility(r) {
		return nil
	}
	readable, err := r2dReadableIssueProjectIDs(queries, r, userID, workspaceID)
	if err != nil {
		return err
	}

	if r.Method == http.MethodGet {
		values := r.URL.Query()
		r2dApplyReadableProjectValues(values, readable)
		r.URL.RawQuery = values.Encode()
		return nil
	}

	fields, err := r2dReadJSONFields(r)
	if err != nil {
		// Keep the handler's canonical malformed-body response. The fail-closed
		// guard will still block this request if hidden Projects exist.
		return nil
	}
	params := make(map[string]string, len(fields)+2)
	for key, raw := range fields {
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil // QueryIssues owns validation of its string-only contract.
		}
		params[key] = value
	}
	values := make(url.Values, len(params)+2)
	for key, value := range params {
		values.Set(key, value)
	}
	r2dApplyReadableProjectValues(values, readable)
	params = make(map[string]string, len(values))
	for key := range values {
		params[key] = values.Get(key)
	}
	body, err := json.Marshal(params)
	if err != nil {
		return err
	}
	r.Body = http.NoBody
	if len(body) > 0 {
		r.Body = ioNopCloserR2D(bytes.NewReader(body))
	}
	r.ContentLength = int64(len(body))
	return nil
}

// Local wrapper avoids importing io solely for io.NopCloser in this small
// middleware extension.
type r2dReadCloser struct{ *bytes.Reader }

func (r2dReadCloser) Close() error { return nil }

func ioNopCloserR2D(r *bytes.Reader) r2dReadCloser { return r2dReadCloser{Reader: r} }
