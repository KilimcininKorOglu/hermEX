import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest"
import forge from "node-forge"

const server = vi.hoisted(() => ({
  getSMIMECertificate: vi.fn(),
  uploadSMIMECertificate: vi.fn(),
  getSMIMEVault: vi.fn(),
  deleteSMIMECertificate: vi.fn(),
}))
const legacy = vi.hoisted(() => ({ readLegacyIdentity: vi.fn(), dropLegacyIdentity: vi.fn() }))
vi.mock("./api", () => ({ default: server }))
vi.mock("./smimeLegacy", () => legacy)

import { forgetIdentity, hasIdentity, removeIdentity, unlock } from "./smimeIdentity"
import { isUnlocked } from "./smime"
import { openVault } from "./smimeVault"

// Two throwaway identities: the account's own, and one another account left in
// this browser.
let own: { p12: ArrayBuffer; certPem: string; fingerprint: string }
let foreign: { p12: ArrayBuffer; certPem: string }

function identity(email: string, password: string) {
  const keys = forge.pki.rsa.generateKeyPair(1024)
  const cert = forge.pki.createCertificate()
  cert.publicKey = keys.publicKey
  cert.serialNumber = "01"
  cert.validity.notBefore = new Date(Date.now() - 60_000)
  cert.validity.notAfter = new Date(Date.now() + 86_400_000)
  cert.setSubject([{ name: "emailAddress", value: email }])
  cert.setIssuer([{ name: "emailAddress", value: email }])
  cert.sign(keys.privateKey, forge.md.sha256.create())
  const der = forge.asn1.toDer(forge.pkcs12.toPkcs12Asn1(keys.privateKey, cert, password, { algorithm: "3des" })).getBytes()
  const md = forge.md.sha256.create()
  md.update(forge.asn1.toDer(forge.pki.certificateToAsn1(cert)).getBytes())
  return {
    p12: Uint8Array.from(der, (c) => c.charCodeAt(0)).buffer,
    certPem: forge.pki.certificateToPem(cert),
    fingerprint: md.digest().toHex(),
  }
}

beforeAll(() => {
  own = identity("ada@hermex.test", "pw")
  foreign = identity("bob@hermex.test", "pw")
})

beforeEach(() => {
  vi.clearAllMocks()
  forgetIdentity()
  server.getSMIMECertificate.mockResolvedValue({ mode: "browser", hasVault: false, fingerprint: own.fingerprint })
  server.uploadSMIMECertificate.mockResolvedValue({})
  legacy.dropLegacyIdentity.mockResolvedValue(undefined)
})

describe("smimeIdentity", () => {
  // The next unlock of an older client's copy moves it into a vault the same
  // password opens, and deletes the copy only after the server has the vault.
  it("moves this account's IndexedDB copy into a vault on unlock", async () => {
    legacy.readLegacyIdentity.mockResolvedValue({ p12: own.p12, certPem: own.certPem })
    expect(await hasIdentity()).toBe(true)
    await unlock("pw")

    expect(isUnlocked()).toBe(true)
    const [certPem, vault] = server.uploadSMIMECertificate.mock.calls[0]
    expect(certPem).toBe(own.certPem)
    await expect(openVault(vault, "pw")).resolves.toMatchObject({ certPem: own.certPem })
    expect(legacy.dropLegacyIdentity).toHaveBeenCalledOnce()
    expect(server.uploadSMIMECertificate.mock.invocationCallOrder[0]).toBeLessThan(
      legacy.dropLegacyIdentity.mock.invocationCallOrder[0],
    )
  })

  it("keeps the IndexedDB copy when the vault upload fails", async () => {
    legacy.readLegacyIdentity.mockResolvedValue({ p12: own.p12, certPem: own.certPem })
    server.uploadSMIMECertificate.mockRejectedValue(new Error("offline"))
    await expect(unlock("pw")).rejects.toThrow("offline")
    expect(legacy.dropLegacyIdentity).not.toHaveBeenCalled()
  })

  // IndexedDB belongs to the browser, not the account: another account's copy is
  // neither offered to this one nor deleted when this one removes its identity.
  it("ignores and keeps another account's IndexedDB copy", async () => {
    legacy.readLegacyIdentity.mockResolvedValue({ p12: foreign.p12, certPem: foreign.certPem })
    expect(await hasIdentity()).toBe(false)
    await expect(unlock("pw")).rejects.toThrow()
    server.deleteSMIMECertificate.mockResolvedValue({ status: "removed" })
    await removeIdentity()
    expect(legacy.dropLegacyIdentity).not.toHaveBeenCalled()
  })

  it("opens a stored vault with the password", async () => {
    legacy.readLegacyIdentity.mockResolvedValue({ p12: own.p12, certPem: own.certPem })
    await unlock("pw")
    const vault = server.uploadSMIMECertificate.mock.calls[0][1]
    forgetIdentity()
    server.getSMIMECertificate.mockResolvedValue({ mode: "browser", hasVault: true, fingerprint: own.fingerprint })
    server.getSMIMEVault.mockResolvedValue(vault)

    await expect(unlock("wrong")).rejects.toThrow()
    expect(isUnlocked()).toBe(false)
    await unlock("pw")
    expect(isUnlocked()).toBe(true)
  })

  it("has no browser key in server mode", async () => {
    server.getSMIMECertificate.mockResolvedValue({ mode: "server", fingerprint: own.fingerprint })
    expect(await hasIdentity()).toBe(false)
  })
})
