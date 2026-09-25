/**
 * The user's browser-mode S/MIME identity.
 *
 * The private key is sealed in the browser under the user's password and only the
 * sealed copy is stored on the server, so the identity unlocks in every browser the
 * user signs in to. An identity an older client kept in this browser's IndexedDB is
 * moved into a vault the next time the user unlocks it, and the IndexedDB copy is
 * then deleted.
 */
import api from "./api"
import { certInfoFromPem, lock, readP12, restoreKey, type CertInfo } from "./smime"
import { dropLegacyIdentity, readLegacyIdentity } from "./smimeLegacy"
import { openVault, sealVault, type VaultContent } from "./smimeVault"

/**
 * KeyPlace is where this browser can reach the key: the server's vault, an older
 * client's copy in this browser, or nowhere (server mode, no identity, or an old
 * copy in another browser).
 */
export type KeyPlace = "vault" | "legacy" | "none"

// The answer is kept for the page's life and dropped whenever it can change here.
let place: Promise<KeyPlace> | null = null

async function findKey(): Promise<KeyPlace> {
  const res = await api.getSMIMECertificate()
  if ("hasKeys" in res || res.mode === "server") return "none"
  if (res.hasVault) return "vault"
  const legacy = await readLegacyIdentity()
  // The IndexedDB copy is per browser, not per account: it counts only when it
  // holds the certificate this account publishes.
  if (legacy && certInfoFromPem(legacy.certPem).fingerprint === res.fingerprint) return "legacy"
  return "none"
}

/** keyPlace reports where the browser-mode key is. */
export function keyPlace(): Promise<KeyPlace> {
  if (!place) {
    place = findKey().catch((err: unknown) => {
      place = null
      throw err
    })
  }
  return place
}

/** hasIdentity reports whether this browser can unlock a browser-mode key. */
export async function hasIdentity(): Promise<boolean> {
  return (await keyPlace()) !== "none"
}

// ownsLegacyCopy reports whether this browser's IndexedDB copy belongs to this
// account. Another account's copy is left alone: it is that account's to move.
async function ownsLegacyCopy(): Promise<boolean> {
  return (await keyPlace()) === "legacy"
}

// storeVault seals the identity, stores it with the published certificate, and
// holds the key for this page. The account's IndexedDB copy is deleted only after
// the server has the vault, so a failed upload loses nothing.
async function storeVault(content: VaultContent, password: string, dropLegacy: boolean): Promise<void> {
  await api.uploadSMIMECertificate(content.certPem, await sealVault(content, password))
  place = null
  restoreKey(content)
  if (dropLegacy) await dropLegacyIdentity()
}

/** importIdentity reads a .p12 and stores its key sealed under the same password. */
export async function importIdentity(p12Bytes: ArrayBuffer, password: string): Promise<CertInfo> {
  const { content, info } = readP12(p12Bytes, password)
  await storeVault(content, password, await ownsLegacyCopy())
  return info
}

/** unlock opens the key with the user's password and holds it for this page. */
export async function unlock(password: string): Promise<void> {
  const where = await keyPlace()
  if (where === "vault") {
    restoreKey(await openVault(await api.getSMIMEVault(), password))
    return
  }
  const legacy = where === "legacy" ? await readLegacyIdentity() : null
  if (!legacy) throw new Error("no S/MIME key is stored for this account")
  await storeVault(readP12(legacy.p12, password).content, password, true)
}

/**
 * removeIdentity deletes the server record first, so a failed delete leaves the
 * certificate and the key where they were.
 */
export async function removeIdentity(): Promise<void> {
  const dropLegacy = await ownsLegacyCopy()
  await api.deleteSMIMECertificate()
  place = null
  lock()
  if (dropLegacy) await dropLegacyIdentity()
}

/** forgetIdentity drops the key and what is known about it, for a sign-out. */
export function forgetIdentity(): void {
  place = null
  lock()
}
