-- 027: цвет группы устройств.
--
-- Рамка устройства в списке красится цветом его группы, поэтому цвет обязан быть у
-- КАЖДОЙ группы, включая созданные до этой миграции. Бэкфилим детерминированной
-- палитрой: строки раскладываются по 8 цветам циклом по (created_at, id), так что
-- результат одинаков на любой инсталляции и не зависит от порядка выдачи планировщика.
--
-- Формат хранения — '#rrggbb' строчными. CHECK обязателен: значение уезжает прямо в
-- style-атрибут фронта, произвольная строка из API там ни к чему.
SET lock_timeout = '3s';

ALTER TABLE device_groups ADD COLUMN IF NOT EXISTS color TEXT;

WITH palette(idx, hex) AS (
  VALUES (0, '#ef4444'), (1, '#f97316'), (2, '#eab308'), (3, '#22c55e'),
         (4, '#14b8a6'), (5, '#3b82f6'), (6, '#8b5cf6'), (7, '#ec4899')
), ranked AS (
  SELECT id, (row_number() OVER (ORDER BY created_at, id) - 1) % 8 AS idx
  FROM device_groups
  WHERE color IS NULL
)
UPDATE device_groups g
SET    color = palette.hex
FROM   ranked JOIN palette ON palette.idx = ranked.idx
WHERE  g.id = ranked.id;

-- DEFAULT нужен не для API (хендлер всегда шлёт цвет), а для psql-вставок вручную и
-- для тестов, которые создают группу одним INSERT'ом.
ALTER TABLE device_groups ALTER COLUMN color SET DEFAULT '#3b82f6';
ALTER TABLE device_groups ALTER COLUMN color SET NOT NULL;

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint WHERE conname = 'device_groups_color_hex'
  ) THEN
    ALTER TABLE device_groups
      ADD CONSTRAINT device_groups_color_hex CHECK (color ~ '^#[0-9a-f]{6}$');
  END IF;
END $$;
