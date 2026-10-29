import { useEffect } from "react"
import { useParams } from "react-router-dom"
import { useTranslation } from "react-i18next"
import { ScreenSessionPanel } from "@/components/ScreenSessionPanel"

// Отдельное окно с экраном устройства (открывается кнопкой «В окне» в карточке). Без бокового
// меню и шапки: всё место отдано картинке, а окно можно растянуть, развернуть на весь
// монитор или унести на другой.
export default function ScreenWindow() {
  const { deviceId, sessionId } = useParams()
  const { t } = useTranslation()
  useEffect(() => {
    document.title = t("screenSession.windowTitle")
  }, [t])
  if (!deviceId || !sessionId) return null
  return (
    <div className="min-h-screen bg-background">
      <ScreenSessionPanel deviceId={deviceId} attachId={sessionId} windowed />
    </div>
  )
}
