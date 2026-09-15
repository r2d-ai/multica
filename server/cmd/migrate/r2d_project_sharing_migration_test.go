package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func readR2DMigration(t *testing.T, name string) string {
	t.Helper()

	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("failed to resolve migration test path")
	}

	path := filepath.Join(filepath.Dir(testFile), "..", "..", "migrations", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration %s: %v", name, err)
	}
	return string(data)
}

func normalizeR2DSQL(sql string) string {
	return strings.Join(strings.Fields(sql), " ")
}

func TestR2DProjectSharingMigrationContract(t *testing.T) {
	up := normalizeR2DSQL(readR2DMigration(t, "900000_r2d_project_sharing.up.sql"))

	required := []string{
		"CREATE TABLE r2d_project_extra",
		"CHECK (visibility IN ('workspace', 'private'))",
		"CREATE TABLE r2d_project_grants",
		"CHECK (principal_type IN ('user', 'workspace'))",
		"CHECK (role IN ('viewer', 'member', 'manager'))",
		"UNIQUE (project_id, principal_type, principal_id)",
		"ON r2d_project_grants (principal_type, principal_id, project_id)",
		"CREATE TABLE r2d_global_roles",
		"CHECK (role = 'global_observer')",
		"ON r2d_global_roles (role, user_id)",
	}

	for _, fragment := range required {
		if !strings.Contains(up, fragment) {
			t.Errorf("migration missing required fragment %q", fragment)
		}
	}

	upper := strings.ToUpper(up)
	if strings.Contains(upper, "ALTER TABLE") {
		t.Error("R2D migration must not alter upstream-owned tables")
	}
	if strings.Contains(upper, "REFERENCES ") {
		t.Error("R2D migration must not add foreign keys into upstream-owned tables")
	}
}

func TestR2DProjectSharingDownMigrationScope(t *testing.T) {
	down := normalizeR2DSQL(readR2DMigration(t, "900000_r2d_project_sharing.down.sql"))

	for _, table := range []string{
		"r2d_global_roles",
		"r2d_project_grants",
		"r2d_project_extra",
	} {
		fragment := "DROP TABLE IF EXISTS " + table
		if !strings.Contains(down, fragment) {
			t.Errorf("down migration missing %q", fragment)
		}
	}

	if count := strings.Count(strings.ToUpper(down), "DROP TABLE IF EXISTS"); count != 3 {
		t.Errorf("down migration must drop exactly 3 R2D tables, got %d", count)
	}
	if strings.Contains(strings.ToUpper(down), "ALTER TABLE") {
		t.Error("down migration must not alter upstream-owned tables")
	}
}
