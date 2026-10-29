-- 019: подпись ВСЕГО манифеста релиза (version+os+arch+sha256), не только sha256(бинарь).
-- Аддитивно и обратно-совместимо: старые агенты продолжают проверять signature (над
-- sha256 бинаря), новые (SEC-3) — manifest_signature над каноничной строкой манифеста.
-- Закрывает downgrade-relabel: компромет-сервер не подсунет старый подписанный билд под
-- новой меткой версии. У старых строк колонка NULL (agentVersion отдаёт "" → старый агент
-- игнорит поле, новый — без manifest_signature просто не примет апдейт, fail-closed).
ALTER TABLE agent_releases ADD COLUMN IF NOT EXISTS manifest_signature TEXT;
