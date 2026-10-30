# Build the Windows agent with the VP8 video encoder (libvpx via CGO) for the WebRTC remote desktop mode.
#
# The server image builds agents with CGO_ENABLED=0, so VP8 is impossible there. This script builds
# the agent on a PC with MSYS2 (UCRT64: gcc, pkg-config, libvpx) and puts it into build/windows/. Then
# copy the files to the server; install.sh publishes them instead of the regular build (if version
# and sha256 match).
#
# Once, in MSYS2 UCRT64:
#   pacman -S --needed mingw-w64-ucrt-x86_64-gcc mingw-w64-ucrt-x86_64-pkgconf mingw-w64-ucrt-x86_64-libvpx
#
# Run (PowerShell, from the repository root):
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
if (-not (Test-Path (Join-Path $bin "gcc.exe"))) { throw "gcc not found in $bin - install the MSYS2 UCRT64 packages (see the script header)" }
$env:PATH = "$bin;$env:PATH"
$env:PKG_CONFIG_PATH = Join-Path $Msys2 "ucrt64\lib\pkgconfig"
$env:CGO_ENABLED = "1"
$env:GOOS = "windows"
$env:GOARCH = "amd64"

& pkg-config --exists vpx
if ($LASTEXITCODE -ne 0) { throw "pkg-config cannot see libvpx: pacman -S mingw-w64-ucrt-x86_64-libvpx" }

# Common-Controls v6 manifest and file version, same as install.sh (without the manifest the tray/overlay crash).
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
# sha256sum -c format: hash, two spaces, file name; no BOM, LF line ending.
[IO.File]::WriteAllText((Join-Path $root "$out.sha256"), "$hash  agent_windows_amd64_vpx.exe`n")
[IO.File]::WriteAllText((Join-Path $root "$out.version"), "v$ver")
Write-Host "Done: $out (v$ver, sha256 $hash)"

# The installer (MSI) must carry the same agent, otherwise fresh installs start on an old
# version. Build it here in the same run, so there is no separate step to forget.
$msiOk = $false
try {
  & powershell -NoProfile -ExecutionPolicy Bypass -File (Join-Path $root "build\msi\build-msi.ps1") `
    -ExePath (Join-Path $root $out) -Version "$ver.0"
  $msiOk = ($LASTEXITCODE -eq 0)
} catch {
  Write-Warning "MSI build failed: $_"
}
if ($msiOk) { Write-Host "MSI: build\msi\RoutineOps-agent.msi (v$ver)" }
else { Write-Warning "MSI was NOT rebuilt - new installs will start on an older agent until it is." }

Write-Host ""
Write-Host "Next:"
Write-Host "  1. Upload $out in the panel (Screen access, Windows agent with VP8), version $ver"
Write-Host "  2. git add build/msi; git commit -m 'MSI $ver'; git push origin HEAD:main"
Write-Host "  3. On the server: cd ~/RoutineOps && git pull && SKIP_DARWIN=1 ./install.sh"
