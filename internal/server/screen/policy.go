//go:build screen

package screen

import "strings"

// Политика транспорта тенанта (docs/REMOTE_DESKTOP_B_DESIGN.md §2, модель RustDesk).
//
// Сервер — rendezvous (сигнализация по gRPC, как hbbs) и relay (SFU-lite, как hbbr).
// relay_only — медиа всегда через сервер, запись делает сервер; это умолчание.
// p2p_preferred — агент и браузер соединяются напрямую через ICE, сервер только передаёт
// SDP. Прямой поток сервер записать не может, поэтому P2P ЗАПРЕЩЁН там, где тенант требует
// запись: аудит важнее задержки, и «предпочесть P2P» не может молча выключить запись.
type Policy string

const (
	PolicyRelayOnly    Policy = "relay_only"
	PolicyP2PPreferred Policy = "p2p_preferred"
)

// ParsePolicy читает политику из настроек тенанта. Пусто и неизвестное значение — relay_only:
// ошибка в настройке обязана давать режим С записью, а не без неё.
func ParsePolicy(s string) (p Policy, known bool) {
	switch Policy(strings.TrimSpace(s)) {
	case "", PolicyRelayOnly:
		return PolicyRelayOnly, true
	case PolicyP2PPreferred:
		return PolicyP2PPreferred, true
	default:
		return PolicyRelayOnly, false
	}
}

// Mode — как идут медиа в конкретном сеансе.
type Mode string

const (
	ModeRSF1  Mode = "rsf1"  // тайлы RSF1 по gRPC (Этап A)
	ModeRelay Mode = "relay" // WebRTC, SFU-lite на сервере
	ModeP2P   Mode = "p2p"   // WebRTC напрямую агент ↔ браузер
)

// Choice — решение о транспорте при создании сеанса. Transport/VideoCodec едут агенту в
// ScreenSessionCommand; Mode агенту не нужен вовсе: он в любом режиме шлёт оферту «на ту
// сторону сигнализации», а кто её примет — сервер или браузер — решает сервер.
type Choice struct {
	Transport  string // "rsf1" | "webrtc"
	VideoCodec string // "vp8" | ""
	Mode       Mode
	Record     bool // запись на сервере возможна (false только для P2P)
}

// ChooseTransport выбирает транспорт сеанса. agentCaps — devices.capabilities, browserCodecs —
// что объявил браузер ("vp8", ...). WebRTC — только при способности агента И поддержке
// браузера; иначе RSF1, который умеет любой агент.
func ChooseTransport(agentCaps, browserCodecs []string, policy Policy, recordingRequired bool) Choice {
	if !(has(agentCaps, "screen_webrtc_vp8") && has(browserCodecs, "vp8")) {
		return Choice{Transport: "rsf1", Mode: ModeRSF1, Record: true}
	}
	c := Choice{Transport: "webrtc", VideoCodec: "vp8", Mode: ModeRelay, Record: true}
	if policy == PolicyP2PPreferred && !recordingRequired {
		c.Mode, c.Record = ModeP2P, false
	}
	return c
}

func has(list []string, v string) bool {
	for _, s := range list {
		if strings.EqualFold(strings.TrimSpace(s), v) {
			return true
		}
	}
	return false
}
