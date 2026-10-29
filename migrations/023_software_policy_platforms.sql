-- Платформенный фильтр для software-политик. NULL/пусто = правило применяется на всех
-- платформах (обратная совместимость: существующие правила остаются глобальными по ОС).
ALTER TABLE software_policy_rules ADD COLUMN IF NOT EXISTS platforms TEXT[];
