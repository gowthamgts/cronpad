-- name: UserByEmail :one
SELECT * FROM users WHERE email=? COLLATE NOCASE;
-- name: UserByID :one
SELECT * FROM users WHERE id=?;
-- name: IdentityUser :one
SELECT u.* FROM users u JOIN identities i ON i.user_id=u.id WHERE i.provider=? AND i.subject=?;
-- name: InsertUser :exec
INSERT INTO users(id,email,name,avatar_url,created_at) VALUES(?,?,?,?,?);
-- name: LinkIdentity :exec
INSERT INTO identities(provider,subject,user_id,created_at) VALUES(?,?,?,?) ON CONFLICT(provider,subject) DO NOTHING;
-- name: TouchLogin :exec
UPDATE users SET last_login_at=? WHERE id=?;
-- name: PersonalWorkspace :one
SELECT w.* FROM workspaces w JOIN workspace_members m ON m.workspace_id=w.id WHERE m.user_id=? AND m.role='owner' ORDER BY w.created_at,w.id LIMIT 1;
-- name: UserWorkspaces :many
SELECT w.* FROM workspaces w JOIN workspace_members m ON m.workspace_id=w.id WHERE m.user_id=? ORDER BY w.id;
-- name: WorkspaceMember :one
SELECT role FROM workspace_members WHERE workspace_id=? AND user_id=?;
-- name: PatchWorkspace :one
UPDATE workspaces SET name=? WHERE id=? RETURNING *;
-- name: FindSession :one
SELECT data FROM sessions WHERE token=? AND expiry>?;
-- name: CommitSession :exec
INSERT INTO sessions(token,data,expiry) VALUES(?,?,?) ON CONFLICT(token) DO UPDATE SET data=excluded.data,expiry=excluded.expiry;
-- name: DeleteSession :exec
DELETE FROM sessions WHERE token=?;
-- name: ExpireSessions :exec
DELETE FROM sessions WHERE expiry<=?;
-- name: KeyByHash :one
SELECT * FROM api_keys WHERE hash=? AND revoked_at IS NULL;
-- name: CreateKey :one
INSERT INTO api_keys(id,workspace_id,name,prefix,hash,scopes,created_by,created_at) VALUES(?,?,?,?,?,?,?,?) RETURNING *;
-- name: ListKeys :many
SELECT id,workspace_id,name,prefix,scopes,created_by,created_at,last_used_at,revoked_at FROM api_keys WHERE workspace_id=sqlc.arg(workspace_id) AND id>sqlc.arg(cursor) ORDER BY id LIMIT sqlc.arg(page_size);
-- name: CountKeys :one
SELECT count(*) FROM api_keys WHERE workspace_id=? AND revoked_at IS NULL;
-- name: RevokeKey :execrows
UPDATE api_keys SET revoked_at=sqlc.arg(now) WHERE id=sqlc.arg(id) AND workspace_id=sqlc.arg(workspace_id) AND revoked_at IS NULL;
-- name: TouchKey :exec
UPDATE api_keys SET last_used_at=sqlc.arg(now) WHERE id=sqlc.arg(id) AND (last_used_at IS NULL OR last_used_at<sqlc.arg(before));
-- name: ListSecrets :many
SELECT name,created_at,updated_at FROM secrets WHERE workspace_id=sqlc.arg(workspace_id) AND name>sqlc.arg(cursor) ORDER BY name LIMIT sqlc.arg(page_size);
-- name: GetSecret :one
SELECT ciphertext FROM secrets WHERE workspace_id=? AND name=?;
-- name: PutSecret :exec
INSERT INTO secrets(workspace_id,name,ciphertext,created_at,updated_at) VALUES(?,?,?,?,?) ON CONFLICT(workspace_id,name) DO UPDATE SET ciphertext=excluded.ciphertext,updated_at=excluded.updated_at;
-- name: DeleteSecret :execrows
DELETE FROM secrets WHERE workspace_id=? AND name=?;
-- name: AllSecrets :many
SELECT * FROM secrets;
-- name: AllSigningSecrets :many
SELECT id,signing_secrets FROM jobs WHERE signing_secrets IS NOT NULL;
-- name: SetSigningSecrets :exec
UPDATE jobs SET signing_secrets=? WHERE id=?;
-- name: AllIdempotency :many
SELECT * FROM idempotency_records;
-- name: SetIdempotencyResponse :exec
UPDATE idempotency_records SET response=? WHERE workspace_id=? AND key=?;
-- name: WorkspaceJobStates :many
SELECT state,count(*) AS n FROM jobs WHERE workspace_id=? AND deleted_at IS NULL GROUP BY state;
-- name: FailingJobs :many
SELECT id,name,consecutive_failures FROM jobs WHERE workspace_id=? AND deleted_at IS NULL AND consecutive_failures>0 ORDER BY consecutive_failures DESC,id LIMIT 50;
-- name: UpdateUserEmail :exec
UPDATE users SET email=? WHERE id=?;
-- name: HasUsers :one
SELECT EXISTS(SELECT 1 FROM users);
-- name: CountActiveUsers :one
SELECT count(*) FROM users WHERE deactivated_at IS NULL;
