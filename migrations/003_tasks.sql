CREATE TABLE tasks (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  device_id UUID NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
  script_content TEXT NOT NULL,
  platform TEXT NOT NULL,
  priority TEXT NOT NULL DEFAULT 'medium',
  status TEXT NOT NULL DEFAULT 'pending',
  output TEXT,
  error_log TEXT,
  created_at TIMESTAMP NOT NULL DEFAULT now(),
  acked_at TIMESTAMP,
  completed_at TIMESTAMP
);

CREATE INDEX idx_tasks_device_id ON tasks(device_id);
CREATE INDEX idx_tasks_status ON tasks(status);
