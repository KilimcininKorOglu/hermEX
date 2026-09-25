import { beforeAll, describe, expect, it } from "vitest"
import forge from "node-forge"
import { encryptMime, verifyMime } from "./smime"

// One throwaway identity for the whole file: key generation is the slow part.
let key: forge.pki.rsa.PrivateKey
let cert: forge.pki.Certificate

beforeAll(() => {
  const keys = forge.pki.rsa.generateKeyPair(1024)
  key = keys.privateKey
  cert = forge.pki.createCertificate()
  cert.publicKey = keys.publicKey
  cert.serialNumber = "01"
  cert.validity.notBefore = new Date(Date.now() - 60_000)
  cert.validity.notAfter = new Date(Date.now() + 86_400_000)
  const attrs = [{ name: "commonName", value: "Ada" }, { name: "emailAddress", value: "ada@hermex.test" }]
  cert.setSubject(attrs)
  cert.setIssuer(attrs)
  cert.sign(key, forge.md.sha256.create())
})

// signed builds a multipart/signed entity the way signMime does: a detached
// signature over the content part, with no authenticated attributes.
function signed(content: string, boundary: string): string {
  const p7 = forge.pkcs7.createSignedData()
  p7.content = forge.util.createBuffer(content, "raw")
  p7.addCertificate(cert)
  p7.addSigner({ key, certificate: cert, digestAlgorithm: forge.pki.oids.sha256 })
  p7.sign({ detached: true })
  const sig = forge.util.encode64(forge.asn1.toDer(p7.toAsn1()).getBytes())
  return (
    `Content-Type: multipart/signed; protocol="application/pkcs7-signature"; micalg="sha-256"; boundary="${boundary}"\r\n\r\n` +
    `--${boundary}\r\n${content}\r\n--${boundary}\r\n` +
    `Content-Type: application/pkcs7-signature\r\nContent-Transfer-Encoding: base64\r\n\r\n${sig}\r\n--${boundary}--\r\n`
  )
}

const content = "Content-Type: text/plain\r\n\r\nhello"

describe("verifyMime", () => {
  it("verifies a detached signature and names the signer", () => {
    expect(verifyMime(signed(content, "Mixed-Case-B"))).toMatchObject({ verified: true, signedBy: "ada@hermex.test" })
  })

  it("fails a signature over bytes that were changed", () => {
    const tampered = signed(content, "b1").replace("hello", "HELLO")
    expect(verifyMime(tampered)).toMatchObject({ verified: false, signedBy: "" })
  })

  it("returns null for an entity that is not multipart/signed or has no content part", () => {
    expect(verifyMime(content)).toBeNull()
    expect(verifyMime('Content-Type: multipart/signed; boundary="b1"\r\n\r\nno parts here')).toBeNull()
  })

  it("reports an unreadable signature as not verified", () => {
    const broken = signed(content, "b1").replace(/(base64\r\n\r\n)[^\r]+/, "$1AAAA")
    expect(verifyMime(broken)).toEqual({ verified: false, signedBy: "", signerCert: "" })
  })
})

describe("encryptMime", () => {
  it("keeps identity headers outside and encrypts the Content-* headers with the body", () => {
    const raw =
      "From: ada@hermex.test\r\nSubject: a long\r\n subject\r\nMIME-Version: 1.0\r\n" +
      "Content-Type: text/plain\r\n charset=utf-8\r\n\r\nsecret body"
    const out = encryptMime(raw, [forge.pki.certificateToPem(cert)])
    const outer = out.slice(0, out.indexOf("\r\n\r\n"))
    expect(outer.startsWith("From: ada@hermex.test\r\nSubject: a long\r\n subject\r\nMIME-Version: 1.0\r\n")).toBe(true)
    expect(out).not.toContain("secret body")

    const b64 = out.slice(out.indexOf("\r\n\r\n") + 4).replace(/\r\n/g, "")
    const p7 = forge.pkcs7.messageFromAsn1(forge.asn1.fromDer(forge.util.decode64(b64))) as unknown as {
      findRecipient(c: forge.pki.Certificate): unknown
      decrypt(r: unknown, k: forge.pki.rsa.PrivateKey): void
      content: forge.util.ByteBuffer
    }
    p7.decrypt(p7.findRecipient(cert), key)
    expect(p7.content.getBytes()).toBe("Content-Type: text/plain\r\n charset=utf-8\r\n\r\nsecret body")
  })
})
