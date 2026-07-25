// mailOptions holds the message-level options gathered in the compose "Options"
// dialog (reference MailOptions*): importance, sensitivity, and tracking. Kept
// as a small helper so the compose toolbar can flag when any option is active.

export type Importance = "low" | "normal" | "high"
export type Sensitivity = "normal" | "personal" | "private" | "confidential"

export interface MailOptionsState {
  importance: Importance
  sensitivity: Sensitivity
  requestReadReceipt: boolean
  requestDeliveryReceipt: boolean
}

// mailOptionsActive reports whether any option differs from its default, so the
// toolbar button can show the active (secondary) variant.
export function mailOptionsActive(o: MailOptionsState): boolean {
  return (
    o.importance !== "normal" ||
    o.sensitivity !== "normal" ||
    o.requestReadReceipt ||
    o.requestDeliveryReceipt
  )
}
