//go:build screen

package screen

// EndReason — канонический список причин, с которыми агент заканчивает сеанс (SessionEnd.reason).
// Строки, а не enum (см. комментарий в proto/agent.proto): незнакомый второй стороне код
// обязан доехать текстом. Значения совпадают с web/src/lib/api.ts SCREEN_END_REASON там,
// где смысл тот же: панель переводит известный код, а незнакомый показывает как есть.
type EndReason string

const (
	// Нет графического сеанса, к которому можно подключиться (никто не вошёл, DISPLAY пуст).
	ReasonNoConsoleUser EndReason = "NO_CONSOLE_USER"
	// Сеанс Wayland: захват без портала невозможен, и честнее отказать, чем показывать чёрный
	// экран XWayland.
	ReasonWaylandUnsupported EndReason = "WAYLAND_UNSUPPORTED"
	// Захват на этой ОС/сервере не реализован (формат пикселей, порядок байт).
	ReasonCaptureUnsupported EndReason = "PLATFORM_UNSUPPORTED"
	// Захват работал и сломался посреди сеанса.
	ReasonCaptureFailed EndReason = "CAPTURE_FAILED"
	// Сервер прислал video_codec, которого в этой сборке нет. Не понижаемся молча до RSF1:
	// транспорт выбирает сервер.
	ReasonCodecUnavailable EndReason = "CODEC_UNAVAILABLE"
	// Кодер упал посреди сеанса.
	ReasonEncoderFailed EndReason = "ENCODER_FAILED"
	// PeerConnection не установился или порвался.
	ReasonTransportLost EndReason = "TRANSPORT_LOST"
	// Сигнализация нарушена (оферта вместо ответа, повторный ответ, битый SDP).
	ReasonSignalFailed EndReason = "SIGNAL_FAILED"
	// Сеанс закрыт владельцем (стоп оператора, конец стрима).
	ReasonStopped EndReason = "OPERATOR_STOPPED"
)

// ReasonError — ошибка с кодом причины: менеджер сеанса кладёт Reason в SessionEnd как есть.
type ReasonError struct {
	Reason EndReason
	Err    error
}

func (e *ReasonError) Error() string {
	if e.Err == nil {
		return string(e.Reason)
	}
	return string(e.Reason) + ": " + e.Err.Error()
}

func (e *ReasonError) Unwrap() error { return e.Err }

// Коды менеджера сеанса Этапа A (RSF1).
const (
	// Плашку «экран просматривается» показать не удалось — кадров не будет (§4: плашка
	// обязательна в обоих режимах и флага отключения не имеет).
	ReasonNoticeUnavailable EndReason = "NOTICE_UNAVAILABLE"
	// Режим consent_required: диалога согласия в этой версии нет, а молчаливый старт без
	// согласия хуже отказа.
	ReasonConsentUnavailable EndReason = "CONSENT_UNAVAILABLE"
	// Сеанс с управлением: внедрения ввода в этой версии нет.
	ReasonInputUnavailable EndReason = "INPUT_UNAVAILABLE"
	// На устройстве уже идёт сеанс.
	ReasonSessionBusy EndReason = "SESSION_BUSY"
	// Процесс захвата умер, не сказав почему.
	ReasonWorkerLost EndReason = "WORKER_LOST"
	// Нарушен протокол (служба ↔ захватчик или служба ↔ сервер).
	ReasonProtocolError EndReason = "PROTOCOL_ERROR"
	// Потолок длительности сеанса.
	ReasonMaxDuration EndReason = "MAX_DURATION"
	// Сеанс закрыт ради самообновления агента.
	ReasonAgentUpdate EndReason = "AGENT_UPDATE"
)

// Причины, с которыми сервер останавливает сеанс (SessionControl.stop_reason).
const (
	ReasonViewerGone      EndReason = "VIEWER_GONE"
	ReasonRecordingQuota  EndReason = "RECORDING_QUOTA"
	ReasonRecordingFailed EndReason = "RECORDING_FAILED"
	ReasonDeviceCutOff    EndReason = "DEVICE_CUT_OFF"
	// Оператор отозван, либо причина остановки сервером не названа или агенту не известна.
	ReasonOperatorRevoked EndReason = "OPERATOR_REVOKED"
)

// serverStopReasons — причины остановки, которые агент принимает от сервера как есть.
// Источник: что сервер реально шлёт в SessionControl.stop_reason (internal/server/screen:
// stop(...), hub.Stop(...), truncationEndReason) плюс серверные причины из контракта
// proto/agent.proto (DEVICE_CUT_OFF, OPERATOR_REVOKED). AGENT_GONE сюда не входит: это
// вывод сервера об агенте, присылать его агенту незачем.
var serverStopReasons = map[EndReason]bool{
	ReasonStopped:         true, // OPERATOR_STOPPED
	ReasonViewerGone:      true,
	ReasonMaxDuration:     true,
	ReasonProtocolError:   true,
	ReasonRecordingQuota:  true,
	ReasonRecordingFailed: true,
	ReasonDeviceCutOff:    true,
	ReasonOperatorRevoked: true,
}

func reasonErr(r EndReason, err error) error { return &ReasonError{Reason: r, Err: err} }
