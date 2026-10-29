INSERT INTO system_settings (key, value)
VALUES ('admin_request_default_duration', '3600')
ON CONFLICT (key) DO NOTHING;
