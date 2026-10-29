# Construirea agentului cu VP8 (libvpx)

Etapa B, pasul B1 (`REMOTE_DESKTOP_B_DESIGN.md` §6). Codorul VP8 se află în
`internal/agent/screen/encoder_vpx.go` și intră în build doar cu **toate** condițiile:

```
-tags "screen vpx"   +   CGO_ENABLED=1   +   libvpx vizibil prin pkg-config
```

Fără ele codecul nu se înregistrează: `NewVideoEncoder("vp8", …)` întoarce `ErrCodecUnavailable`,
`VideoCapabilities()` e gol, iar agentul rămâne pe `RSF1`. Asta e intenționat: agentul nu anunță
`screen_webrtc_vp8` dacă nu poate coda.

## Linux

```sh
sudo apt install libvpx-dev pkg-config gcc     # Debian/Ubuntu
make test-vpx
```

Static: `PKG_CONFIG="pkg-config --static" CGO_LDFLAGS="-static" …` (necesită `libvpx.a`).

## Windows (MSYS2)

Toolchain-ul trebuie să fie de aceeași arhitectură ca Go (`go env GOARCH`): `amd64` → UCRT64,
`386` → MINGW32.

```sh
pacman -S mingw-w64-ucrt-x86_64-{gcc,libvpx,pkgconf}
export PATH=/ucrt64/bin:$PATH CGO_ENABLED=1
make test-vpx
```

Pentru un `.exe` fără DLL-uri: `PKG_CONFIG="pkg-config --static"` și `-ldflags "-extldflags -static"`.

## macOS

```sh
brew install libvpx pkg-config
make test-vpx
```

## Ce verifică testele

- `encoder_test.go` (fără CGO): conversia RGBA/BGRA → I420 (BT.601, geometrie impară,
  stride DXGI), parsarea antetului VP8, corespondența capabilitate ↔ codor.
- `encoder_vpx_test.go` (cu libvpx): cadre sintetice 320×240 și 321×241, primul cadru cheie
  cu dimensiunile corecte, cadre inter după el, `forceKey`, ecran static ieftin, `SetBitrate`.
