-- 021: расширение групп — софт-политики на группу (#2) + индексы под резолвинг.
--
-- На проде миграции НЕ авто (initdb.d не трогает непустой том). Применить вручную:
--   psql "$DATABASE_URL" -f migrations/021_group_policy_extensions.sql
-- ADD COLUMN/INDEX IF NOT EXISTS → идемпотентно, безопасно прогонять повторно.
SET lock_timeout = '3s';

-- #2: групповые софт-правила (мягко, зеркалит device_id). NULL group_id = не групповое.
ALTER TABLE software_policy_rules
  ADD COLUMN IF NOT EXISTS group_id UUID REFERENCES device_groups(id) ON DELETE CASCADE;

-- Индекс под ветку резолвинга "group_id IN (группы устройства)".
CREATE INDEX IF NOT EXISTS idx_software_policy_rules_group_id
  ON software_policy_rules(group_id);

-- Резолвинг тянет device_group_members по device_id (FetchPolicyRules, fan-out).
-- PK = (device_id, group_id) уже индексирует префикс device_id, но явный индекс
-- по group_id ускоряет обратную выборку членов группы (fan-out, листинг групп).
CREATE INDEX IF NOT EXISTS idx_device_group_members_group_id
  ON device_group_members(group_id);
