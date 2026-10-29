import { describe, it, expect } from "vitest"
import { buildBundle, bundleTrailer } from "./setupBundle"

const params = {
  enroll_url: "https://h/api/v1/enroll",
  enroll_token: "tok",
  ca_url: "https://h/ca.crt",
  ca_sha256: "ab12",
  server_addr: "h:50051",
}

async function bytes(b: Blob): Promise<Uint8Array> {
  return new Uint8Array(await b.arrayBuffer())
}

describe("buildBundle", () => {
  it("кладёт stub, msi, json и трейлер в порядке, который читает Go-сторона", async () => {
    const stub = new Uint8Array([1, 2, 3]).buffer
    const msi = new Uint8Array([9, 9, 9, 9]).buffer
    const out = await bytes(buildBundle(stub, msi, params))
    const json = new TextEncoder().encode(JSON.stringify(params))
    const tail = out.slice(out.length - 16)
    const dv = new DataView(tail.buffer, tail.byteOffset)
    expect(dv.getUint32(0, true)).toBe(4) // длина msi
    expect(dv.getUint32(4, true)).toBe(json.length) // длина json
    expect(new TextDecoder().decode(tail.slice(8))).toBe("ROPSET01")
    expect(Array.from(out.slice(0, 3))).toEqual([1, 2, 3])
    expect(Array.from(out.slice(3, 7))).toEqual([9, 9, 9, 9])
    expect(out.length).toBe(3 + 4 + json.length + 16)
  })

  it("🔴 отвергает кавычку в значении: она вышла бы из командной строки msiexec", () => {
    expect(() => buildBundle(new ArrayBuffer(1), new ArrayBuffer(1), { ...params, server_addr: 'x" & calc' })).toThrow()
  })

  it("отвергает пустой токен", () => {
    expect(() => buildBundle(new ArrayBuffer(1), new ArrayBuffer(1), { ...params, enroll_token: " " })).toThrow()
  })

  it("трейлер — 16 байт", () => {
    expect(bundleTrailer(1, 2).length).toBe(16)
  })
})
