-- 015: перевод всех TIMESTAMP (without time zone) колонок в TIMESTAMPTZ.
--
-- Зачем: TIMESTAMP без зоны хранит "наивное" время и интерпретируется в TimeZone
-- сессии при чтении/сравнении. Сервер пишет время в UTC (pgx, time.Now()), поэтому
-- разные TimeZone у клиента/сервера давали бы расхождения в окнах истечения токенов,
-- грантов admin-доступа и lock-таймерах. TIMESTAMPTZ хранит абсолютный момент.
--
-- Существующие значения были записаны как UTC → при конверсии трактуем их как UTC.
-- Конверсия идемпотентна: фильтр по data_type пропускает уже переведённые колонки,
-- поэтому миграцию безопасно прогонять повторно.
--
-- ВНИМАНИЕ (прод): initdb.d НЕ накатывает миграции на непустой том. На живой VM
-- применить вручную:  psql "$DATABASE_URL" -f migrations/015_timestamptz.sql
--
-- lock_timeout: ALTER ... TYPE берёт AccessExclusiveLock на таблицу. На проде не
-- ждём блокировку дольше 3с (fail-fast вместо очереди блокировок на живом трафике);
-- идемпотентность позволяет повторить миграцию, если часть колонок не успела
-- сконвертироваться из-за таймаута.
SET lock_timeout = '3s';

DO $$
DECLARE
  r record;
BEGIN
  FOR r IN
    SELECT table_schema, table_name, column_name
    FROM information_schema.columns
    WHERE table_schema = 'public'
      AND data_type = 'timestamp without time zone'
  LOOP
    EXECUTE format(
      'ALTER TABLE %I.%I ALTER COLUMN %I TYPE timestamptz USING %I AT TIME ZONE ''UTC''',
      r.table_schema, r.table_name, r.column_name, r.column_name
    );
  END LOOP;
END $$;
