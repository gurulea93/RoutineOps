import { useEffect, useState } from "react"
import { useTranslation } from "react-i18next"
import api, { errMessage } from "@/lib/api"
import { Button } from "@/components/ui/button"

// Запись сеанса WebRTC — обычный .webm, играется прямо в <video>. Загрузка идёт через тот же
// эндпоинт, что и скачивание: грант на просмотр и запись факта в аудит проверяет сервер.
export default function RecordingVideo({ sessionId, onClose }: { sessionId: string; onClose: () => void }) {
  const { t } = useTranslation()
  const [url, setUrl] = useState("")
  const [error, setError] = useState("")

  useEffect(() => {
    let dropped = false
    let objectUrl = ""
    api
      .get(`/screen-sessions/${sessionId}/recording`, { responseType: "blob" })
      .then((r) => {
        if (dropped) return
        objectUrl = URL.createObjectURL(new Blob([r.data as Blob], { type: "video/webm" }))
        setUrl(objectUrl)
      })
      .catch((e) => !dropped && setError(errMessage(e)))
    return () => {
      dropped = true
      if (objectUrl) URL.revokeObjectURL(objectUrl)
    }
  }, [sessionId])

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && onClose()
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  }, [onClose])

  return (
    <div className="fixed inset-0 z-50 flex flex-col bg-black/95">
      <div className="flex min-h-0 flex-1 items-center justify-center p-2">
        {!url && !error && <p className="text-sm text-white/70">{t("screenSession.recordingLoading")}</p>}
        {error && <p className="text-sm text-amber-300">{error}</p>}
        {url && <video src={url} controls autoPlay className="max-h-full max-w-full bg-black" />}
      </div>
      <div className="flex justify-end bg-black px-4 py-2">
        <Button size="sm" variant="outline" className="border-white/30 bg-transparent text-white hover:bg-white/15 hover:text-white" onClick={onClose}>
          {t("screenSession.recordingClose")}
        </Button>
      </div>
    </div>
  )
}
