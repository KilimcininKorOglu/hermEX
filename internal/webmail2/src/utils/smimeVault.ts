/**
 * Password-sealed S/MIME identity ("vault").
 *
 * The browser seals the private key and its certificate under a key derived from
 * the user's password (PBKDF2-SHA256, AES-256-GCM, both WebCrypto), and only the
 * sealed envelope goes to the server. The server stores it with the mailbox and
 * hands it back to its owner, so the identity is usable in every browser while the
 * server never holds anything it can open.
 */

/** VaultEnvelope is the sealed identity exactly as the server stores it. */
export interface VaultEnvelope {
  v: 1
  kdf: "PBKDF2-SHA256"
  iter: number
  salt: string
  iv: string
  ct: string
}

/** VaultContent is what the envelope seals: the key as PKCS#8 DER and the certificate. */
export interface VaultContent {
  keyPkcs8: string
  certPem: string
}

/** VAULT_ITERATIONS is the PBKDF2 work factor; the server refuses a lower one. */
export const VAULT_ITERATIONS = 600_000

// The format tag is authenticated with the ciphertext, so an envelope cannot be
// replayed as some other sealed format.
const AAD = new TextEncoder().encode("hermex-smime-vault-v1")

/** WrongVaultPassword is thrown when the envelope does not open under the password. */
export class WrongVaultPassword extends Error {
  constructor() {
    super("the password does not open the stored S/MIME key")
    this.name = "WrongVaultPassword"
  }
}

function toBase64(bytes: Uint8Array): string {
  let s = ""
  for (const b of bytes) s += String.fromCharCode(b)
  return btoa(s)
}

function fromBase64(s: string): Uint8Array<ArrayBuffer> {
  const bin = atob(s)
  const out = new Uint8Array(bin.length)
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i)
  return out
}

async function deriveKey(password: string, salt: Uint8Array<ArrayBuffer>, iter: number): Promise<CryptoKey> {
  const base = await crypto.subtle.importKey("raw", new TextEncoder().encode(password), "PBKDF2", false, ["deriveKey"])
  return crypto.subtle.deriveKey(
    { name: "PBKDF2", hash: "SHA-256", salt, iterations: iter },
    base,
    { name: "AES-GCM", length: 256 },
    false,
    ["encrypt", "decrypt"],
  )
}

/** sealVault seals an identity under the password. */
export async function sealVault(content: VaultContent, password: string): Promise<VaultEnvelope> {
  const salt = crypto.getRandomValues(new Uint8Array(16))
  const iv = crypto.getRandomValues(new Uint8Array(12))
  const key = await deriveKey(password, salt, VAULT_ITERATIONS)
  const plain = new TextEncoder().encode(JSON.stringify(content))
  const ct = new Uint8Array(await crypto.subtle.encrypt({ name: "AES-GCM", iv, additionalData: AAD }, key, plain))
  return { v: 1, kdf: "PBKDF2-SHA256", iter: VAULT_ITERATIONS, salt: toBase64(salt), iv: toBase64(iv), ct: toBase64(ct) }
}

/** openVault opens an envelope, throwing WrongVaultPassword when the password does not fit. */
export async function openVault(env: VaultEnvelope, password: string): Promise<VaultContent> {
  if (env.v !== 1 || env.kdf !== "PBKDF2-SHA256") throw new Error("unsupported S/MIME key format")
  const key = await deriveKey(password, fromBase64(env.salt), env.iter)
  let plain: ArrayBuffer
  try {
    plain = await crypto.subtle.decrypt({ name: "AES-GCM", iv: fromBase64(env.iv), additionalData: AAD }, key, fromBase64(env.ct))
  } catch {
    throw new WrongVaultPassword()
  }
  return JSON.parse(new TextDecoder().decode(plain)) as VaultContent
}
