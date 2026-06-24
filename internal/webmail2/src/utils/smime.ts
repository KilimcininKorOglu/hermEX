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
export function requireUnlocked(): { key: forge.pki.rsa.PrivateKey; cert: forge.pki.Certificate } {
  if (!unlockedKey || !unlockedCertPem) throw new Error("unlock your S/MIME certificate first")
  return { key: unlockedKey, cert: forge.pki.certificateFromPem(unlockedCertPem) }
}

/** crlf canonicalizes line endings to CRLF, as S/MIME signing requires. */
function crlf(s: string): string {
  return s.replace(/\r\n/g, "\n").replace(/\n/g, "\r\n")
}

/**
 * splitIdentity divides a built RFC 5322 message into the identity headers that
 * stay on the outer message (From/To/Subject/Date/…) and the inner MIME entity
 * (Content-* headers + body) that S/MIME signs or encrypts. Mirrors the server's
 * splitForSmime so signatures interoperate. MIME-Version is dropped from both.
 */
function splitIdentity(raw: string): { identity: string; inner: string } {
  const sep = raw.indexOf("\r\n\r\n") >= 0 ? "\r\n\r\n" : "\n\n"
  const cut = raw.indexOf(sep)
  if (cut < 0) return { identity: raw, inner: "" }
  const headerBlock = raw.slice(0, cut)
  const body = raw.slice(cut + sep.length)
  const lines = headerBlock.split(/\r\n|\n/)
  const idLines: string[] = []
  const contentLines: string[] = []
  let cur: "id" | "content" | null = null
  for (const line of lines) {
    if (/^[ \t]/.test(line) && cur) {
      ;(cur === "content" ? contentLines : idLines).push(line)
      continue
    }
    const name = line.slice(0, line.indexOf(":")).toLowerCase().trim()
    if (name === "mime-version") {
      cur = null
      continue
    }
    if (name.startsWith("content-")) {
      cur = "content"
      contentLines.push(line)
    } else {
      cur = "id"
      idLines.push(line)
    }
  }
  const identity = idLines.length ? idLines.join("\r\n") + "\r\n" : ""
  const inner = (contentLines.length ? contentLines.join("\r\n") + "\r\n" : "") + "\r\n" + body
  return { identity, inner }
}

/** randomBoundary returns a unique MIME multipart boundary. */
function randomBoundary(): string {
  const a = new Uint8Array(16)
  crypto.getRandomValues(a)
  return "hermex-smime-" + Array.from(a, (b) => b.toString(16).padStart(2, "0")).join("")
}

/** chunk76 wraps a base64 string at 76 columns (RFC 2045). */
function chunk76(b64: string): string {
  return (b64.match(/.{1,76}/g) || []).join("\r\n")
}

/**
 * signMime signs a built message with the unlocked identity, producing an S/MIME
 * multipart/signed message. The identity headers are re-attached around the
 * signed entity so the delivered message keeps its From/To/Subject. Throws if the
 * identity is locked.
 */
export function signMime(raw: string): string {
  const { key, cert } = requireUnlocked()
  const { identity, inner } = splitIdentity(crlf(raw))
  const innerCanon = crlf(inner)

  const p7 = forge.pkcs7.createSignedData()
  p7.content = forge.util.createBuffer(innerCanon, "raw")
  p7.addCertificate(cert)
  // Sign the content directly, WITHOUT authenticated attributes: forge's
  // signedAttrs DER does not re-encode identically under the server's verifier
  // (smallstep/pkcs7), which breaks verification, whereas a content-direct
  // signature verifies under both openssl and the server. signedAttrs are
  // optional in RFC 5652/5751, so this stays standards-compliant.
  p7.addSigner({ key, certificate: cert, digestAlgorithm: forge.pki.oids.sha256 })
  p7.sign({ detached: true })
  const der = forge.asn1.toDer(p7.toAsn1()).getBytes()
  const sigB64 = chunk76(forge.util.encode64(der))

  const b = randomBoundary()
  return (
    identity +
    "MIME-Version: 1.0\r\n" +
    `Content-Type: multipart/signed; protocol="application/pkcs7-signature"; micalg="sha-256"; boundary="${b}"\r\n\r\n` +
    "This is an S/MIME signed message\r\n\r\n" +
    `--${b}\r\n` +
    innerCanon +
    `\r\n--${b}\r\n` +
    `Content-Type: application/pkcs7-signature; name="smime.p7s"\r\n` +
    "Content-Transfer-Encoding: base64\r\n" +
    `Content-Disposition: attachment; filename="smime.p7s"\r\n\r\n` +
    sigB64 +
    `\r\n--${b}--\r\n`
  )
}
