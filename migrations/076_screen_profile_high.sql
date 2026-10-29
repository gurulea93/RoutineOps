-- 076: профиль сеанса экрана `high` (10 к/с, до 1920 px).
--
-- 🔴 Формат файла: ПЛОСКИЙ SQL, без goose-аннотаций. Всё, что стоит после «-- DOWN:»,
-- закомментировано и живой SQL'ой не является.
--
-- Зачем. 1-2 кадра в секунду годятся, чтобы смотреть, но не чтобы управлять: курсор
-- запаздывает на секунду, и работать мышью нельзя. На локальной сети и обычном
-- офисном канале 10 к/с укладываются в бюджет с запасом (на кадр уходит ~20 КБ даже при
-- перетаскивании окон), а тенант на узком канале по-прежнему может опустить потолок до
-- medium или low: потолок остаётся потолком, а не значением.
--
-- Умолчание становится `high`. Колонка получила умолчание `medium` в 075 и явно ею никто
-- не управлял, поэтому существующие `medium` — это «умолчание», а не выбор: переводим
-- их на `high`. Кому нужен прежний потолок, ставит его в «Доступ к экрану».

SET lock_timeout = '5s';

ALTER TABLE tenants DROP CONSTRAINT IF EXISTS tenants_screen_profile_cap_check;
ALTER TABLE tenants
  ADD CONSTRAINT tenants_screen_profile_cap_check
  CHECK (screen_profile_cap IN ('low', 'medium', 'high'));

ALTER TABLE tenants ALTER COLUMN screen_profile_cap SET DEFAULT 'high';
UPDATE tenants SET screen_profile_cap = 'high' WHERE screen_profile_cap = 'medium';

COMMENT ON COLUMN tenants.screen_profile_cap IS
  'Потолок профиля сеанса экрана: low (1 к/с, 1280), medium (2 к/с, 1920) или high (10 к/с, 1920). Сервер выдаёт профиль в приглашении не выше этого значения.';

-- DOWN:
-- UPDATE tenants SET screen_profile_cap = 'medium' WHERE screen_profile_cap = 'high';
-- ALTER TABLE tenants DROP CONSTRAINT IF EXISTS tenants_screen_profile_cap_check;
-- ALTER TABLE tenants ADD CONSTRAINT tenants_screen_profile_cap_check CHECK (screen_profile_cap IN ('low', 'medium'));
-- ALTER TABLE tenants ALTER COLUMN screen_profile_cap SET DEFAULT 'medium';
