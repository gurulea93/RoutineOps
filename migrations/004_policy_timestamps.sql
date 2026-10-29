-- Add updated_at to software_policy_rules for FetchPolicy versioning (unix max(updated_at))
ALTER TABLE software_policy_rules
  ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT now();
