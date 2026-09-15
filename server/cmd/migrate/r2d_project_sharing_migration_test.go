package main

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
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

func TestR2DProjectSharingMigrationApplyAndRollback(t *testing.T) {
	t.Parallel()

	const version = "900000_r2d_project_sharing"
	admin := openTestPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	schema := fmt.Sprintf("r2d_project_sharing_%d_%d", time.Now().UnixNano(), rand.Uint32())
	ident := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+ident); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if _, err := admin.Exec(cleanupCtx, "DROP SCHEMA "+ident+" CASCADE"); err != nil {
			t.Errorf("cleanup schema: %v", err)
		}
	})

	pool := openTestPoolWithSearchPath(t, schema)
	if _, err := pool.Exec(ctx, `CREATE TABLE schema_migrations (
		version TEXT PRIMARY KEY,
		applied_at TIMESTAMPTZ DEFAULT now()
	)`); err != nil {
		t.Fatal(err)
	}

	options := runOptions{
		Direction:             "up",
		Files:                 realMigrationFiles(t, []string{version}, "up"),
		SchemaMigrationsTable: schema + ".schema_migrations",
		AdvisoryLockKey:       int64(rand.Uint64()&0x7fffffffffffffff) | 1,
		Hooks:                 hooksForDirection("up"),
	}
	if err := runMigrations(ctx, pool, options); err != nil {
		t.Fatal(err)
	}

	for _, table := range []string{"r2d_project_extra", "r2d_project_grants", "r2d_global_roles"} {
		var exists bool
		if err := pool.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", schema+"."+table).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Errorf("expected table %s to exist after migration", table)
		}
	}

	if _, err := pool.Exec(ctx, `INSERT INTO r2d_project_extra (project_id) VALUES ('project-1')`); err != nil {
		t.Fatalf("insert default project extra: %v", err)
	}
	var visibility string
	if err := pool.QueryRow(ctx, `SELECT visibility FROM r2d_project_extra WHERE project_id = 'project-1'`).Scan(&visibility); err != nil {
		t.Fatal(err)
	}
	if visibility != "workspace" {
		t.Fatalf("default visibility = %q, want workspace", visibility)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO r2d_project_extra (project_id, visibility) VALUES ('project-invalid', 'public')`); err == nil {
		t.Error("invalid project visibility was accepted")
	}

	if _, err := pool.Exec(ctx, `INSERT INTO r2d_project_grants
		(id, project_id, principal_type, principal_id, role, created_by)
		VALUES ('grant-1', 'project-1', 'workspace', 'workspace-2', 'member', 'user-admin')`); err != nil {
		t.Fatalf("insert valid project grant: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO r2d_project_grants
		(id, project_id, principal_type, principal_id, role, created_by)
		VALUES ('grant-duplicate', 'project-1', 'workspace', 'workspace-2', 'viewer', 'user-admin')`); err == nil {
		t.Error("duplicate project/principal grant was accepted")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO r2d_project_grants
		(id, project_id, principal_type, principal_id, role, created_by)
		VALUES ('grant-invalid-principal', 'project-1', 'team', 'team-1', 'viewer', 'user-admin')`); err == nil {
		t.Error("invalid principal type was accepted")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO r2d_project_grants
		(id, project_id, principal_type, principal_id, role, created_by)
		VALUES ('grant-invalid-role', 'project-1', 'user', 'user-1', 'owner', 'user-admin')`); err == nil {
		t.Error("invalid project role was accepted")
	}

	if _, err := pool.Exec(ctx, `INSERT INTO r2d_global_roles (user_id, role, created_by)
		VALUES ('observer-1', 'global_observer', 'user-admin')`); err != nil {
		t.Fatalf("insert global observer: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO r2d_global_roles (user_id, role, created_by)
		VALUES ('observer-2', 'admin', 'user-admin')`); err == nil {
		t.Error("invalid global role was accepted")
	}

	options.Direction = "down"
	options.Files = realMigrationFiles(t, []string{version}, "down")
	options.Hooks = hooksForDirection("down")
	if err := runMigrations(ctx, pool, options); err != nil {
		t.Fatal(err)
	}

	for _, table := range []string{"r2d_project_extra", "r2d_project_grants", "r2d_global_roles"} {
		var exists bool
		if err := pool.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", schema+"."+table).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists {
			t.Errorf("expected table %s to be removed by rollback", table)
		}
	}
}
