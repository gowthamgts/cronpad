-- name: UpsertDirectoryUser :one
INSERT INTO users(id,email,name,created_at) VALUES(?,?,?,?)
ON CONFLICT(email) DO UPDATE SET name=excluded.name
RETURNING *;
-- name: UpdateDirectoryUser :execrows
UPDATE users SET email=?,name=? WHERE id=?;
-- name: SetMembership :exec
INSERT INTO workspace_members(workspace_id,user_id,role) VALUES(?,?,?)
ON CONFLICT(workspace_id,user_id) DO UPDATE SET role=excluded.role;
-- name: RemoveMembership :execrows
DELETE FROM workspace_members WHERE workspace_id=? AND user_id=?;
-- name: DeactivateUser :execrows
UPDATE users SET deactivated_at=? WHERE id=?;
-- name: RevokeUserKeys :exec
UPDATE api_keys SET revoked_at=? WHERE created_by=? AND revoked_at IS NULL;
-- name: SetWorkspacePlan :execrows
UPDATE workspaces SET plan=? WHERE id=?;
-- name: GetKey :one
SELECT * FROM api_keys WHERE id=? AND workspace_id=?;
-- name: DirectorySessions :many
SELECT token,data FROM sessions;
