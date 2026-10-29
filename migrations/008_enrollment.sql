CREATE TABLE enrollment_tokens (
  id         UUID      PRIMARY KEY DEFAULT uuid_generate_v4(),
  device_id  UUID      NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
  token      TEXT      NOT NULL UNIQUE,
  expires_at TIMESTAMP NOT NULL,
  used_at    TIMESTAMP,
  created_at TIMESTAMP NOT NULL DEFAULT now()
);

CREATE INDEX idx_enrollment_tokens_token ON enrollment_tokens(token);

ALTER TABLE devices ADD COLUMN IF NOT EXISTS cert_serial TEXT;
ALTER TABLE devices ADD COLUMN IF NOT EXISTS enrolled_at TIMESTAMP;
