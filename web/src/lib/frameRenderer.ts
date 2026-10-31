// Отрисовка потока кадров на холст: JPEG-тайлы (RSF1) и видео VP8 в том же потоке.
//
// VP8 идёт тем же путём, что и тайлы (HTTPS), а декодирует его браузер через WebCodecs —
// схема RustDesk: никакого UDP, работает везде, где открывается панель. Кадр VP8 приходит
// кусками в частях одного seq; склеенные по порядку, они — один кадр кодека.

import { drawPart, type FramePart } from "@/lib/screenframe"

// Сообщения здесь по-английски: это диагностика в консоли, а не текст интерфейса.

/** vp8DecodeSupported — умеет ли браузер декодировать VP8 сам (WebCodecs). */
export const vp8DecodeSupported: Promise<boolean> = (async () => {
  try {
    if (typeof VideoDecoder === "undefined") return false
    const r = await VideoDecoder.isConfigSupported({ codec: "vp8" })
    return Boolean(r.supported)
  } catch {
    return false
  }
})()

export class FrameRenderer {
  private decoder: VideoDecoder | null = null
  private needKey = true
  private chunks: Uint8Array[] = []
  private chunkSeq = -1
  private ts = 0

  /**
   * @param canvas холст
   * @param onSize геометрия видео сменилась (холст уже подогнан)
   * @param onNeedKey декодеру нужен ключевой кадр (ошибка или пропуск) — вызывающий
   *   переоткрывает поток с начала буфера сервера (там всегда ключевой кадр)
   */
  constructor(
    private canvas: HTMLCanvasElement,
    private onSize?: (w: number, h: number) => void,
    private onNeedKey?: () => void,
  ) {}

  /** reset — следующая часть VP8 должна начинаться с ключевого кадра (перемотка, новый поток). */
  reset() {
    this.chunks = []
    this.chunkSeq = -1
    this.needKey = true
    if (this.decoder && this.decoder.state !== "closed") {
      try {
        this.decoder.reset()
      } catch {
        this.closeDecoder()
      }
    }
  }

  close() {
    this.chunks = []
    this.closeDecoder()
  }

  async draw(part: FramePart): Promise<void> {
    if (!part.vp8) {
      const ctx = this.canvas.getContext("2d")
      if (ctx) await drawPart(ctx, part)
      return
    }
    if (part.seq !== this.chunkSeq) {
      this.chunks = []
      this.chunkSeq = part.seq
    }
    for (const t of part.tiles) this.chunks.push(t.jpeg)
    if (!part.last) return

    const size = this.chunks.reduce((n, c) => n + c.byteLength, 0)
    const data = new Uint8Array(size)
    let off = 0
    for (const c of this.chunks) {
      data.set(c, off)
      off += c.byteLength
    }
    this.chunks = []

    if (this.needKey && !part.keyframe) return // дельта без ключевого кадра не декодируется
    const dec = this.ensureDecoder()
    if (!dec) return
    if (part.keyframe) this.needKey = false
    // Декодер не успевает (слабая машина, вкладка в фоне) — не копим очередь: ждём ключевой.
    if (dec.decodeQueueSize > 60) {
      this.needKey = true
      this.onNeedKey?.()
      return
    }
    try {
      this.ts += 33_333
      dec.decode(new EncodedVideoChunk({ type: part.keyframe ? "key" : "delta", timestamp: this.ts, data }))
    } catch (e) {
      console.warn("screen: vp8 decode failed", e)
      this.closeDecoder()
      this.needKey = true
      this.onNeedKey?.()
    }
  }

  private ensureDecoder(): VideoDecoder | null {
    if (this.decoder && this.decoder.state !== "closed") return this.decoder
    if (typeof VideoDecoder === "undefined") return null
    const dec = new VideoDecoder({
      output: (frame) => {
        try {
          const w = frame.displayWidth
          const h = frame.displayHeight
          if (this.canvas.width !== w || this.canvas.height !== h) {
            this.canvas.width = w
            this.canvas.height = h
            this.onSize?.(w, h)
          }
          this.canvas.getContext("2d")?.drawImage(frame, 0, 0)
        } finally {
          frame.close()
        }
      },
      error: (e) => {
        console.warn("screen: vp8 decoder error", e)
        this.decoder = null
        this.needKey = true
        this.onNeedKey?.()
      },
    })
    dec.configure({ codec: "vp8", optimizeForLatency: true })
    this.decoder = dec
    return dec
  }

  private closeDecoder() {
    const d = this.decoder
    this.decoder = null
    if (d && d.state !== "closed") {
      try {
        d.close()
      } catch {
        // уже закрыт
      }
    }
  }
}
