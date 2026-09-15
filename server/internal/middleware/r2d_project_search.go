package middleware

import (
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

// r2dServeProjectSearch adapts the upstream Workspace-scoped search into an
// explicitly Project-scoped read without copying its ranking SQL. Filtering a
// single upstream page would make hidden/unrelated Workspace rows displace the
// requested Project's results, so scan bounded 50-row windows until the
// Project-relative offset+limit is satisfied. Filtering preserves the upstream
// total ordering because it only removes rows; it never re-ranks them.
func r2dServeProjectSearch(queries *db.Queries, w http.ResponseWriter, r *http.Request, next http.Handler, userID, projectID string) bool {
	ownerWorkspaceID, _, handled := r2dRequireProjectOperation(
		queries, w, r, userID, projectID, r2dauth.OperationRead,
	)
	if handled {
		return true
	}

	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if value, err := strconv.Atoi(raw); err == nil && value > 0 {
			limit = value
		}
	}
	if limit > 50 {
		limit = 50
	}
	offset := 0
	if raw := r.URL.Query().Get("offset"); raw != "" {
		if value, err := strconv.Atoi(raw); err == nil && value >= 0 {
			offset = value
		}
	}
	need := offset + limit
	if need > r2dProjectSearchMaxScan {
		writeError(w, http.StatusBadRequest, "project search offset is too large; refine the query")
		return true
	}

	matches := make([]json.RawMessage, 0, need)
	var last *r2dResponseBuffer
	for scanOffset := 0; scanOffset < r2dProjectSearchMaxScan && len(matches) < need; scanOffset += r2dProjectSearchChunk {
		page := r.Clone(SetWorkspaceIDContext(r.Context(), ownerWorkspaceID))
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
		for _, raw := range envelope.Issues {
			var identity struct {
				ProjectID *string `json:"project_id"`
			}
			if json.Unmarshal(raw, &identity) != nil || identity.ProjectID == nil || *identity.ProjectID != projectID {
				continue
			}
			matches = append(matches, raw)
		}
		if len(envelope.Issues) < r2dProjectSearchChunk {
			break
		}
	}

	if len(matches) < need && last != nil {
		// If the final scanned Workspace page was full, more results may exist
		// beyond the bounded scan. Do not silently claim an incomplete Project
		// page; ask the caller to narrow the text search instead.
		var envelope struct {
			Issues []json.RawMessage `json:"issues"`
		}
		if json.Unmarshal(last.body.Bytes(), &envelope) == nil && len(envelope.Issues) == r2dProjectSearchChunk {
			writeError(w, http.StatusServiceUnavailable, "project search scope is too broad; refine the query")
			return true
		}
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
