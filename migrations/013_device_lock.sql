-- task_type: 'script' (default) | 'lock'
ALTER TABLE tasks
  ADD COLUMN task_type   TEXT NOT NULL DEFAULT 'script',
  ADD COLUMN lock_hash   TEXT NOT NULL DEFAULT '',
  ADD COLUMN lock_reason TEXT NOT NULL DEFAULT '',
  ADD COLUMN lock_unlock BOOLEAN NOT NULL DEFAULT false;

-- lock_status: 'unlocked' (default) | 'locked'
ALTER TABLE devices
  ADD COLUMN lock_status TEXT NOT NULL DEFAULT 'unlocked';
