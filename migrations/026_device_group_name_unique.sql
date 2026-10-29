-- 026: имена групп устройств уникальны.
-- До сих пор ни схема, ни код не мешали создать две группы «Бухгалтерия»: в UI они
-- выглядят одинаково, назначения политик расползаются между дублями.
--
-- Сравнение нечувствительно к регистру и краевым пробелам («  Бухгалтерия » == «бухгалтерия»),
-- потому что именно так люди путаются. Хранится имя как введено.

-- 1. Пустые имена и имена из одних пробелов: NOT NULL их пропускал.
--    Чиним ДО дедупликации, иначе все пустые попадут в одну партицию и получат суффиксы.
UPDATE device_groups
SET    name = 'Группа ' || left(id::text, 8)
WHERE  btrim(name) = '';

-- 2. Разводим существующие дубли: самой старой группе имя оставляем, остальным
--    дописываем фрагмент её id. Суффикс из id, а не порядковый номер: « (2)» могло бы
--    столкнуться с уже существующей группой «Бухгалтерия (2)» и уронить индекс ниже.
WITH ranked AS (
  SELECT id,
         name,
         row_number() OVER (PARTITION BY lower(btrim(name)) ORDER BY created_at, id) AS rn
  FROM device_groups
)
UPDATE device_groups g
SET    name = ranked.name || ' #' || left(g.id::text, 8)
FROM   ranked
WHERE  g.id = ranked.id
  AND  ranked.rn > 1;

-- 3. Гарантии на будущее. Идемпотентно: миграции гоняет migrate-сервис по
--    schema_migrations, но ручной повтор не должен падать.
DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint WHERE conname = 'device_groups_name_not_blank'
  ) THEN
    ALTER TABLE device_groups
      ADD CONSTRAINT device_groups_name_not_blank CHECK (btrim(name) <> '');
  END IF;
END $$;

CREATE UNIQUE INDEX IF NOT EXISTS device_groups_name_unique ON device_groups (lower(btrim(name)));
