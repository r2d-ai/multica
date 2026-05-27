-- Reverse 110_unified_pull_request: restore old github-specific tables.

-- 1. Re-create old tables
CREATE TABLE github_pull_request (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    installation_id BIGINT NOT NULL,
    repo_owner      TEXT NOT NULL,
    repo_name       TEXT NOT NULL,
    pr_number       INTEGER NOT NULL,
    title           TEXT NOT NULL,
    state           TEXT NOT NULL
        CHECK (state IN ('open', 'closed', 'merged', 'draft')),
    html_url        TEXT NOT NULL,
    branch          TEXT,
    author_login    TEXT,
    author_avatar_url TEXT,
    merged_at       TIMESTAMPTZ,
    closed_at       TIMESTAMPTZ,
    pr_created_at   TIMESTAMPTZ NOT NULL,
    pr_updated_at   TIMESTAMPTZ NOT NULL,
    head_sha        TEXT NOT NULL DEFAULT '',
    mergeable_state TEXT,
    additions       INT NOT NULL DEFAULT 0,
    deletions       INT NOT NULL DEFAULT 0,
    changed_files   INT NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, repo_owner, repo_name, pr_number)
);

CREATE INDEX idx_github_pull_request_workspace ON github_pull_request(workspace_id);

CREATE TABLE github_pull_request_check_suite (
    pr_id        UUID NOT NULL REFERENCES github_pull_request(id) ON DELETE CASCADE,
    suite_id     BIGINT NOT NULL,
    head_sha     TEXT NOT NULL,
    app_id       BIGINT NOT NULL,
    conclusion   TEXT,
    status       TEXT NOT NULL,
    updated_at   TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (pr_id, suite_id)
);

-- 2. Migrate data back
INSERT INTO github_pull_request (
    id, workspace_id, installation_id,
    repo_owner, repo_name, pr_number, title, state,
    html_url, branch, author_login, author_avatar_url,
    merged_at, closed_at, pr_created_at, pr_updated_at,
    head_sha, mergeable_state,
    additions, deletions, changed_files,
    created_at, updated_at
)
SELECT
    id, workspace_id, installation_id,
    repo_owner, repo_name, pr_number, title, state,
    html_url, branch, author_login, author_avatar_url,
    merged_at, closed_at, pr_created_at, pr_updated_at,
    head_sha, mergeable_state,
    additions, deletions, changed_files,
    created_at, updated_at
FROM pull_request
WHERE source = 'github';

INSERT INTO github_pull_request_check_suite (pr_id, suite_id, head_sha, app_id, conclusion, status, updated_at)
SELECT pr_id, suite_id, head_sha, app_id, conclusion, status, updated_at
FROM pull_request_check_suite;

-- 3. Fix issue_pull_request FK to point back at github_pull_request
ALTER TABLE issue_pull_request DROP CONSTRAINT issue_pull_request_pull_request_id_fkey;
ALTER TABLE issue_pull_request ADD FOREIGN KEY (pull_request_id) REFERENCES github_pull_request(id) ON DELETE CASCADE;

-- 4. Drop backward-compat views
DROP VIEW github_pull_request_check_suite;
DROP VIEW github_pull_request;

-- 5. Drop new tables
DROP TABLE pull_request_check_suite;
DROP TABLE pull_request;
