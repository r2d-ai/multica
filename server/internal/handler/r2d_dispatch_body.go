package handler

import (
	"encoding/json"
	"net/http"
)

// decodeJSONBody is the shared body decoder for the R2D dispatch endpoints.
// P06-B added the first caller (r2d_agent_dispatch.go) without the helper, which
// broke `go build ./internal/handler`. Keeping the tiny helper here lets both
// the P06-B Agent boundary and the P06-C Squad boundary share one decode rule
// without editing the already-merged P06-B file.
func decodeJSONBody(r *http.Request, dst any) error {
	return json.NewDecoder(r.Body).Decode(dst)
}
