# Plan tehnic: remote desktop propriu pentru fork-ul RoutineOps

Document de lucru. Bazat pe codul din `main` (commit `e589ed9`, 28.10.2026).

## 1. Ce există deja și ce lipsește

Autorul ține remote desktop-ul în ediția Enterprise, dar **scheletul este public**. Asta scurtează mult drumul.

| Piesă | Stare în repo | Unde |
|---|---|---|
| Contract de protocol (gRPC) | ✅ complet | `proto/agent.proto`, liniile ~770–1185: `ScreenSession` (stream bidirecțional), `ReportScreenEvent`, `SessionUp/Down`, `InputEvent`, `ScreenSessionCommand` |
| Schema bazei de date | ✅ | `migrations/067`–`075` (`screen_sessions`, consimțământ, înregistrări, control) |
| Panou web (vizualizare, control, înregistrări) | ✅ ~1400 linii | `web/src/components/ScreenSessionPanel.tsx`, `web/src/lib/screenframe.ts`, `screeninput.ts`, `pages/ScreenAccess.tsx` |
| Cârlig pe agent pentru invitație | ✅ | `internal/agent/command/executor.go` (`ScreenSessioner`, `SetScreenSessioner`) |
| Cârlig pe server | ✅ | `internal/server/gateway/screen_seam.go` (interfața `ScreenService`) |
| Lansare în sesiunea utilizatorului (Windows) | ✅ parțial | `internal/agent/winsession/` |
| **Captură ecran + codare + injectare input (agent)** | ❌ lipsește | `internal/agent/screen/` (doar `doc_free.go`) |
| **Format cadru, partea Go** | ❌ lipsește | `internal/screenframe/` (doar `doc_free.go` și `testdata/golden.bin`, 64 B) |
| **Serviciu server: relay, sesiuni, înregistrare, rute REST** | ❌ lipsește | `internal/server/screen/` (nu există) |
| Cablare agent | ❌ stub | `cmd/agent/screen_stub.go` (returnează `nil`) |
| Cablare server | ❌ stub | `cmd/server/enterprise_stub.go` |
| Documentul de contract | ❌ referit, dar absent | `docs/remote-desktop-contract.md` |

Concluzie: nu proiectăm de la zero. **Implementăm cele patru piese lipsă pe un contract care există deja**, iar panoul web funcționează fără modificări mari.

## 2. Decizie de arhitectură: două etape

**Etapa A (recomandată întâi): implementăm contractul existent.**
- Transport: agent → server → browser, relay prin server (fără P2P). Contractul cere asta explicit, pentru ca înregistrarea sesiunii să fie făcută de partea nesupravegheată.
- Format cadru `RSF1`: dale (tiles) JPEG cu dreptunghiuri modificate. Specificația este în antetul `web/src/lib/screenframe.ts`.
- Avantaj: panoul, baza de date și protocolul sunt gata, deci avem repede ceva funcțional cap-coadă.

**Etapa B (după ce A merge): plan media propriu, mai rapid, în stil RustDesk.**
- Video H.264/VP9 sau AV1 în loc de dale JPEG, transport WebRTC (`pion`) cu relay prin server ca rezervă și P2P opțional.
- Semnalizarea trece prin canalul gRPC existent.
- Se adaugă mesaje noi în proto, fără a rupe vechiul contract (agenții vechi rămân pe RSF1).
- Atenție: P2P intră în conflict cu regula „înregistrare pe server". Rămâne relay implicit; P2P doar ca mod explicit, fără înregistrare, activat din setările tenantului.

Nu copiem cod din RustDesk (AGPL-3.0, incompatibil cu Apache-2.0 al proiectului). Îl folosim doar ca model de arhitectură.

## 3. Ce trebuie scris (Etapa A)

### 3.1 `internal/screenframe/frame.go`
- Codor și decodor `RSF1` identice octet cu octet cu parserul din TypeScript:
  antet `[u32 magic][u32 seq][u16 flags][u16 ntiles][u16 w][u16 h]`, apoi dale `[u16 x][u16 y][u16 w][u16 h][u32 len][JPEG]`.
- Test de conformitate pe `testdata/golden.bin`, același fișier pe care îl verifică `screenframe.test.ts`.
- Tăiere în părți de 256 KiB (limita gRPC este 4 MiB, nu se modifică).

### 3.2 `internal/agent/screen/`
- `manager.go`: implementează `command.ScreenSessioner` (`StartSession`). Deschide al doilea canal gRPC (conexiune separată, ca să nu blocheze heartbeat-ul). Trimite `SessionHello`, așteaptă `SessionAccepted`, apoi cadre.
- `capture_linux_x11.go`: `xgb` este deja în `go.mod`. Cel mai simplu de testat (Xvfb în workspace).
- `capture_windows.go`: Desktop Duplication API (DXGI), cu rezervă GDI. Rulează în sesiunea utilizatorului prin `winsession`.
- `capture_darwin.go`: ScreenCaptureKit sau CGDisplayStream; necesită CGO și permisiunea Screen Recording (TCC).
- `diff.go`: compară cu cadrul anterior, trimite doar dalele schimbate; cadru complet (keyframe) la cerere.
- `profile.go`: profilul `low` (rezoluție redusă, buget de bandă, „ultimul cadru câștigă").
- `input_windows.go` (`SendInput`), `input_darwin.go` (`CGEvent`), `input_linux_x11.go` (XTest). Coordonatele se convertesc pe agent din cadrul operatorului în pixeli reali.
- `reasons.go`: codurile de încheiere (`WAYLAND_UNSUPPORTED`, `NO_CONSOLE_USER`, `SCREEN_RECORDING_DENIED`, `INPUT_UNAVAILABLE` etc.).
- `cmd/agent/screen_wiring.go`: `wireScreen`, `capabilities`, `sessioner`, `holdUpdates`. Înlocuiește stub-ul.

### 3.3 `internal/server/screen/`
- `service.go`: implementează `gateway.ScreenService` (`Stream`, `Event`). Identitatea vine doar din CN-ul certificatului mTLS; `session_id` nu autorizează niciodată.
- `hub.go`: un agent per sesiune, mai mulți spectatori, tampon circular de cadre, cerere de keyframe la spectator nou.
- `handlers.go`, rutele pe care panoul le folosește deja:
  - `GET/POST /devices/{id}/screen-sessions`
  - `GET /screen-sessions/{id}/live` (long-poll cu `since`, antete `X-Screen-*`)
  - `POST /screen-sessions/{id}/input`
  - `DELETE /screen-sessions/{id}/control`
  - `POST /screen-sessions/{id}/stop`
  - `GET /screen-recording-grants/me`
- `recording.go`: fișier de înregistrare în formatul `[u32 len][u32 rel_ms][payload]`, cotă pe tenant.
- Audit fail-closed: fără rând de audit comis, agentul nu primește `SessionAccepted`.
- `cmd/server/enterprise.go` (sau fișier propriu): înregistrează serviciul și rutele, înlocuind `enterprise_stub.go`.

### 3.4 Build tag-uri
Fișierele noi vor fi sub un tag propriu sau fără tag. Gardurile existente (`make check-remote-tags`, `scripts/export-free.sh`) verifică absența funcției în build-ul „free" și vor da erori în fork; se ajustează sau se dezactivează.

### 3.5 Documentație
Scriem `docs/remote-desktop-contract.md` din comentariile din `agent.proto`, fiindcă este referit peste tot și nu există.

## 4. Ordinea de lucru și puncte de verificare

| Pas | Rezultat testabil |
|---|---|
| M0 | Stack-ul pornit local (`docker compose`, `install.sh`), un agent Linux înrolat |
| M1 | `screenframe` + hub pe server + captură Linux X11, doar vizualizare, cap-coadă în panou. Testabil în workspace cu Xvfb |
| M2 | Windows: captură DXGI + lansare în sesiunea utilizatorului |
| M3 | Input pe cele trei sisteme (Windows `SendInput`, Linux XTest, macOS `CGEvent`) |
| M4 | Înregistrare, consimțământ, audit, plăcuța „cineva vă vede ecranul" |
| M5 | Etapa B: video + WebRTC, P2P opțional |
| După | SIEM (syslog/CEF, webhook), apoi LDAP/AD |

**TODO M1 (teste rămase pentru `sendLoop`, `internal/agent/screen/session_send.go`):**

- pauza proporțională la un cadru peste buget, cu `stalls` propagat în `setStats`;
- compunerea a două cadre puse în `latestFrame` în timpul pauzei, trimise ca un singur cadru după ea;
- semnalul `Ready` fără cadru în `box`: nu se trimite nimic, iar bucla continuă.

**TODO M2 (lansarea pe Windows).** `internal/agent/winsession.LaunchInActiveSession` nu poate fi folosit încă pentru `screen-worker`:

- nu transmite procesului pipe-uri stdin/stdout. Trebuie moștenirea handle-urilor în `CreateProcessAsUser`, adică `bInheritHandles` plus `STARTF_USESTDHANDLES`, ideal cu `PROC_THREAD_ATTRIBUTE_HANDLE_LIST`, ca să se moștenească doar cele două pipe-uri;
- nu pune argumentele între ghilimele (`buildCmdLine`).

Până atunci `internal/agent/screen/session_launch_other.go` întoarce `PLATFORM_UNSUPPORTED` pe Windows. Canalul serviciu ↔ procesul de captură rămâne cel din `wire.go`; parametrii sesiunii trec ca prim mesaj (`msgConfig`), nu în linia de comandă.

## 5. Limite și riscuri
- **Wayland** nu permite captură/input fără portal (compositorul decide); rămâne refuz explicit cu cod, ca în design-ul original.
- **Windows**: serviciul rulează în sesiunea 0, fără acces la ecran; de aici `winsession`. UAC și ecranul de blocare cer atașare la desktopul Winlogon (rulare ca SYSTEM).
- **macOS**: permisiuni TCC (Screen Recording, Accessibility) cer acordul utilizatorului și build cu CGO.
- **RDP real** există doar pe Windows. Protocolul propriu este cel care funcționează uniform pe toate platformele.
- **Legal**: acces la calculatoarele angajaților. Menținem plăcuța permanentă de notificare și motivul obligatoriu, cum cere designul. Verifică cerințele de notificare din legislația din RM.

## 6. Decizii deschise
1. Ce platformă prioritară pentru demo: Windows sau Linux?
2. Relay doar prin server sau și P2P în Etapa B?
3. Tag de build propriu sau fără tag?
4. Unde ții fork-ul (cont GitHub), ca să putem face push?
