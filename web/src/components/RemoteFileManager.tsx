import { useCallback, useEffect, useRef, useState } from "react"
import { useTranslation } from "react-i18next"
import { ArrowUp, Download, File, Folder, FolderPlus, HardDrive, Home, RefreshCw, Upload } from "lucide-react"

import api, { errMessage } from "@/lib/api"
import { Button } from "@/components/ui/button"
import { formatBytes } from "@/lib/format"
import { formatDateTime } from "@/lib/time"

// Файловый менеджер сеанса с управлением (как в RustDesk): папки машины сотрудника,
// загрузка туда (кнопкой или перетаскиванием) и скачивание оттуда. Файлы идут через сервер
// потоком; каждый перенос сервер пишет в аудит.

interface Entry {
  name: string
  dir: boolean
  size: number
  mtime: number
}

interface Transfer {
  key: number
  name: string
  up: boolean
  pct: number
  state: "run" | "ok" | "err"
  err?: string
}

function sepOf(p: string) {
  return p.includes("\\") || /^[A-Za-z]:/.test(p) ? "\\" : "/"
}

export function childPath(dir: string, name: string): string {
  if (dir === "") return name // корень: имя диска «C:\»
  return dir.endsWith("\\") || dir.endsWith("/") ? dir + name : dir + sepOf(dir) + name
}

export function parentPath(p: string): string {
  if (p === "" || /^[A-Za-z]:\\?$/.test(p) || p === "/") return ""
  const trimmed = p.replace(/[\\/]+$/, "")
  const i = Math.max(trimmed.lastIndexOf("\\"), trimmed.lastIndexOf("/"))
  if (i < 0) return ""
  const parent = trimmed.slice(0, i)
  if (/^[A-Za-z]:$/.test(parent)) return parent + "\\"
  return parent === "" ? "/" : parent
}

interface Props {
  sessionId: string
  onClose: () => void
  /** Файлы, брошенные на экран сеанса: загружаются на Рабочий стол сотрудника. */
  dropped?: File[]
  onDroppedTaken?: () => void
}

export default function RemoteFileManager({ sessionId, onClose, dropped, onDroppedTaken }: Props) {
  const { t } = useTranslation()
  const [path, setPath] = useState<string | null>(null)
  const [pathInput, setPathInput] = useState("")
  const [entries, setEntries] = useState<Entry[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState("")
  const [transfers, setTransfers] = useState<Transfer[]>([])
  const [dragOver, setDragOver] = useState(false)
  const fileInput = useRef<HTMLInputElement>(null)
  const keyRef = useRef(0)
  const base = `/screen-sessions/${sessionId}/files`

  const load = useCallback(
    async (p: string | null) => {
      setLoading(true)
      setError("")
      try {
        const r = await api.get<{ path: string; entries: Entry[]; error?: string }>(base, {
          params: p === null ? { home: "1" } : { path: p },
        })
        setPath(r.data.path)
        setPathInput(r.data.path)
        setEntries(r.data.entries ?? [])
        if (r.data.error) setError(r.data.error)
      } catch (e) {
        setError(errMessage(e))
      } finally {
        setLoading(false)
      }
    },
    [base],
  )

  useEffect(() => {
    void load(null)
  }, [load])

  const update = (key: number, patch: Partial<Transfer>) =>
    setTransfers((cur) => cur.map((x) => (x.key === key ? { ...x, ...patch } : x)))

  const upload = useCallback(
    async (files: File[], dir: string) => {
      for (const f of files) {
        const key = ++keyRef.current
        setTransfers((cur) => [{ key, name: f.name, up: true, pct: 0, state: "run" }, ...cur].slice(0, 30))
        try {
          await api.post(`${base}/upload`, f, {
            params: { path: dir, name: f.name },
            headers: { "Content-Type": "application/octet-stream" },
            timeout: 0,
            onUploadProgress: (ev) => {
              if (ev.total) update(key, { pct: Math.round((ev.loaded / ev.total) * 100) })
            },
          })
          update(key, { pct: 100, state: "ok" })
        } catch (e) {
          const body = (e as { response?: { data?: { error?: string } } }).response?.data
          update(key, { state: "err", err: body?.error ?? errMessage(e) })
        }
      }
      void load(dir)
    },
    [base, load],
  )

  // Файлы, брошенные на экран сеанса, — на Рабочий стол, как только он известен.
  useEffect(() => {
    if (!dropped?.length || path === null) return
    onDroppedTaken?.()
    void upload(dropped, path)
  }, [dropped, path, upload, onDroppedTaken])

  const download = (name: string) => {
    if (path === null) return
    // Ссылкой, а не XHR: браузер качает потоком, с собственным прогрессом, и большой файл
    // не держится в памяти вкладки. Авторизация — та же cookie сеанса панели.
    const a = document.createElement("a")
    a.href = `/api/v1${base}/download?path=${encodeURIComponent(childPath(path, name))}`
    a.download = name
    document.body.appendChild(a)
    a.click()
    a.remove()
    const key = ++keyRef.current
    setTransfers((cur) => [{ key, name, up: false, pct: 100, state: "ok" as const }, ...cur].slice(0, 30))
  }

  const mkdir = async () => {
    if (!path) return
    const name = window.prompt(t("screenSession.files.newFolderName"))
    if (!name) return
    try {
      await api.post(`${base}/mkdir`, { path, name })
      void load(path)
    } catch (e) {
      const body = (e as { response?: { data?: { error?: string } } }).response?.data
      setError(body?.error ?? errMessage(e))
    }
  }

  const atRoot = path === ""

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4" onClick={onClose}>
      <div
        className={
          "popover-surface flex h-[80vh] w-full max-w-4xl flex-col gap-3 p-4 " + (dragOver ? "ring-2 ring-primary" : "")
        }
        onClick={(e) => e.stopPropagation()}
        onDragOver={(e) => {
          if (path && e.dataTransfer.types.includes("Files")) {
            e.preventDefault()
            setDragOver(true)
          }
        }}
        onDragLeave={() => setDragOver(false)}
        onDrop={(e) => {
          e.preventDefault()
          setDragOver(false)
          if (path && e.dataTransfer.files.length) void upload(Array.from(e.dataTransfer.files), path)
        }}
      >
        <div className="flex items-center justify-between gap-2">
          <h2 className="text-base font-semibold">{t("screenSession.files.title")}</h2>
          <Button size="sm" variant="outline" onClick={onClose}>
            {t("screenSession.recordingClose")}
          </Button>
        </div>

        <div className="flex flex-wrap items-center gap-2">
          <Button size="sm" variant="outline" onClick={() => void load(null)} title={t("screenSession.files.desktop")}>
            <Home className="h-4 w-4" />
          </Button>
          <Button size="sm" variant="outline" onClick={() => void load("")} title={t("screenSession.files.drives")}>
            <HardDrive className="h-4 w-4" />
          </Button>
          <Button
            size="sm"
            variant="outline"
            disabled={atRoot || path === null}
            onClick={() => path !== null && void load(parentPath(path))}
            title={t("screenSession.files.up")}
          >
            <ArrowUp className="h-4 w-4" />
          </Button>
          <input
            className="h-8 min-w-0 flex-1 rounded-md border border-border bg-transparent px-2 font-mono text-xs"
            value={pathInput}
            onChange={(e) => setPathInput(e.target.value)}
            onKeyDown={(e) => e.key === "Enter" && void load(pathInput.trim())}
            aria-label={t("screenSession.files.path")}
          />
          <Button size="sm" variant="outline" onClick={() => path !== null && void load(path)} title={t("screenSession.files.refresh")}>
            <RefreshCw className="h-4 w-4" />
          </Button>
          <Button size="sm" variant="outline" disabled={!path} onClick={() => void mkdir()} title={t("screenSession.files.newFolder")}>
            <FolderPlus className="h-4 w-4" />
          </Button>
          <Button size="sm" disabled={!path} onClick={() => fileInput.current?.click()}>
            <Upload className="mr-1 h-4 w-4" />
            {t("screenSession.files.upload")}
          </Button>
          <input
            ref={fileInput}
            type="file"
            multiple
            className="hidden"
            onChange={(e) => {
              const files = Array.from(e.target.files ?? [])
              e.target.value = ""
              if (path && files.length) void upload(files, path)
            }}
          />
        </div>

        {error && <p className="text-xs text-amber-600 dark:text-amber-500">{error}</p>}

        <div className="min-h-0 flex-1 overflow-auto rounded-md border border-border">
          {loading && <p className="p-3 text-sm text-muted-foreground">{t("screenSession.files.loading")}</p>}
          {!loading && entries.length === 0 && (
            <p className="p-3 text-sm text-muted-foreground">{t("screenSession.files.empty")}</p>
          )}
          {!loading &&
            entries.map((en) => (
              <div
                key={en.name}
                className="flex cursor-default items-center gap-2 border-b border-border px-3 py-1.5 text-sm last:border-b-0 hover:bg-muted/50"
                onDoubleClick={() => (en.dir ? void load(childPath(path ?? "", en.name)) : download(en.name))}
              >
                {en.dir ? <Folder className="h-4 w-4 shrink-0 text-amber-500" /> : <File className="h-4 w-4 shrink-0 text-muted-foreground" />}
                <button
                  type="button"
                  className="min-w-0 flex-1 truncate text-left hover:underline"
                  onClick={() => (en.dir ? void load(childPath(path ?? "", en.name)) : download(en.name))}
                >
                  {en.name}
                </button>
                {!en.dir && <span className="w-20 shrink-0 text-right text-xs text-muted-foreground">{formatBytes(en.size)}</span>}
                <span className="hidden w-36 shrink-0 text-right text-xs text-muted-foreground sm:block">
                  {en.mtime ? formatDateTime(new Date(en.mtime * 1000).toISOString()) : ""}
                </span>
                {!en.dir && (
                  <Button size="sm" variant="ghost" onClick={() => download(en.name)} title={t("screenSession.files.download")}>
                    <Download className="h-4 w-4" />
                  </Button>
                )}
              </div>
            ))}
        </div>

        <p className="text-xs text-muted-foreground">{t("screenSession.files.hint")}</p>

        {transfers.length > 0 && (
          <div className="max-h-32 space-y-1 overflow-auto text-xs">
            {transfers.map((x) => (
              <div key={x.key} className="flex items-center gap-2">
                <span className="w-4">{x.up ? "↑" : "↓"}</span>
                <span className="min-w-0 flex-1 truncate">{x.name}</span>
                {x.state === "run" && <span>{x.pct}%</span>}
                {x.state === "ok" && <span className="text-emerald-600 dark:text-emerald-500">{x.up ? t("screenSession.files.uploaded") : t("screenSession.files.downloadStarted")}</span>}
                {x.state === "err" && <span className="text-destructive">{x.err}</span>}
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  )
}
