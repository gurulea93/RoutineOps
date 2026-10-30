# Сборка Windows-агента с видеокодером VP8 (libvpx через CGO) — для WebRTC-режима удалённого стола.
#
# Образ сервера собирает агентов с CGO_ENABLED=0, поэтому VP8 там невозможен. Этот скрипт собирает
# агента на ПК с MSYS2 (UCRT64: gcc, pkg-config, libvpx) и кладёт его в build/windows/. Дальше
# файлы копируются на сервер, а install.sh публикует их вместо обычной сборки (если версия
# и sha256 совпали).
#
# Один раз в MSYS2 UCRT64:
#   pacman -S --needed mingw-w64-ucrt-x86_64-gcc mingw-w64-ucrt-x86_64-pkgconf mingw-w64-ucrt-x86_64-libvpx
#
# Запуск (PowerShell, из корня репозитория):
#   powershell -ExecutionPolicy Bypass -File scripts\build-windows-vpx.ps1
param(
  [string]$Msys2 = "C:\msys64",
  [string]$Tags = "screen vpx"
)
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

$ver = (Get-Content AGENT_VERSION -Raw).Trim()
$parts = $ver.Split(".")
$bin = Join-Path $Msys2 "ucrt64\bin"
if (-not (Test-Path (Join-Path $bin "gcc.exe"))) { throw "gcc не найден в $bin — установите пакеты MSYS2 UCRT64 (см. заголовок скрипта)" }
$env:PATH = "$bin;$env:PATH"
$env:PKG_CONFIG_PATH = Join-Path $Msys2 "ucrt64\lib\pkgconfig"
$env:CGO_ENABLED = "1"
$env:GOOS = "windows"
$env:GOARCH = "amd64"

& pkg-config --exists vpx
if ($LASTEXITCODE -ne 0) { throw "pkg-config не видит libvpx: pacman -S mingw-w64-ucrt-x86_64-libvpx" }

# Манифест Common-Controls v6 и версия файла — как в install.sh (без манифеста трей/оверлей падают).
go run github.com/josephspurrier/goversioninfo/cmd/goversioninfo@v1.7.0 -64 -arm=false `
  -o cmd/agent/rsrc_windows_amd64.syso -manifest cmd/agent/agent.manifest `
  -ver-major $parts[0] -ver-minor $parts[1] -ver-patch $parts[2] -ver-build 0 `
  -product-ver-major $parts[0] -product-ver-minor $parts[1] -product-ver-patch $parts[2] -product-ver-build 0 `
  -file-version "$ver.0" -product-version $ver `
  -company RoutineOps -product-name "RoutineOps Agent" -description "RoutineOps Agent" `
  -internal-name RoutineOps-agent -original-name RoutineOps-agent.exe cmd/agent/versioninfo.json
if ($LASTEXITCODE -ne 0) { throw "goversioninfo failed" }

New-Item -ItemType Directory -Force build\windows | Out-Null
$out = "build\windows\agent_windows_amd64_vpx.exe"
try {
  go build -trimpath -buildvcs=false -tags $Tags `
    -ldflags "-s -w -X main.version=$ver -H windowsgui -extldflags '-static'" `
    -o $out ./cmd/agent
  if ($LASTEXITCODE -ne 0) { throw "go build failed" }
} finally {
  Remove-Item cmd\agent\rsrc_windows_amd64.syso -ErrorAction SilentlyContinue
}

$hash = (Get-FileHash $out -Algorithm SHA256).Hash.ToLower()
# Формат sha256sum -c: "<hash>  <имя>" без BOM и с LF.
[IO.File]::WriteAllText((Join-Path $root "$out.sha256"), "$hash  agent_windows_amd64_vpx.exe`n")
[IO.File]::WriteAllText((Join-Path $root "$out.version"), "v$ver")
Write-Host "Готово: $out (v$ver, sha256 $hash)"
Write-Host "Скопируйте на сервер в ~/RoutineOps/build/windows/ три файла: .exe, .exe.sha256, .exe.version, затем:"
Write-Host "  cd ~/RoutineOps && git pull && SKIP_DARWIN=1 ./install.sh"
