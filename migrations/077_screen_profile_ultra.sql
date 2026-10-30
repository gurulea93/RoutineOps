-- 077: профиль сеанса экрана `ultra` (30 к/с, родное разрешение, JPEG высокого качества).
--
-- Плоский SQL без goose-аннотаций; всё после «-- DOWN:» закомментировано.
--
-- Зачем. high (10 к/с, 1920 px, quality 70) годится для мыши, но текст на 2K/4K мыльный, а
-- движение рваное. На хорошем канале и сервере с запасом нужен профиль «как вживую».
-- Потолок тенанта остаётся потолком: умолчание не меняем, ultra включают явно.

SET lock_timeout = '5s';

ALTER TABLE tenants DROP CONSTRAINT IF EXISTS tenants_screen_profile_cap_check;
ALTER TABLE tenants
  ADD CONSTRAINT tenants_screen_profile_cap_check
  CHECK (screen_profile_cap IN ('low', 'medium', 'high', 'ultra'));

COMMENT ON COLUMN tenants.screen_profile_cap IS
  'Потолок профиля сеанса экрана: low (1 к/с, 1280), medium (2 к/с, 1920), high (10 к/с, 1920) или ultra (30 к/с, родное разрешение, quality 88).';

-- DOWN:
-- UPDATE tenants SET screen_profile_cap = 'high' WHERE screen_profile_cap = 'ultra';
-- ALTER TABLE tenants DROP CONSTRAINT IF EXISTS tenants_screen_profile_cap_check;
-- ALTER TABLE tenants ADD CONSTRAINT tenants_screen_profile_cap_check CHECK (screen_profile_cap IN ('low', 'medium', 'high'));
