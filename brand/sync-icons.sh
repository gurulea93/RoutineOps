#!/usr/bin/env bash
# Разносит иконки из brand/ по потребителям. brand/ — единственный исток:
# правишь иконку — кладёшь новый файл сюда и гоняешь этот скрипт.
#
#   logo/       — знак и лого-локап (README, шапка веба)
#   frontend/   — favicon-набор SPA
#   tray/       — иконка в трее (windows: .ico, macos: template + цветная)
#   app-icon/   — иконка приложения (windows: MSI/exe; macos: под .app, пока не собираем)
set -euo pipefail

cd "$(dirname "$0")"
root=".."

# --- web ---
cp frontend/favicon.ico          "$root/web/public/favicon.ico"
cp frontend/favicon-16.png       "$root/web/public/favicon-16.png"
cp frontend/favicon-32.png       "$root/web/public/favicon-32.png"
cp frontend/apple-touch-icon.png "$root/web/public/apple-touch-icon.png"
cp frontend/android-chrome-192.png "$root/web/public/android-chrome-192.png"
cp frontend/android-chrome-512.png "$root/web/public/android-chrome-512.png"
cp logo/routineops-logo-256.png  "$root/web/public/logo.png"

# --- агент: трей ---
# Windows берёт .ico, macOS — template-PNG (монохромная маска, система сама тонирует
# её под светлую/тёмную строку меню). Оба вшиваются go:embed.
# Только КВАДРАТНЫЙ вариант: systray прибивает NSImage к 16x16 pt
# (systray_darwin.m: [image setSize:NSMakeSize(16,16)]) и пропорции не сохраняет —
# исходный 53x44 сплющило бы по горизонтали. -32 = тот же знак с прозрачными полями до квадрата.
cp tray/windows/tray.ico              "$root/cmd/agent/assets/tray.ico"
cp tray/macos/tray-ROTemplate-32.png  "$root/cmd/agent/assets/trayTemplate.png"

# --- windows: иконка MSI ---
cp app-icon/windows/routineops.ico "$root/build/msi/icon.ico"

echo "иконки разнесены из brand/"
