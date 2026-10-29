-- 022: FileVault динамический лок — escrow recovery-ключей + режим/actual-состояние лока.
--
-- 021 занят device-groups → это 022. Синхронизировано с ратифицированным
-- дизайном FileVault-эскроу (внутренние design-доки).
--
-- На проде миграции НЕ авто (initdb.d не трогает непустой том). Применить вручную:
--   psql "$DATABASE_URL" -f migrations/022_filevault_escrow.sql
-- ADD COLUMN/INDEX/TABLE IF NOT EXISTS → идемпотентно, безопасно прогонять повторно.
--
-- lock_timeout: ALTER берёт AccessExclusiveLock; на живом трафике не ждём >3с (fail-fast),
-- идемпотентность позволяет повторить (как 015/021).
SET lock_timeout = '3s';

-- Режим лока на ОБЕИХ таблицах (pull+push), DEFAULT 'overlay' — НИКОГДА не переключаем
-- единственный прод-мак в деструктив без спроса (fail-safe).
ALTER TABLE devices ADD COLUMN IF NOT EXISTS lock_mode         TEXT NOT NULL DEFAULT 'overlay';
ALTER TABLE tasks   ADD COLUMN IF NOT EXISTS lock_mode         TEXT NOT NULL DEFAULT 'overlay';

-- REPORTED состояние (actual), ОТДЕЛЬНО от lock_status (DESIRED). Без отдельной колонки
-- filevault half-state (FILEVAULT_REVOKED) испортил бы desired → реконсайлер пере-ревокнет
-- (класс полевого re-lock-бага). Обязательна, не опция.
ALTER TABLE devices ADD COLUMN IF NOT EXISTS lock_actual_state TEXT NOT NULL DEFAULT '';
ALTER TABLE devices ADD COLUMN IF NOT EXISTS lock_actual_at    TIMESTAMPTZ;

-- Право на reveal escrow — НЕ роль (requireRole = точное сравнение строки, роль одна в JWT).
-- BOOL + live-проверка в отдельном middleware, fail-closed. Не самовыдаётся обычным it_admin
--: super-admin tier ИЛИ out-of-band DB-грант.
ALTER TABLE users   ADD COLUMN IF NOT EXISTS can_reveal_escrow BOOLEAN NOT NULL DEFAULT false;

-- Хранилище escrow: прод держит ТОЛЬКО шифртекст (age), расшифровать НЕ может.
CREATE TABLE IF NOT EXISTS recovery_key_escrow (
    id            UUID PRIMARY KEY DEFAULT uuid_generate_v4(),          -- uuid-ossp (001)
    device_id     UUID NOT NULL REFERENCES devices(id) ON DELETE RESTRICT,  -- НЕ CASCADE:
        -- удаление устройства снесло бы единственную копию PRK ещё зашифрованного тома =
        -- вечный кирпич. Блок удаления при активном локе +
        -- escrow в бэкап-сет — слоями.
    request_id    TEXT NOT NULL,
    secret_type   TEXT NOT NULL CHECK (secret_type IN ('prk','secondary_cred')),  -- ужесточено (§5)
    ciphertext    BYTEA NOT NULL,              -- age blob; прод расшифровать НЕ может
    recipient_fpr TEXT NOT NULL,               -- сверять с pinned на каждом reveal
    key_scheme    TEXT NOT NULL DEFAULT 'age-v1',
    agent_version TEXT,
    escrowed_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    revealed_at   TIMESTAMPTZ,
    revealed_by   UUID REFERENCES users(id) ON DELETE SET NULL,  -- §5: аудит переживает удаление ревьюера
    -- идемпотентность энролл-эскроу: повтор того же (device,request,type) НЕ затирает
    -- хороший blob (ON CONFLICT DO NOTHING в коде; DO UPDATE поверх непустого = кирпич).
    UNIQUE (device_id, request_id, secret_type)
);
CREATE INDEX IF NOT EXISTS idx_recovery_key_escrow_device
    ON recovery_key_escrow(device_id, escrowed_at DESC);
