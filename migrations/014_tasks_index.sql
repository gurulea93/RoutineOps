-- Partial covering index: только pending-таски (активный рабочий набор).
-- Устраняет full-scan по device_id на каждом heartbeat-поллинге.
CREATE INDEX IF NOT EXISTS idx_tasks_device_status_pending
    ON tasks (device_id, created_at)
    WHERE status = 'pending';
