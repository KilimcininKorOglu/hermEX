/**
 * The S/MIME identity an older client kept in this browser's IndexedDB.
 *
 * The key now lives in a password-sealed vault on the server, and this module
 * exists only to carry an old identity over: it reads the record once and deletes
 * it after the vault is stored. It never writes one, and it never creates the
 * database when it is absent.
 */

const DB_NAME = "hermex-smime"
const STORE = "identity"
const REC_KEY = "self"

/** LegacyIdentity is the record an older client stored: the .p12 as imported and its certificate. */
export interface LegacyIdentity {
  p12: ArrayBuffer
  certPem: string
}

// openExisting opens the database only when it already exists, answering null
// otherwise: aborting the upgrade that creating it would run leaves nothing behind.
function openExisting(): Promise<IDBDatabase | null> {
  if (typeof indexedDB === "undefined") return Promise.resolve(null)
  return new Promise((resolve, reject) => {
    const req = indexedDB.open(DB_NAME)
    let created = false
    req.onupgradeneeded = () => {
      created = true
      req.transaction?.abort()
    }
    req.onsuccess = () => resolve(req.result)
    req.onerror = () => (created ? resolve(null) : reject(req.error))
  })
}

// isLegacyIdentity checks the stored value has the fields an older client wrote.
function isLegacyIdentity(v: unknown): v is LegacyIdentity {
  const rec = v as Partial<LegacyIdentity> | undefined
  return !!rec && rec.p12 instanceof ArrayBuffer && typeof rec.certPem === "string"
}

/** readLegacyIdentity returns the identity an older client stored here, or null. */
export async function readLegacyIdentity(): Promise<LegacyIdentity | null> {
  const db = await openExisting()
  if (!db) return null
  try {
    if (!db.objectStoreNames.contains(STORE)) return null
    const value = await new Promise<unknown>((resolve, reject) => {
      const r = db.transaction(STORE, "readonly").objectStore(STORE).get(REC_KEY)
      r.onsuccess = () => resolve(r.result)
      r.onerror = () => reject(r.error)
    })
    return isLegacyIdentity(value) ? value : null
  } finally {
    db.close()
  }
}

/** dropLegacyIdentity deletes the database an older client kept the identity in. */
export function dropLegacyIdentity(): Promise<void> {
  if (typeof indexedDB === "undefined") return Promise.resolve()
  return new Promise((resolve, reject) => {
    const req = indexedDB.deleteDatabase(DB_NAME)
    req.onsuccess = () => resolve()
    req.onerror = () => reject(req.error)
  })
}
