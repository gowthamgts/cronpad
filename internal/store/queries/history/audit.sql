-- name: LastAuditRowID :one
SELECT CAST(COALESCE(max(rowid),0) AS INTEGER) FROM audit_log;
