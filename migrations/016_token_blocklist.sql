-- 016: блок-лист отозванных JWT (M-7).
--
-- Зачем: JWT у нас stateless и валиден до естественного истечения (24ч). При
-- logout токен раньше оставался рабочим до экспирации — украденную/слитую куку
-- нельзя было погасить. Теперь при выходе jti токена попадает сюда, а
-- jwtMiddleware отклоняет любой токен с jti из этого списка.
--
-- expires_at = момент, когда токен истёк бы сам → строку можно безопасно удалить
-- (фоновая чистка CleanupExpiredRevokedTokens по суточному тикеру).
CREATE TABLE IF NOT EXISTS token_blocklist (
    jti        TEXT PRIMARY KEY,
    expires_at TIMESTAMPTZ NOT NULL,   -- когда токен истёк бы сам → можно чистить
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_token_blocklist_expires_at ON token_blocklist (expires_at);
