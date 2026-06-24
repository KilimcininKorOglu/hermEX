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

/** decodeCTE decodes an entity body per its Content-Transfer-Encoding. */
function decodeCTE(body: string, cte: string): string {
  const enc = cte.toLowerCase().trim()
  if (enc === "base64") return forge.util.decode64(body.replace(/\s+/g, ""))
  if (enc === "quoted-printable") {
    return body
      .replace(/=\r?\n/g, "")
      .replace(/=([0-9A-Fa-f]{2})/g, (_, h) => String.fromCharCode(parseInt(h, 16)))
  }
  return body
}

/** parseEntity splits a MIME entity into its header map and body. */
function parseEntity(mime: string): { headers: Record<string, string>; body: string } {
  const sep = mime.indexOf("\r\n\r\n") >= 0 ? "\r\n\r\n" : "\n\n"
  const cut = mime.indexOf(sep)
  const head = cut >= 0 ? mime.slice(0, cut) : mime
  const body = cut >= 0 ? mime.slice(cut + sep.length) : ""
  const headers: Record<string, string> = {}
  let last = ""
  for (const line of head.split(/\r\n|\n/)) {
    if (/^[ \t]/.test(line) && last) {
      headers[last] += " " + line.trim()
      continue
    }
    const i = line.indexOf(":")
    if (i < 0) continue
    last = line.slice(0, i).toLowerCase().trim()
    headers[last] = line.slice(i + 1).trim()
  }
  return { headers, body }
}

/**
 * extractMimeBody pulls a displayable body out of a decrypted MIME entity,
 * preferring text/html over text/plain. Handles single parts and nested
 * multipart/alternative — enough for typical mail.
 */
export function extractMimeBody(mime: string): { html: boolean; body: string } {
  const { headers, body } = parseEntity(mime)
  const ctRaw = headers["content-type"] || "text/plain"
  const ct = ctRaw.toLowerCase()
  if (ct.startsWith("multipart/")) {
    // boundaries are case-sensitive (RFC 2046): match against the original header.
    const m = ctRaw.match(/boundary="?([^";]+)"?/i)
    if (m) {
      const parts = body.split("--" + m[1]).slice(1, -1)
      let fallback: { html: boolean; body: string } | null = null
      for (const part of parts) {
        const r = extractMimeBody(part.replace(/^\r?\n/, ""))
        if (r.html) return r
        if (!fallback) fallback = r
      }
      if (fallback) return fallback
    }
  }
  return { html: ct.startsWith("text/html"), body: decodeCTE(body, headers["content-transfer-encoding"] || "") }
}

/** requireUnlocked returns the in-memory key/cert or throws. */
export function requireUnlocked(): { key: forge.pki.rsa.PrivateKey; cert: forge.pki.Certificate } {
  if (!unlockedKey || !unlockedCertPem) throw new Error("unlock your S/MIME certificate first")
  return { key: unlockedKey, cert: forge.pki.certificateFromPem(unlockedCertPem) }
}

/**
 * encryptMime wraps a built message as S/MIME application/pkcs7-mime enveloped
 * data, encrypted to every recipient certificate (PEM). The identity headers are
 * re-attached around the enveloped entity. The body is encrypted; the envelope
 * headers (From/To/Subject) stay in the clear, as S/MIME requires.
 */
export function encryptMime(raw: string, recipientCertPems: string[]): string {
  const { identity, inner } = splitIdentity(crlf(raw))
  const p7 = forge.pkcs7.createEnvelopedData()
  for (const pem of recipientCertPems) p7.addRecipient(forge.pki.certificateFromPem(pem))
  p7.content = forge.util.createBuffer(crlf(inner), "raw")
  p7.encrypt()
  const der = forge.asn1.toDer((p7 as unknown as { toAsn1(): forge.asn1.Asn1 }).toAsn1()).getBytes()
  const b64 = chunk76(forge.util.encode64(der))
  return (
    identity +
    "MIME-Version: 1.0\r\n" +
    `Content-Type: application/pkcs7-mime; smime-type=enveloped-data; name="smime.p7m"\r\n` +
    "Content-Transfer-Encoding: base64\r\n" +
    `Content-Disposition: attachment; filename="smime.p7m"\r\n\r\n` +
    b64 +
    "\r\n"
  )
}

/**
 * decryptMime decrypts an S/MIME enveloped message with the unlocked key,
 * returning the inner MIME entity. Forge does the RSAES-PKCS1-v1_5 RSA operation
 * in pure JS (WebCrypto cannot), which is why the key must be available in memory.
 */
export function decryptMime(raw: string): string {
  const { key, cert } = requireUnlocked()
  const sep = raw.indexOf("\r\n\r\n") >= 0 ? "\r\n\r\n" : "\n\n"
  const cut = raw.indexOf(sep)
  const body = (cut >= 0 ? raw.slice(cut + sep.length) : raw).replace(/[^A-Za-z0-9+/=]/g, "")
  const der = forge.util.decode64(body)
  const p7 = forge.pkcs7.messageFromAsn1(forge.asn1.fromDer(der)) as unknown as {
    findRecipient(c: forge.pki.Certificate): unknown
    decrypt(r: unknown, k: forge.pki.rsa.PrivateKey): void
    content: forge.util.ByteBuffer
  }
  const recipient = p7.findRecipient(cert)
  if (!recipient) throw new Error("this message is not encrypted to your certificate")
  p7.decrypt(recipient, key)
  return p7.content.getBytes()
}

/** signerEmail pulls the best email identifier out of a signer certificate. */
function signerEmail(cert: forge.pki.Certificate): string {
  const ext = cert.getExtension("subjectAltName") as
    | { altNames?: { type: number; value: string }[] }
    | undefined
  const san = ext?.altNames?.find((n) => n.type === 1) // rfc822Name
  if (san?.value) return san.value
  const e =
    cert.subject.getField("E") ||
    cert.subject.getField({ type: "1.2.840.113549.1.9.1" })
  if (e?.value) return e.value
  const cn = cert.subject.getField("CN")
  return cn?.value ?? ""
}

/**
 * verifyMime checks the signature on a decrypted multipart/signed entity entirely
 * in the browser. For browser-mode encrypted-then-signed mail the server never sees
 * the decrypted content — posting it for verification would leak the plaintext — so
 * the check must run client-side. Our sign path omits signedAttrs, so the signature
 * is a plain RSA(SHA-256(content)): extract the signer certificate and signature
 * from the detached SignedData, hash the exact signed bytes (the content part
 * between the first two boundaries, as signMime emitted them), and RSA-verify.
 * Returns null when the entity is not a multipart/signed structure.
 */
export function verifyMime(mime: string): { verified: boolean; signedBy: string } | null {
  const { headers, body } = parseEntity(mime)
  const ctRaw = headers["content-type"] || ""
  if (!ctRaw.toLowerCase().startsWith("multipart/signed")) return null
  // MIME boundaries are case-sensitive (RFC 2046) — read it from the original
  // header, never a lower-cased copy, or an external client's mixed-case boundary
  // silently fails extraction and the signature reads as invalid.
  const m = ctRaw.match(/boundary="?([^";]+)"?/i)
  if (!m) return null
  const dashB = "--" + m[1]
  const startMarker = dashB + "\r\n"
  const i1 = body.indexOf(startMarker)
  if (i1 < 0) return null
  const contentStart = i1 + startMarker.length
  const i2 = body.indexOf("\r\n" + dashB, contentStart)
  if (i2 < 0) return null
  // the exact bytes that were signed: the content part, no trailing CRLF (that
  // CRLF belongs to the boundary delimiter, not the content).
  const signedContent = body.slice(contentStart, i2)
  const sigStart = i2 + ("\r\n" + dashB + "\r\n").length
  const i3 = body.indexOf("\r\n" + dashB, sigStart)
  const sigPart = body.slice(sigStart, i3 < 0 ? undefined : i3)
  const { headers: sh, body: sb } = parseEntity(sigPart)
  const sigDer = decodeCTE(sb, sh["content-transfer-encoding"] || "base64")
  try {
    const p7 = forge.pkcs7.messageFromAsn1(forge.asn1.fromDer(sigDer)) as unknown as {
      certificates: forge.pki.Certificate[]
      rawCapture: { signature: string }
    }
    const cert = p7.certificates[0]
    const sig = p7.rawCapture.signature
    if (!cert || !sig) return { verified: false, signedBy: "" }
    const md = forge.md.sha256.create()
    md.update(signedContent)
    const verified = (cert.publicKey as forge.pki.rsa.PublicKey).verify(md.digest().bytes(), sig)
    return { verified, signedBy: verified ? signerEmail(cert) : "" }
  } catch {
    return { verified: false, signedBy: "" }
  }
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
