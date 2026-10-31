import { useCallback, useEffect, useRef, useState } from "react"
import { useTranslation } from "react-i18next"
import api from "@/lib/api"
import { Button } from "@/components/ui/button"
import { parseRecords, type FrameRecord } from "@/lib/screenframe"
import { FrameRenderer } from "@/lib/frameRenderer"

// Проигрыватель записи сеанса прямо в панели. Запись — тот же поток RSF1, что отдаёт
// /live ([u32 len][u32 rel_ms][часть кадра]), поэтому разбор и отрисовка общие с
// живым просмотром. Загрузка идёт через тот же эндпоинт, что и скачивание: грант и
// запись факта просмотра в аудит проверяет сервер.

const SPEEDS = [1, 2, 4, 8]
// Паузы длиннее этого на экране без изменений в записи сжимаются, чтобы просмотр не
// «зависал» на минуты у неподвижного рабочего стола.
const MAX_GAP_MS = 1500

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms))

function fmt(ms: number): string {
  const s = Math.max(0, Math.floor(ms / 1000))
  const m = Math.floor(s / 60)
  return `${m}:${String(s % 60).padStart(2, "0")}`
}

interface Props {
  sessionId: string
  onClose: () => void
}

export default function RecordingPlayer({ sessionId, onClose }: Props) {
  const { t } = useTranslation()
  const canvasRef = useRef<HTMLCanvasElement>(null)
  // Отрисовщик: JPEG-тайлы и видео VP8 (запись того же потока). Создаётся на первом кадре.
  const rendererRef = useRef<FrameRenderer | null>(null)
  const recsRef = useRef<FrameRecord[]>([])
  const cursorRef = useRef(0)
  const tokenRef = useRef(0)
  const speedRef = useRef(1)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState("")
  const [playing, setPlaying] = useState(false)
  const [speed, setSpeed] = useState(1)
  const [pos, setPos] = useState(0)
  const [duration, setDuration] = useState(0)

  const draw = useCallback(async (rec: FrameRecord) => {
    const canvas = canvasRef.current
    if (!canvas) return
    const { width, height } = rec.part
    if (width > 0 && height > 0 && (canvas.width !== width || canvas.height !== height)) {
      canvas.width = width
      canvas.height = height
    }
    if (!rendererRef.current) rendererRef.current = new FrameRenderer(canvas)
    await rendererRef.current.draw(rec.part)
  }, [])

  const play = useCallback(async () => {
    const recs = recsRef.current
    if (cursorRef.current >= recs.length) cursorRef.current = 0
    const my = ++tokenRef.current
    setPlaying(true)
    let shift = 0
    let baseWall = performance.now()
    let baseMs = recs[cursorRef.current]?.relMs ?? 0
    let prevMs = baseMs
    if (cursorRef.current === 0) {
      // Начало записи: чистый холст, дальше только дельты от ключевого кадра.
      const c = canvasRef.current
      c?.getContext("2d")?.clearRect(0, 0, c.width, c.height)
      rendererRef.current?.reset()
    }
    while (cursorRef.current < recs.length && my === tokenRef.current) {
      const rec = recs[cursorRef.current]
      const gap = rec.relMs - prevMs
      if (gap > MAX_GAP_MS) shift += gap - MAX_GAP_MS
      prevMs = rec.relMs
      const due = (rec.relMs - shift - baseMs) / speedRef.current
      const wait = due - (performance.now() - baseWall)
      if (wait > 4) await sleep(wait)
      if (my !== tokenRef.current) return
      try {
        await draw(rec)
      } catch {
        setError(t("screenSession.brokenStream"))
        break
      }
      if (my !== tokenRef.current) return
      cursorRef.current++
      setPos(rec.relMs)
    }
    if (my === tokenRef.current) setPlaying(false)
  }, [draw, t])

  const pause = useCallback(() => {
    tokenRef.current++
    setPlaying(false)
  }, [])

  // Перемотка: назад до начала ключевого кадра, дальше быстро применяем дельты без пауз.
  const seek = useCallback(
    async (ms: number, resume: boolean) => {
      const recs = recsRef.current
      const my = ++tokenRef.current
      let target = -1
      for (let i = 0; i < recs.length && recs[i].relMs <= ms; i++) target = i
      if (target < 0) target = 0
      let j = target
      while (j > 0 && !recs[j].part.keyframe) j--
      while (j > 0 && recs[j - 1].part.keyframe) j--
      // Видео декодируется заново с ключевого кадра: прежнее состояние декодера не годится.
      rendererRef.current?.reset()
      try {
        for (let k = j; k <= target; k++) {
          if (my !== tokenRef.current) return
          await draw(recs[k])
        }
      } catch {
        setError(t("screenSession.brokenStream"))
        return
      }
      cursorRef.current = target + 1
      setPos(recs[target]?.relMs ?? 0)
      if (resume) void play()
      else setPlaying(false)
    },
    [draw, play, t],
  )

  useEffect(() => {
    let dropped = false
    void (async () => {
      try {
        const res = await api.get<ArrayBuffer>(`/screen-sessions/${sessionId}/recording`, {
          responseType: "arraybuffer",
          timeout: 10 * 60 * 1000,
        })
        if (dropped) return
        const recs = parseRecords(new Uint8Array(res.data))
        if (recs.length === 0) {
          setError(t("screenSession.recordingEmpty"))
          setLoading(false)
          return
        }
        recsRef.current = recs
        setDuration(recs[recs.length - 1].relMs)
        setLoading(false)
        void play()
      } catch (e) {
        if (dropped) return
        const status = (e as { response?: { status?: number } }).response?.status
        setError(status === 403 ? t("screenSession.recordingForbidden") : t("screenSession.brokenStream"))
        setLoading(false)
      }
    })()
    return () => {
      dropped = true
      tokenRef.current++
      rendererRef.current?.close()
      rendererRef.current = null
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sessionId])

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && onClose()
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  }, [onClose])

  return (
    <div className="fixed inset-0 z-50 flex flex-col bg-black/95">
      <div className="flex min-h-0 flex-1 items-center justify-center p-2">
        {loading && <p className="text-sm text-white/70">{t("screenSession.recordingLoading")}</p>}
        {error && <p className="text-sm text-amber-300">{error}</p>}
        <canvas
          ref={canvasRef}
          className={loading || error ? "hidden" : "max-h-full max-w-full bg-black"}
        />
      </div>
      <div className="flex items-center gap-3 bg-black px-4 py-2 text-white">
        <Button
          size="sm"
          variant="outline"
          className="border-white/30 bg-transparent text-white hover:bg-white/15 hover:text-white"
          disabled={loading || !!error}
          onClick={() => (playing ? pause() : void play())}
        >
          {playing ? t("screenSession.recordingPause") : t("screenSession.recordingPlay")}
        </Button>
        <span className="w-24 text-center text-xs tabular-nums text-white/70">
          {fmt(pos)} / {fmt(duration)}
        </span>
        <input
          type="range"
          className="flex-1 accent-[hsl(var(--brand))]"
          min={0}
          max={Math.max(duration, 1)}
          value={pos}
          disabled={loading || !!error}
          onChange={(e) => {
            const v = Number(e.target.value)
            setPos(v)
            void seek(v, playing)
          }}
        />
        <select
          className="rounded border border-white/30 bg-transparent px-1 py-1 text-xs"
          value={speed}
          onChange={(e) => {
            const v = Number(e.target.value)
            speedRef.current = v
            setSpeed(v)
            if (playing) void seek(pos, true)
          }}
        >
          {SPEEDS.map((s) => (
            <option key={s} value={s} className="text-black">
              {s}×
            </option>
          ))}
        </select>
        <Button
          size="sm"
          variant="outline"
          className="border-white/30 bg-transparent text-white hover:bg-white/15 hover:text-white"
          onClick={onClose}
        >
          {t("screenSession.recordingClose")}
        </Button>
      </div>
    </div>
  )
}
