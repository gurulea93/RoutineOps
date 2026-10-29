-- 025: версия агентского бинаря на устройстве — для видимости раскатки self-update.
-- Приходит от агента (proto DeviceInfo.agent_version = ldflags main.version) на
-- каждом ReportInventory. NULL/'' у устройств, ещё не приславших инвентарь новым
-- агентом; UpsertInventory не затирает известную версию пустым значением.
ALTER TABLE devices ADD COLUMN IF NOT EXISTS agent_version TEXT;
