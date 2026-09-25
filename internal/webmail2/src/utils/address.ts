// angleAddress matches the "Name <addr@x>" form: a display name, then one address
// in angle brackets at the end of the value.
const angleAddress = /^(.*?)<([^<>]*)>\s*$/

// splitAddress turns "Name <addr@x>" or "addr@x" into {name, email}. A value
// without a complete angle-bracket address is used whole for both, and an empty
// display name falls back to the address. The result is display data rendered as
// text, not markup.
export function splitAddress(value: string): { name: string; email: string } {
  const m = angleAddress.exec(value)
  if (!m) return { name: value, email: value }
  const email = m[2].trim()
  return { name: m[1].trim() || email, email }
}
