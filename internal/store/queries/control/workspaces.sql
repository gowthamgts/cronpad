-- name: GetWorkspace :one
SELECT * FROM workspaces WHERE id = ?;

-- name: CreateWorkspace :exec
INSERT INTO workspaces (id, name, slug, plan, created_at) VALUES (?, ?, ?, ?, ?);

-- name: WorkspaceSuspension :one
SELECT suspended_at,suspended_reason FROM workspaces WHERE id=?;
-- name: WorkspaceBySlug :one
SELECT * FROM workspaces WHERE slug=?;
-- name: SetWorkspaceSuspension :execrows
UPDATE workspaces SET suspended_at=sqlc.narg(suspended_at),suspended_reason=sqlc.narg(suspended_reason) WHERE id=sqlc.arg(id);
-- name: CountWorkspaces :one
SELECT count(*) AS total, count(suspended_at) AS suspended FROM workspaces;
