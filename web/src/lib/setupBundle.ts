// Сборка установщика «одним файлом» прямо в браузере (Go-сторона — internal/setupbundle).
//
// Раскладка: [stub.exe][msi][json][u32 LE длина msi][u32 LE длина json]["ROPSET01"].
// Токен устройства и пин CA панель и так показывает оператору; сервер в сборке не
// участвует и ничего не хранит: файл существует только в браузере и в «Загрузках».

export interface SetupParams {
  enroll_url: string
  enroll_token: string
  ca_url: string
  ca_sha256: string
  server_addr: string
}

const MAGIC = "ROPSET01"

export function bundleTrailer(msiLen: number, jsonLen: number): Uint8Array {
  const tr = new Uint8Array(8 + MAGIC.length)
  const dv = new DataView(tr.buffer)
  dv.setUint32(0, msiLen, true)
  dv.setUint32(4, jsonLen, true)
  tr.set(new TextEncoder().encode(MAGIC), 8)
  return tr
}

// buildBundle склеивает файл. Кавычки в значениях ломали бы командную строку msiexec, и
// Go-сторона такие параметры всё равно отвергнет, поэтому отказываем сразу, с понятной причиной.
export function buildBundle(stub: ArrayBuffer, msi: ArrayBuffer, params: SetupParams): Blob {
  for (const [k, v] of Object.entries(params)) {
    if (!v.trim()) throw new Error(`${k} is empty`)
    if (/["\r\n\0]/.test(v)) throw new Error(`${k} contains a forbidden character`)
  }
  const json = new TextEncoder().encode(JSON.stringify(params))
  return new Blob([stub, msi, json, bundleTrailer(msi.byteLength, json.byteLength)], {
    type: "application/vnd.microsoft.portable-executable",
  })
}

export class SetupUnavailableError extends Error {}

async function getBinary(url: string): Promise<ArrayBuffer> {
  const r = await fetch(url, { cache: "no-store" })
  if (!r.ok) throw new SetupUnavailableError(`${url}: HTTP ${r.status}`)
  return r.arrayBuffer()
}

// fetchSetup downloads the stub and the MSI from the panel and builds the installer.
export async function fetchSetup(base: string, params: SetupParams): Promise<Blob> {
  const [stub, msi] = await Promise.all([
    getBinary(`${base}/downloads/RoutineOps-setup.exe`),
    getBinary(`${base}/downloads/RoutineOps-agent.msi`),
  ])
  return buildBundle(stub, msi, params)
}

export function saveBlob(blob: Blob, filename: string) {
  const url = URL.createObjectURL(blob)
  const a = document.createElement("a")
  a.href = url
  a.download = filename
  document.body.appendChild(a)
  a.click()
  document.body.removeChild(a)
  setTimeout(() => URL.revokeObjectURL(url), 10_000)
}
