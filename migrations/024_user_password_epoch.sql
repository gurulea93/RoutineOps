-- Token-epoch для инвалидации JWT при смене/сбросе пароля (security-hardening).
-- jwtMiddleware отвергает токен, выпущенный ДО password_changed_at — украденный/
-- утёкший токен перестаёт работать сразу после смены пароля, а не живёт до конца
-- 8ч-TTL. Смена собственного пароля не разлогинивает: changePassword переминчивает
-- свежий токен (его iat >= новый epoch), дохнут только ранее выпущенные токены.
--
-- Идемпотентно и БЕЗОПАСНО при повторном прогоне (не клоберит реальные смены):
-- backfill только строк, где значение ещё не проставлено (IS NULL).
ALTER TABLE users ADD COLUMN IF NOT EXISTS password_changed_at TIMESTAMPTZ;

-- Существующие юзеры: created_at — нижняя граница (пароль установлен не раньше
-- создания). Все УЖЕ выпущенные токены имеют iat >= created_at → переживают деплой
-- (без массового разлогина). Повторный прогон: строк с NULL нет → no-op.
UPDATE users SET password_changed_at = created_at WHERE password_changed_at IS NULL;

ALTER TABLE users ALTER COLUMN password_changed_at SET DEFAULT now();
ALTER TABLE users ALTER COLUMN password_changed_at SET NOT NULL;
