import { useCallback, useEffect, useRef, useState } from "react"
import { useTranslation } from "react-i18next"

import { Button } from "@/components/ui/button"
import axios from "axios"

import api, { errMessage, errStatus } from "@/lib/api"
import { formatBytes } from "@/lib/format"
import { toast } from "@/lib/toast"

// Загрузка Windows-агента с VP8 (собирается на ПК скриптом build-windows-vpx.ps1).
// Карточка видна только суперадмину из SEED_ADMIN_EMAIL: для остальных сервер отвечает 403,
// и мы её просто не рисуем. Файл лишь кладётся на сервер; подписывает и публикует его
// следующий запуск install.sh — ключ подписи в контейнер сервера не монтируется.

interface BuildInfo {
  present: boolean
  version?: string
  sha256?: string
  bytes?: number
  uploaded_at?: string
}

// Сервер отвечает {"error":"код"}; общий errMessage его не показывает, а здесь код — это и есть диагноз.
function uploadError(e: unknown): string {
  if (axios.isAxiosError(e)) {
    const code = (e.response?.data as { error?: string } | undefined)?.error
    if (code) return `${e.response?.status}: ${code}`
  }
  return errMessage(e)
}

export default function AgentBuildUpload() {
  const { t } = useTranslation()
  const [info, setInfo] = useState<BuildInfo | null>(null)
  const [allowed, setAllowed] = useState(false)
  const [version, setVersion] = useState("")
  const [file, setFile] = useState<File | null>(null)
  const [progress, setProgress] = useState<number | null>(null)
  const input = useRef<HTMLInputElement>(null)

  const load = useCallback(async () => {
    try {
      const r = await api.get<BuildInfo>("/agent-builds/windows-vpx")
      setInfo(r.data)
      setAllowed(true)
    } catch (e) {
      if (errStatus(e) !== 403 && errStatus(e) !== 404) toast({ title: errMessage(e), variant: "destructive" })
      setAllowed(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  if (!allowed) return null

  const upload = async () => {
    if (!file || !version.trim()) return
    const fd = new FormData()
    // version раньше file: сервер читает поток и должен знать версию до файла.
    fd.append("version", version.trim())
    fd.append("file", file)
    setProgress(0)
    try {
      const r = await api.post<BuildInfo>("/agent-builds/windows-vpx", fd, {
        timeout: 0,
        onUploadProgress: (ev) => {
          if (ev.total) setProgress(Math.round((ev.loaded / ev.total) * 100))
        },
      })
      setInfo(r.data)
      setFile(null)
      if (input.current) input.current.value = ""
      toast({ title: t("screenAccess.build.done"), variant: "success" })
    } catch (e) {
      toast({ title: uploadError(e), variant: "destructive" })
    } finally {
      setProgress(null)
    }
  }

  return (
    <section className="space-y-3">
      <div>
        <h2 className="text-lg font-semibold">{t("screenAccess.build.title")}</h2>
        <p className="text-sm text-muted-foreground">{t("screenAccess.build.hint")}</p>
      </div>
      <div className="text-sm">
        {info?.present
          ? t("screenAccess.build.current", {
              version: (info.version ?? "?").replace(/^v/, ""),
              size: formatBytes(info.bytes ?? 0),
              hash: (info.sha256 ?? "").slice(0, 12),
            })
          : t("screenAccess.build.none")}
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <input
          className="h-9 w-32 rounded-md border bg-background px-2 text-sm"
          placeholder="2.6.23"
          value={version}
          onChange={(e) => setVersion(e.target.value)}
          aria-label={t("screenAccess.build.version")}
        />
        <input
          ref={input}
          type="file"
          accept=".exe"
          className="text-sm"
          onChange={(e) => setFile(e.target.files?.[0] ?? null)}
        />
        <Button size="sm" disabled={!file || !version.trim() || progress !== null} onClick={() => void upload()}>
          {progress !== null ? `${progress}%` : t("screenAccess.build.upload")}
        </Button>
      </div>
    </section>
  )
}
