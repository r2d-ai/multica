package middleware

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/multica-ai/multica/server/internal/r2dauth"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// filterR2DIssueCollectionScoped filters an {issues:[...]} response in one
// batched Project-facts read. allowProjectless must only be true after Workspace
// membership (or global-observer) authorization. exactProjectID optionally
// narrows the result to one explicitly authorized Project, which is useful for
// cross-workspace search where the upstream handler is Workspace-scoped.
func filterR2DIssueCollectionScoped(ctx context.Context, queries *db.Queries, userID string, body []byte, allowProjectless bool, exactProjectID string) ([]byte, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, err
	}
	rawItems, ok := envelope["issues"]
	if !ok {
		return nil, errors.New("missing issues collection")
	}
	var items []map[string]any
	if err := json.Unmarshal(rawItems, &items); err != nil {
		return nil, err
	}

	projectIDs := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		projectID, _ := item["project_id"].(string)
		if projectID == "" {
			continue
		}
		if exactProjectID != "" && projectID != exactProjectID {
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
		projectID, _ := item["project_id"].(string)
		if projectID == "" {
			if allowProjectless && exactProjectID == "" {
				out = append(out, item)
			}
			continue
		}
		if exactProjectID != "" && projectID != exactProjectID {
			continue
		}
		if readable[projectID] {
			out = append(out, item)
		}
	}

	envelope["issues"], err = json.Marshal(out)
	if err != nil {
		return nil, err
	}
	if _, ok := envelope["total"]; ok {
		envelope["total"], _ = json.Marshal(len(out))
	}
	return json.Marshal(envelope)
}
