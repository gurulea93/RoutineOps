-- Initial schema migration
-- Полное описание схемы — в файлах migrations/*.sql

CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

CREATE TABLE users (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  name TEXT NOT NULL,
  email TEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL,
  role TEXT NOT NULL, -- it_admin / employee
  notification_channel TEXT, -- telegram / email / web
  telegram_chat_id TEXT,
  created_at TIMESTAMP NOT NULL DEFAULT now()
);

CREATE TABLE devices (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  hostname TEXT NOT NULL,
  os TEXT NOT NULL, -- macOS / Windows
  os_version TEXT,
  cpu TEXT,
  ram INTEGER,
  disk TEXT,
  ip_address TEXT,
  status TEXT NOT NULL DEFAULT 'active', -- active / blocked
  certificate_fingerprint TEXT,
  certificate_issued_at TIMESTAMP,
  owner_id UUID REFERENCES users(id),
  created_at TIMESTAMP NOT NULL DEFAULT now(),
  last_seen_at TIMESTAMP
);

CREATE TABLE device_software (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  device_id UUID NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
  software_name TEXT NOT NULL,
  version TEXT,
  detected_at TIMESTAMP NOT NULL DEFAULT now()
);

CREATE TABLE software_policy_rules (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  software_name TEXT NOT NULL,
  rule_type TEXT NOT NULL, -- allowed / forbidden
  device_id UUID REFERENCES devices(id) ON DELETE CASCADE -- NULL = глобальное правило
);

CREATE TABLE process_events (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  device_id UUID NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
  process_name TEXT NOT NULL,
  event_type TEXT NOT NULL, -- started / stopped
  pid INTEGER,
  app_user TEXT,
  occurred_at TIMESTAMP NOT NULL DEFAULT now()
);

CREATE TABLE admin_access_requests (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  device_id UUID NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
  requested_by UUID NOT NULL REFERENCES users(id),
  status TEXT NOT NULL DEFAULT 'pending', -- pending / approved / rejected / expired
  reason TEXT,
  requested_at TIMESTAMP NOT NULL DEFAULT now(),
  pending_expires_at TIMESTAMP NOT NULL,
  decided_by UUID REFERENCES users(id),
  decided_at TIMESTAMP,
  granted_at TIMESTAMP,
  expires_at TIMESTAMP,
  revoked_at TIMESTAMP
);

CREATE TABLE alerts (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  device_id UUID NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
  alert_type TEXT NOT NULL, -- forbidden_software / unauthorized_install / unauthorized_settings_change / agent_unreachable
  admin_access_request_id UUID REFERENCES admin_access_requests(id),
  details TEXT,
  created_at TIMESTAMP NOT NULL DEFAULT now(),
  acknowledged_at TIMESTAMP
);

CREATE TABLE scripts (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  name TEXT NOT NULL,
  platform TEXT NOT NULL, -- macOS / Windows
  content TEXT NOT NULL,
  created_at TIMESTAMP NOT NULL DEFAULT now(),
  updated_at TIMESTAMP NOT NULL DEFAULT now()
);

CREATE TABLE policies (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  name TEXT NOT NULL,
  script_id UUID NOT NULL REFERENCES scripts(id),
  trigger_type TEXT NOT NULL, -- schedule / event_trigger / on_connect
  schedule_config JSONB,
  event_trigger_config JSONB,
  is_active BOOLEAN NOT NULL DEFAULT true,
  created_at TIMESTAMP NOT NULL DEFAULT now()
);

CREATE TABLE device_groups (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  name TEXT NOT NULL,
  created_at TIMESTAMP NOT NULL DEFAULT now()
);

CREATE TABLE device_group_members (
  device_id UUID NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
  group_id UUID NOT NULL REFERENCES device_groups(id) ON DELETE CASCADE,
  PRIMARY KEY (device_id, group_id)
);

CREATE TABLE policy_assignments (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  policy_id UUID NOT NULL REFERENCES policies(id) ON DELETE CASCADE,
  group_id UUID NOT NULL REFERENCES device_groups(id) ON DELETE CASCADE
);

CREATE TABLE system_settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL,
  updated_at TIMESTAMP NOT NULL DEFAULT now()
);

-- Базовые системные настройки
INSERT INTO system_settings (key, value) VALUES
  ('admin_request_timeout_minutes', '15');

-- Индексы для часто запрашиваемых полей
CREATE INDEX idx_devices_owner_id ON devices(owner_id);
CREATE INDEX idx_device_software_device_id ON device_software(device_id);
CREATE INDEX idx_process_events_device_id ON process_events(device_id);
CREATE INDEX idx_process_events_occurred_at ON process_events(occurred_at);
CREATE INDEX idx_admin_access_requests_device_id ON admin_access_requests(device_id);
CREATE INDEX idx_admin_access_requests_status ON admin_access_requests(status);
CREATE INDEX idx_alerts_device_id ON alerts(device_id);
CREATE INDEX idx_alerts_created_at ON alerts(created_at);
