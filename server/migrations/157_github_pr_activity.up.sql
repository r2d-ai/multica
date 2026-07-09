-- GitHub PR activity dedupe + Multica comment/thread mapping for linked PRs.

CREATE TABLE github_pr_activity (
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id            UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    pull_request_id         UUID NOT NULL REFERENCES github_pull_request(id) ON DELETE CASCADE,
    issue_id                UUID NOT NULL REFERENCES issue(id) ON DELETE CASCADE,
    event_kind              TEXT NOT NULL
        CHECK (event_kind IN (
            'issue_comment',
            'pull_request_review',
            'pull_request_review_comment',
            'pull_request_review_thread'
        )),
    github_external_id      BIGINT NOT NULL,
    action                  TEXT NOT NULL,
    github_thread_id        BIGINT,
    review_state            TEXT,
    body_hash               TEXT,
    actor_login             TEXT,
    actor_type              TEXT,
    github_url              TEXT,
    comment_id              UUID REFERENCES comment(id) ON DELETE SET NULL,
    thread_root_comment_id  UUID REFERENCES comment(id) ON DELETE SET NULL,
    resolved                BOOLEAN NOT NULL DEFAULT FALSE,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, issue_id, event_kind, github_external_id, action)
);

CREATE INDEX idx_github_pr_activity_pr ON github_pr_activity(pull_request_id);
CREATE INDEX idx_github_pr_activity_issue ON github_pr_activity(issue_id);
CREATE INDEX idx_github_pr_activity_thread ON github_pr_activity(pull_request_id, github_thread_id)
    WHERE github_thread_id IS NOT NULL;
