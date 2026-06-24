/**
 * Client-side S/MIME identity store.
 *
 * The private key never reaches the server. A PKCS#12 (.p12) identity — already
 * encrypted under its own password — is stored verbatim in IndexedDB, so it is
 * encrypted at rest (an XSS at rest cannot use it without the password). Each
 * session the user unlocks it: forge opens the .p12 in memory and the key is held
 * only there. All sign/encrypt/verify/decrypt happen in the browser with forge.
 */
import forge from "node-forge"

const DB_NAME = "hermex-smime"
const STORE = "identity"
const REC_KEY = "self"

/** Stored identity: the raw (password-protected) PKCS#12 plus its public cert. */
interface StoredIdentity {
  v: number
  p12: ArrayBuffer
  certPem: string
}

/** CertInfo mirrors the server's SMIMECertInfo for the settings view. */
export interface CertInfo {
  subject: string
  issuer: string
  notBefore: string
  notAfter: string
  serialNumber: string
  fingerprint: string
}

// In-memory unlocked state — cleared on refresh/lock; never persisted decrypted.
let unlockedKey: forge.pki.rsa.PrivateKey | null = null
let unlockedCertPem: string | null = null

function openDB(): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    const req = indexedDB.open(DB_NAME, 1)
    req.onupgradeneeded = () => req.result.createObjectStore(STORE)
    req.onsuccess = () => resolve(req.result)
    req.onerror = () => reject(req.error)
  })
}

async function idbGet(key: string): Promise<StoredIdentity | undefined> {
  const db = await openDB()
  return new Promise((resolve, reject) => {
    const r = db.transaction(STORE, "readonly").objectStore(STORE).get(key)
    r.onsuccess = () => resolve(r.result as StoredIdentity | undefined)
    r.onerror = () => reject(r.error)
  })
}

async function idbPut(key: string, val: StoredIdentity): Promise<void> {
  const db = await openDB()
  return new Promise((resolve, reject) => {
    const tx = db.transaction(STORE, "readwrite")
    tx.objectStore(STORE).put(val, key)
    tx.oncomplete = () => resolve()
    tx.onerror = () => reject(tx.error)
  })
}

async function idbDel(key: string): Promise<void> {
  const db = await openDB()
  return new Promise((resolve, reject) => {
    const tx = db.transaction(STORE, "readwrite")
    tx.objectStore(STORE).delete(key)
    tx.oncomplete = () => resolve()
    tx.onerror = () => reject(tx.error)
  })
}

/** parseP12 opens a PKCS#12 with its password, returning the key and cert. */
function parseP12(p12Der: string, password: string): { key: forge.pki.rsa.PrivateKey; cert: forge.pki.Certificate } {
  const asn1 = forge.asn1.fromDer(p12Der)
  const p12 = forge.pkcs12.pkcs12FromAsn1(asn1, password)
  let key: forge.pki.rsa.PrivateKey | null = null
  let cert: forge.pki.Certificate | null = null
  for (const safeContents of p12.safeContents) {
    for (const bag of safeContents.safeBags) {
      if (bag.key) key = bag.key as forge.pki.rsa.PrivateKey
      if (bag.cert) cert = bag.cert
    }
  }
  if (!key || !cert) throw new Error("the .p12 file does not contain both a certificate and a private key")
  return { key, cert }
}

/** certToInfo projects a forge certificate into the settings CertInfo shape. */
function certToInfo(cert: forge.pki.Certificate): CertInfo {
  const der = forge.asn1.toDer(forge.pki.certificateToAsn1(cert)).getBytes()
  const md = forge.md.sha256.create()
  md.update(der)
  const dn = (attrs: forge.pki.CertificateField[]) =>
    attrs.map((a) => `${a.shortName || a.name}=${a.value}`).join(", ")
  return {
    subject: dn(cert.subject.attributes),
    issuer: dn(cert.issuer.attributes),
    notBefore: cert.validity.notBefore.toISOString(),
    notAfter: cert.validity.notAfter.toISOString(),
    serialNumber: cert.serialNumber,
    fingerprint: md.digest().toHex(),
  }
}

/** binaryToDer converts an ArrayBuffer to the binary string forge expects. */
function binaryToDer(buf: ArrayBuffer): string {
  return forge.util.binary.raw.encode(new Uint8Array(buf))
}

/**
 * importP12 validates a .p12 with its password, stores it (encrypted at rest by
 * its own password) in IndexedDB, and leaves the identity unlocked for this
 * session. Returns the certificate info and its public certificate as PEM (to
 * publish to the directory).
 */
export async function importP12(
  p12Bytes: ArrayBuffer,
  password: string,
): Promise<{ info: CertInfo; certPem: string }> {
  const der = binaryToDer(p12Bytes)
  const { key, cert } = parseP12(der, password)
  const certPem = forge.pki.certificateToPem(cert)
  await idbPut(REC_KEY, { v: 1, p12: p12Bytes, certPem })
  unlockedKey = key
  unlockedCertPem = certPem
  return { info: certToInfo(cert), certPem }
}

/** hasIdentity reports whether a stored identity exists in this browser. */
export async function hasIdentity(): Promise<boolean> {
  return (await idbGet(REC_KEY)) !== undefined
}

/** storedCertInfo returns the stored public certificate's info, or null. */
export async function storedCertInfo(): Promise<CertInfo | null> {
  const rec = await idbGet(REC_KEY)
  if (!rec) return null
  return certToInfo(forge.pki.certificateFromPem(rec.certPem))
}

/** removeIdentity clears the stored identity and locks the session. */
export async function removeIdentity(): Promise<void> {
  await idbDel(REC_KEY)
  lock()
}

/** unlock opens the stored .p12 with its password, holding the key in memory. */
export async function unlock(password: string): Promise<void> {
  const rec = await idbGet(REC_KEY)
  if (!rec) throw new Error("no S/MIME identity is set up in this browser")
  const { key, cert } = parseP12(binaryToDer(rec.p12), password)
  unlockedKey = key
  unlockedCertPem = forge.pki.certificateToPem(cert)
}

/** isUnlocked reports whether the private key is available in memory. */
export function isUnlocked(): boolean {
  return unlockedKey !== null
}

/** lock clears the in-memory key. */
export function lock(): void {
  unlockedKey = null
  unlockedCertPem = null
}

/** requireUnlocked returns the in-memory key/cert or throws. */
export function requireUnlocked(): { key: forge.pki.rsa.PrivateKey; certPem: string } {
  if (!unlockedKey || !unlockedCertPem) throw new Error("unlock your S/MIME certificate first")
  return { key: unlockedKey, certPem: unlockedCertPem }
}
