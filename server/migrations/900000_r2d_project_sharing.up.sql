CREATE TABLE r2d_project_extra (
    project_id TEXT PRIMARY KEY,
    visibility TEXT NOT NULL DEFAULT 'workspace'
        CHECK (visibility IN ('workspace', 'private')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE r2d_project_grants (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    principal_type TEXT NOT NULL
        CHECK (principal_type IN ('user', 'workspace')),
    principal_id TEXT NOT NULL,
    role TEXT NOT NULL
        CHECK (role IN ('viewer', 'member', 'manager')),
    created_by TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT r2d_project_grants_project_principal_key
        UNIQUE (project_id, principal_type, principal_id)
);

CREATE INDEX r2d_project_grants_principal_projects_idx
    ON r2d_project_grants (principal_type, principal_id, project_id);

CREATE TABLE r2d_global_roles (
    user_id TEXT NOT NULL,
    role TEXT NOT NULL
        CHECK (role = 'global_observer'),
    created_by TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, role)
);

CREATE INDEX r2d_global_roles_role_users_idx
    ON r2d_global_roles (role, user_id);
