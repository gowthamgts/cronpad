-- name: ListAlertChannels :many
SELECT * FROM alert_channels WHERE workspace_id = ? ORDER BY id;
-- name: GetAlertChannel :one
SELECT * FROM alert_channels WHERE id = ? AND workspace_id = ?;
-- name: SaveAlertChannel :exec
INSERT INTO alert_channels(id,workspace_id,kind,name,config,verified_at,created_at,signing_secret)
VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET kind=excluded.kind,name=excluded.name,config=excluded.config,verified_at=excluded.verified_at,signing_secret=excluded.signing_secret WHERE alert_channels.workspace_id=excluded.workspace_id;
-- name: DeleteAlertChannel :execrows
DELETE FROM alert_channels WHERE id=? AND workspace_id=?;
-- name: AllAlertRules :many
SELECT * FROM alert_rules WHERE enabled=1;
-- name: ListAlertRules :many
SELECT * FROM alert_rules WHERE workspace_id=? ORDER BY id;
-- name: GetAlertRule :one
SELECT * FROM alert_rules WHERE id=? AND workspace_id=?;
-- name: SaveAlertRule :exec
INSERT INTO alert_rules(id,workspace_id,name,job_selector,events,conditions,channel_ids,cooldown_ms,enabled,created_at)
VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET name=excluded.name,job_selector=excluded.job_selector,events=excluded.events,conditions=excluded.conditions,channel_ids=excluded.channel_ids,cooldown_ms=excluded.cooldown_ms,enabled=excluded.enabled WHERE alert_rules.workspace_id=excluded.workspace_id;
-- name: DeleteAlertRule :execrows
DELETE FROM alert_rules WHERE id=? AND workspace_id=?;
-- name: AddAlertVerification :exec
INSERT INTO alert_verifications(channel_id,address) VALUES(?,?) ON CONFLICT DO NOTHING;
-- name: ClearAlertVerifications :exec
DELETE FROM alert_verifications WHERE channel_id=?;
-- name: VerifyAlertAddress :execrows
UPDATE alert_verifications SET verified_at=? WHERE channel_id=? AND address=?;
-- name: MarkAlertChannelVerified :exec
UPDATE alert_channels SET verified_at=? WHERE id=? AND NOT EXISTS (SELECT 1 FROM alert_verifications WHERE channel_id=alert_channels.id AND verified_at IS NULL);
-- name: WorkspaceOwnerEmails :many
SELECT u.email FROM users u JOIN workspace_members m ON m.user_id=u.id WHERE m.workspace_id=? AND m.role='owner';
-- name: AllWorkspaceIDs :many
SELECT id FROM workspaces ORDER BY id;

-- name: AllAlertSigningSecrets :many
SELECT id,signing_secret FROM alert_channels WHERE signing_secret<>'';
-- name: SetAlertSigningSecret :exec
UPDATE alert_channels SET signing_secret=? WHERE id=?;
-- name: AlertJobState :one
SELECT consecutive_failures,state,auto_disable_threshold FROM jobs WHERE id=? AND workspace_id=? AND failures_reset_version<=sqlc.arg(version) AND deleted_at IS NULL;
