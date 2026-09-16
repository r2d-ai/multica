package r2dauth

import (
	"reflect"
	"strings"
	"testing"
)

// P07-F: Workspace MCP inventory never crosses the Project decision boundary.
//
// The R2D Project ACL decides access to a shared Project's content. Workspace
// MCP inventory — the shared server library (whose entries routinely embed
// bearer credentials), the per-agent bindings, and the daemon overlay — is
// Workspace-owned configuration, not Project content. Keeping it out of the
// decision/fact structs and out of the authorization SQL is what makes "a
// Project grant never widens MCP access" a property of the policy layer instead
// of a promise each handler has to keep.
//
// These allowlists are security contracts. Adding a field to any of these
// structs can hand a cross-Workspace Project caller a view of the owner
// Workspace's MCP configuration; update this test deliberately, never
// incidentally.

func TestP07FMcpInventoryDoesNotCrossProjectDecisionBoundary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		value  any
		fields []string
	}{
		{
			name:  "project facts carry roles and workspace identity only",
			value: ProjectFacts{},
			fields: []string{
				"ProjectID", "OwnerWorkspaceID", "Visibility", "OwnerWorkspaceRole",
				"DirectGrantRole", "WorkspaceGrantRole", "GlobalObserver",
			},
		},
		{
			name:  "project decision carries role and observer bit only",
			value: Decision{},
			fields: []string{
				"Role", "GlobalObserver",
			},
		},
		{
			name:  "project capabilities carry authorization flags only",
			value: ProjectCapabilities{},
			fields: []string{
				"Role", "GlobalObserver", "Read", "Contribute", "Manage", "Share", "ViewResources",
			},
		},
	}

	// Substrings that mark a Workspace-inventory field. `ViewResources` is the
	// one capability that deliberately reads owner-Workspace execution metadata
	// (repo URLs, daemon ids), and it is already restricted to human owner
	// Workspace members by ResolveCapabilities — it must stay a boolean, never
	// a carrier for the metadata itself.
	forbidden := []string{
		"mcp", "secret", "credential", "config", "runtime", "integration",
		"token", "vcs", "repo", "env", "overlay",
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			typ := reflect.TypeOf(tt.value)
			exported := make([]string, 0, typ.NumField())
			for i := 0; i < typ.NumField(); i++ {
				field := typ.Field(i)
				if field.PkgPath != "" {
					continue // unexported
				}
				exported = append(exported, field.Name)
				lower := strings.ToLower(field.Name)
				for _, bad := range forbidden {
					if strings.Contains(lower, bad) {
						t.Fatalf("field %q exposes Workspace inventory %q across the Project boundary", field.Name, bad)
					}
				}
			}
			if !reflect.DeepEqual(exported, tt.fields) {
				t.Fatalf("exported fields = %v, want %v; Workspace MCP inventory must not cross the authorization boundary", exported, tt.fields)
			}
		})
	}
}

// TestP07FAuthorizationQueriesDoNotLoadMcpInventory extends the P06 SQL audit to
// the MCP tables. Every R2D authorization fact query must resolve Project access
// from Project/Workspace membership and grants only — never by joining the
// Workspace MCP library or agent MCP bindings.
func TestP07FAuthorizationQueriesDoNotLoadMcpInventory(t *testing.T) {
	t.Parallel()

	for name, query := range map[string]string{
		"project facts":           projectFactsSQL,
		"candidate project facts": candidateProjectFactsSQL,
		"agent dispatch":          agentDispatchFactsSQL,
		"task execution":          taskExecutionFactsSQL,
		"task resource":           taskResourceFactsSQL,
		"squad dispatch":          squadDispatchFactsSQL,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			lower := strings.ToLower(query)
			for _, forbidden := range []string{
				"workspace_mcp", "agent_mcp", "mcp_server",
				"runtime_config", "runtime_mcp_overlay", "custom_env", "custom_args",
				"secret", "credential", "integration", "vcs_connection",
			} {
				if strings.Contains(lower, forbidden) {
					t.Fatalf("authorization query contains Workspace inventory source %q", forbidden)
				}
			}
		})
	}
}
