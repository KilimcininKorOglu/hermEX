import { describe, expect, it } from "vitest"
import { openVault, sealVault, VAULT_ITERATIONS, WrongVaultPassword } from "./smimeVault"

const content = { keyPkcs8: "MIIBVQIBADANBgkqhkiG9w0BAQEFAASC", certPem: "-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n" }

describe("smimeVault", () => {
  it("opens what it sealed under the same password", async () => {
    const env = await sealVault(content, "correct horse")
    await expect(openVault(env, "correct horse")).resolves.toEqual(content)
  })

  it("refuses the wrong password", async () => {
    const env = await sealVault(content, "correct horse")
    await expect(openVault(env, "wrong horse")).rejects.toBeInstanceOf(WrongVaultPassword)
  })

  // The server validates these fields before it stores an envelope; a sealed
  // envelope that did not meet them would be refused on upload.
  it("produces the envelope the server accepts", async () => {
    const env = await sealVault(content, "pw")
    expect(env.v).toBe(1)
    expect(env.kdf).toBe("PBKDF2-SHA256")
    expect(env.iter).toBeGreaterThanOrEqual(VAULT_ITERATIONS)
    expect(atob(env.salt)).toHaveLength(16)
    expect(atob(env.iv)).toHaveLength(12)
    expect(env.ct.length).toBeGreaterThan(0)
    expect(env.ct).not.toContain(content.keyPkcs8)
  })

  it("never reuses a salt or an IV", async () => {
    const a = await sealVault(content, "pw")
    const b = await sealVault(content, "pw")
    expect(a.salt).not.toBe(b.salt)
    expect(a.iv).not.toBe(b.iv)
  })
})
