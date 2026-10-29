-- Stage 5: добавляем updated_at в policies и таблицу результатов скриптов

ALTER TABLE policies ADD COLUMN IF NOT EXISTS updated_at TIMESTAMP NOT NULL DEFAULT now();

CREATE INDEX IF NOT EXISTS idx_policy_assignments_group_id ON policy_assignments(group_id);
CREATE INDEX IF NOT EXISTS idx_device_group_members_device_id ON device_group_members(device_id);

CREATE TABLE script_results (
  id            UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  policy_id     UUID NOT NULL REFERENCES policies(id) ON DELETE CASCADE,
  device_id     UUID NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
  run_id        TEXT NOT NULL,           -- uuid от агента (идемпотентность)
  exit_code     INTEGER NOT NULL,
  stdout        TEXT,
  stderr        TEXT,
  trigger       TEXT NOT NULL,           -- schedule / event / on_connect
  started_at    TIMESTAMP NOT NULL,
  finished_at   TIMESTAMP NOT NULL,
  created_at    TIMESTAMP NOT NULL DEFAULT now(),
  UNIQUE (run_id)
);

CREATE INDEX idx_script_results_policy_id ON script_results(policy_id);
CREATE INDEX idx_script_results_device_id ON script_results(device_id);
CREATE INDEX idx_script_results_created_at ON script_results(created_at);
