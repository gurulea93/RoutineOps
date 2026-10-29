CREATE TABLE audit_log (
  id          UUID      PRIMARY KEY DEFAULT uuid_generate_v4(),
  user_id     UUID,
  user_email  TEXT      NOT NULL DEFAULT '',
  action      TEXT      NOT NULL,
  target_type TEXT      NOT NULL,
  target_id   TEXT      NOT NULL,
  details     JSONB,
  created_at  TIMESTAMP NOT NULL DEFAULT now()
);

CREATE INDEX idx_audit_log_created_at ON audit_log(created_at DESC);
CREATE INDEX idx_audit_log_user_id    ON audit_log(user_id);
CREATE INDEX idx_audit_log_action     ON audit_log(action);
