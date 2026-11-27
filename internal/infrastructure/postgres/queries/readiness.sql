-- name: ReadLedger :exec
-- Reads the ledger, which readiness requires: it fails until the migrations have created it.
SELECT 1 FROM notification LIMIT 1;
