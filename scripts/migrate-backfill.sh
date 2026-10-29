#!/bin/sh
# ONE-TIME backfill для СУЩЕСТВУЮЩИХ инсталляций (напр. текущий прод), где миграции
# уже накатаны ВРУЧНУЮ до появления schema_migrations. Помечает миграции ВПЛОТЬ до
# baseline (BACKFILL_UPTO) как применённые БЕЗ выполнения — чтобы migrate.sh их не
# прогнал повторно (001..NNN НЕ идемпотентны).
#
# BACKFILL_UPTO ОБЯЗАТЕЛЕН и = последняя РЕАЛЬНО накатанная у тебя миграция (номер или
# имя файла, напр. "022" или "022_filevault_escrow.sql"). Миграции ВЫШЕ baseline НЕ
# помечаются — их накатит migrate.sh. Это защищает от кейса «git pull принёс новую
# миграцию 023, которую backfill пометил бы применённой без выполнения → schema drift».
#
# НЕ запускать на пустой/новой БД — там всё накатывает migrate.sh с нуля.
#
# Запуск на проде (см. docs/self-hosted-deploy.md):
#   set -a; . ./.env.prod; set +a
#   docker run --rm --network "<compose-net>" \
#     -v "$PWD/migrations:/migrations:ro" -v "$PWD/scripts:/scripts:ro" \
#     -e DATABASE_DSN -e BACKFILL_UPTO=022 postgres:16-alpine sh /scripts/migrate-backfill.sh
set -e

: "${DATABASE_DSN:?DATABASE_DSN не задан (ожидается из .env.prod)}"
: "${BACKFILL_UPTO:?Укажи BACKFILL_UPTO=<последняя_накатанная_миграция>, напр. 022 или 022_filevault_escrow.sql}"
MIGRATIONS_DIR="${MIGRATIONS_DIR:-/migrations}"

# Нормализуем baseline к 3-значному префиксу (022). sed убирает ведущие нули перед
# printf (иначе 022 трактуется как восьмеричное).
upto_n=$(echo "$BACKFILL_UPTO" | grep -oE '[0-9]+' | head -1 | sed 's/^0*//')
[ -z "$upto_n" ] && { echo "BACKFILL_UPTO не содержит номера миграции" >&2; exit 1; }
upto=$(printf '%03d' "$upto_n")

echo "BACKFILL: пометить миграции ВПЛОТЬ до ${upto} как применённые БЕЗ выполнения."

psql "$DATABASE_DSN" -v ON_ERROR_STOP=1 -c '
  CREATE TABLE IF NOT EXISTS schema_migrations (
    version    TEXT PRIMARY KEY,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
  );'

for f in $(ls "$MIGRATIONS_DIR"/*.sql | sort); do
  v=$(basename "$f")
  pfx=$(echo "$v" | cut -c1-3)
  # Строковое сравнение зеропадженных 3-значных префиксов корректно (001<=022, 023>022).
  if [ "$pfx" \> "$upto" ]; then
    echo "leave  $v (> ${upto}) — накатит migrate.sh"
    continue
  fi
  psql "$DATABASE_DSN" -v ON_ERROR_STOP=1 \
    -c "INSERT INTO schema_migrations(version) VALUES ('$v') ON CONFLICT DO NOTHING;"
  echo "marked $v"
done

echo "backfill complete — migrate.sh накатит только миграции ВЫШЕ ${upto}"
