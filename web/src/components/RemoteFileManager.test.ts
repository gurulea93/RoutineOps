import { describe, expect, it } from "vitest"
import { childPath, parentPath } from "./RemoteFileManager"

describe("remote paths", () => {
  it("walks Windows paths", () => {
    expect(childPath("", "C:\\")).toBe("C:\\")
    expect(childPath("C:\\", "Users")).toBe("C:\\Users")
    expect(childPath("C:\\Users", "Ion")).toBe("C:\\Users\\Ion")
    expect(parentPath("C:\\Users\\Ion")).toBe("C:\\Users")
    expect(parentPath("C:\\Users")).toBe("C:\\")
    expect(parentPath("C:\\")).toBe("")
  })
  it("walks POSIX paths", () => {
    expect(childPath("/", "home")).toBe("/home")
    expect(parentPath("/home/ion")).toBe("/home")
    expect(parentPath("/home")).toBe("/")
    expect(parentPath("/")).toBe("")
  })
})
