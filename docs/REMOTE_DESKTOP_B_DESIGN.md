# Etapa B: remote desktop cu video și WebRTC (design)

Continuare a `REMOTE_DESKTOP_PLAN.md`. **Stare: doar design, necompilat.** Mediul în care a fost scris nu avea Go 1.26.4 și nici acces la module, deci nimic de aici nu a rulat. Mesajele proto sunt într-un bloc de cod, nu în `agent.proto`, ca să nu strice build-ul (fișierele `*.pb.go` trebuie regenerate cu `buf`).

## 1. Obiectiv
Înlocuim dalele JPEG (`RSF1`) cu video codat, trimis prin WebRTC, păstrând tot ce garantează contractul existent: identitate doar din mTLS, audit înainte de primul cadru, înregistrare pe server, consimțământ și plăcuță permanentă, sfârșit de sesiune comun pentru cadre și input.

## 2. Alegeri

| Subiect | Decizie | De ce |
|---|---|---|
| Topologie | **Model RustDesk, în forma standard WebRTC**: serverul are rolul de rendezvous (semnalizare pe gRPC, ca `hbbs`) și de relay (SFU-lite, ca `hbbr`). Politică pe tenant: `relay_only` (implicit, cu înregistrare pe server) sau `p2p_preferred` (conexiune directă prin ICE, cu relay ca rezervă) | ICE face același lucru ca hole punching-ul din RustDesk, iar un browser nu poate vorbi un protocol UDP propriu. P2P nu poate fi înregistrat pe server, deci `relay_only` rămâne implicit acolo unde auditul cere înregistrare. |
| Codec inițial | **VP8** | Royalty-free, merge în toate browserele, înregistrarea în `.webm` se redă direct în `<video>` (pion are `webmwriter`). Cu `tune-content=screen` și profil mic (15 fps) CPU-ul e rezonabil. |
| Codec ulterior | **H.264** cu encodere hardware (Media Foundation, VideoToolbox, VAAPI) | Mai puțin CPU, dar înregistrarea cere mux MP4 și licențe pe sisteme. Vine după VP8. |
| Encoder | Interfață `Encoder` înlocuibilă; implementare `libvpx` prin CGO sub tag `vpx` | Fără libvpx agentul cade pe `RSF1`. Nu există encoder VP8/H.264 bun în Go pur. |
| Input | Rămâne pe stream-ul gRPC (`SessionInput`) | Contractul cere un singur moment de moarte pentru cadre și input. Browserul trimite prin DataChannel către server, serverul îl pune în `SessionDown.input`. |
| Rezervă | `RSF1` pentru agenți vechi și rețele fără UDP | Serverul alege transportul la crearea sesiunii. |

## 3. Extinderi de protocol (compatibile înapoi)
Numere de câmp noi, deci agenții vechi le ignoră.

```proto
// ScreenSessionCommand: câmp nou 11
string transport = 11;      // "rsf1" (implicit) | "webrtc"
string video_codec = 12;    // "vp8" | "h264"; gol pentru rsf1

// SessionUp: oneof msg, câmp nou 8
SessionSignal signal = 8;

// SessionDown: oneof msg, câmp nou 4
SessionSignal signal = 4;

message SessionSignal {
  oneof msg {
    string sdp_offer = 1;       // agent -> server
    string sdp_answer = 2;      // server -> agent
    IceCandidate ice = 3;       // ambele sensuri
    bool   keyframe_request = 4; // server -> agent (echivalent PLI)
  }
}
message IceCandidate {
  string candidate = 1;
  string sdp_mid = 2;
  uint32 sdp_mline_index = 3;
}
```

Agentul anunță `screen_webrtc_vp8` în `devices.capabilities` (coloana există, migrarea 067). Serverul alege `webrtc` doar dacă agentul are capabilitatea și browserul declară suport; altfel `rsf1`.

## 4. Fluxul unei sesiuni
1. Operatorul creează sesiunea (`POST /devices/{id}/screen-sessions`). Serverul scrie rândul de sesiune și rândul de audit, apoi trimite `ScreenSessionCommand{transport:"webrtc"}` prin `Connect`.
2. Agentul deschide stream-ul `ScreenSession` (conexiune separată) și trimite `SessionHello`.
3. Serverul răspunde `SessionAccepted`; abia apoi agentul pornește captura.
4. Agentul creează PeerConnection, trimite `sdp_offer` în `SessionSignal`; serverul răspunde cu `sdp_answer`; se schimbă candidați ICE pe același stream.
5. Agentul trimite pista video VP8. Serverul o retransmite fiecărui browser (PeerConnection separată) și o scrie în `.webm`.
6. Browserul trimite input pe DataChannel către server; serverul îl transmite agentului ca `SessionInput`.
7. Spectator nou sau pierdere de pachete: serverul trimite `keyframe_request` agentului.

## 5. Server (`internal/server/screen/`)
- `rtc.go`: `pion/webrtc/v4`, `SettingEngine` cu `SetICEUDPMux` (un singur port UDP, de ex. `50052/udp`) și mux ICE-TCP ca rezervă; `SetNAT1To1IPs` din `PUBLIC_ADDR` (există deja la instalare).
- `sfu.go`: o pistă de intrare, `TrackLocalStaticRTP` per spectator; citește RTCP (PLI/FIR) și le mapează pe `keyframe_request`.
- `recorder.go`: `webmwriter` pentru VP8; aceeași cotă pe tenant și același `truncated` ca la RSF1.
- Rute noi: `POST /screen-sessions/{id}/offer` (browser → server) și DataChannel pentru input.
- Panoul web: componentă nouă care redă `<video>` din WebRTC; `ScreenSessionPanel.tsx` păstrează calea RSF1 ca rezervă.

## 6. Agent (`internal/agent/screen/`)
- `encoder.go`: interfața `Encoder{ Encode(frame I420, forceKey bool) ([]byte, error); Close() }`.
- `encoder_vpx.go` (`//go:build vpx`, CGO): libvpx, `tune-content=screen`, bitrate adaptiv pe baza RTCP.
- `rtc.go`: PeerConnection către server, pista video, DataChannel opțional dezactivat (input vine pe gRPC).
- Captura, input-ul, managerul de sesiune și codurile de încheiere: cele din `REMOTE_DESKTOP_PLAN.md` §3.2, neschimbate.
- Build: libvpx static. Pe Windows prin MSYS2/vcpkg, pe macOS prin Homebrew sau build propriu; documentat în `docs/`.

## 7. Securitate
- Semnalizarea merge pe stream-ul gRPC autentificat mTLS, deci agentul verifică amprenta DTLS a serverului prin canal de încredere.
- `session_id` nu autorizează niciodată; PeerConnection-ul se leagă de sesiunea al cărei `device_id` coincide cu CN-ul certificatului.
- Auditul se comite înainte de `SessionAccepted`; plăcuța și consimțământul rămân neschimbate.
- Serverul nu acceptă ofertă SDP de la un browser fără sesiune activă și drept de vizualizare (RBAC existent).

## 8. Etape de implementare

| Pas | Ce | Unde se poate testa |
|---|---|---|
| B0 | Regenerare proto (`buf`), câmpurile din §3 | orice mașină cu `buf` |
| B1 | `Encoder` + libvpx, test pe cadre sintetice | Linux/Windows cu libvpx |
| B2 | Server: PeerConnection, SFU-lite, `webm` | local |
| B3 | Agent Linux X11 → VP8 → browser | Linux sau Xvfb |
| B4 | Windows: DXGI + VP8 | PC Windows |
| B5 | Input prin DataChannel → gRPC | după B3 |
| B6 | macOS, H.264 hardware, ajustări de bitrate | ulterior |

## 9. Riscuri
- **libvpx prin CGO** complică build-ul agentului pe cele trei sisteme (cel mai mare cost al etapei).
- **UDP spre server:** rețelele care blochează UDP au nevoie de ICE-TCP sau cad pe `RSF1`.
- **CPU pe agent:** VP8 software la rezoluții mari încarcă mașina; profilul `low` rămâne implicit.
- **Wayland, UAC/Winlogon, TCC pe macOS:** aceleași limite ca în Etapa A.

## 10. Corespondența cu RustDesk și rolul Guacamole

**RustDesk este modelul principal**, doar că adaptat unui panou în browser. Nu copiem cod (AGPL-3.0); copiem arhitectura.

| RustDesk | La noi |
|---|---|
| `hbbs` (rendezvous, hole punching) | Semnalizare pe stream-ul gRPC mTLS + ICE (WebRTC) |
| `hbbr` (relay când P2P nu merge) | SFU-lite pe server (și TURN, dacă e nevoie) |
| Client nativ, protocol propriu pe TCP/UDP | Browserul (WebRTC). Un client nativ cu același protocol rămâne posibil într-o etapă ulterioară |
| Criptare end-to-end cu libsodium | DTLS-SRTP, cu amprenta verificată prin canalul mTLS |
| Codecuri VP9/AV1/H.264, encodere hardware | VP8 întâi, apoi H.264 hardware; VP9/AV1 când encoderul permite |

De ce WebRTC și nu un protocol UDP propriu: browserul nu poate deschide socket-uri UDP arbitrare, iar WebRTC este singura cale standard către P2P și relay din browser.

**Guacamole este planul de rezervă**, ca transport `guacamole` lângă `rsf1` și `webrtc`:
- `guacd` rulează în `docker-compose`; serverul vorbește protocolul Guacamole cu el și îl trimite browserului pe WebSocket.
- Fiindcă `guacd` nu are agent, e nevoie de un tunel TCP invers pornit de agent prin canalul gRPC existent.
- Folosește RDP real pe Windows (cont și parolă, sesiune separată) și VNC/`xrdp` pe celelalte sisteme; nu înlocuiește supravegherea ecranului utilizatorului.
- Se implementează după B3, ca opțiune de rezervă pentru dispozitive unde WebRTC nu poate fi folosit.
