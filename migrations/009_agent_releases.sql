CREATE TABLE agent_releases (
  id         UUID      PRIMARY KEY DEFAULT uuid_generate_v4(),
  os         TEXT      NOT NULL,
  arch       TEXT      NOT NULL,
  version    TEXT      NOT NULL,
  filename   TEXT      NOT NULL,
  sha256     TEXT      NOT NULL,
  signature  TEXT      NOT NULL,
  created_at TIMESTAMP NOT NULL DEFAULT now(),
  UNIQUE (os, arch, version)
);

CREATE INDEX idx_agent_releases_os_arch ON agent_releases(os, arch, created_at DESC);
