package storage

import (
	"context"
	"time"
)

// RevokeToken помещает jti токена в блок-лист (M-7). expiresAt — момент, когда
// токен истёк бы сам; по нему фоновая чистка удаляет строку. Повторный отзыв
// того же jti — no-op (ON CONFLICT DO NOTHING).
func (db *DB) RevokeToken(ctx context.Context, jti string, expiresAt time.Time) error {
	_, err := db.pool.Exec(ctx,
		`INSERT INTO token_blocklist (jti, expires_at) VALUES ($1, $2) ON CONFLICT (jti) DO NOTHING`,
		jti, expiresAt)
	return err
}

// IsTokenRevoked сообщает, лежит ли jti в блок-листе. Вызывается на каждый
// аутентифицированный запрос из jwtMiddleware — fail-closed на ошибке БД.
func (db *DB) IsTokenRevoked(ctx context.Context, jti string) (bool, error) {
	var revoked bool
	err := db.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM token_blocklist WHERE jti = $1 AND expires_at > now())`, jti).Scan(&revoked)
	return revoked, err
}

// CleanupExpiredRevokedTokens удаляет записи об уже истёкших токенах — держать их
// в блок-листе после экспирации бессмысленно. Возвращает число удалённых строк.
func (db *DB) CleanupExpiredRevokedTokens(ctx context.Context) (int64, error) {
	tag, err := db.pool.Exec(ctx, `DELETE FROM token_blocklist WHERE expires_at < now()`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
