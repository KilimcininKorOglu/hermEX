import type { SenderIdentity, SharedMailbox } from "@/utils/api"

/**
 * Builds the compose-picker identity of a shared mailbox from the send grant the
 * server reports for it. A mailbox the caller may open but holds no send grant on
 * stays listed and cannot be picked, because the send gate would refuse it.
 */
export function sharedSenderIdentity(mb: SharedMailbox): SenderIdentity {
  return {
    email: mb.owner,
    displayName: `${mb.mailbox} (${mb.owner})`,
    type: mb.sendGrant === "send-as" ? "send-as" : "send-on-behalf",
    mailboxOwner: mb.owner,
    canSend: mb.sendGrant === "send-as" || mb.sendGrant === "on-behalf",
  }
}
