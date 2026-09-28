import { MailCheck } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { useBusyGate } from "@/hooks/useBusyGate"
import { useI18n } from "@/hooks/useI18n"
import api from "@/utils/api"

// ReadReceiptBanner asks the reader whether to send the read receipt the open
// message is waiting for. It shows only when the reader chose to be asked; either
// answer ends the prompt, because declining consumes the request on the server.
// onAnswered runs once the server kept the answer, so the prompt stays on a failure
// and the reader can answer again.
export function ReadReceiptBanner({ id, onAnswered }: { id: string; onAnswered: () => void }) {
  const { t } = useI18n()
  const gate = useBusyGate()

  const answer = async (send: boolean) => {
    if (!gate.begin()) return
    try {
      await api.answerReadReceipt(id, send)
      if (send) toast.success(t("emailDetail.readReceiptSent"))
      onAnswered()
    } catch {
      toast.error(t("emailDetail.readReceiptFailed"))
    } finally {
      gate.end()
    }
  }

  return (
    <div className="mb-4 flex flex-wrap items-center justify-between gap-3 rounded-md border border-sky-300 bg-sky-50 px-3 py-2 text-sm text-sky-900 dark:border-sky-700 dark:bg-sky-950 dark:text-sky-100">
      <span className="flex items-center gap-2">
        <MailCheck className="h-4 w-4 shrink-0" />
        {t("emailDetail.readReceiptRequested")}
      </span>
      <span className="flex gap-2">
        <Button variant="outline" size="sm" disabled={gate.busy} onClick={() => void answer(false)}>
          {t("emailDetail.readReceiptDecline")}
        </Button>
        <Button size="sm" disabled={gate.busy} onClick={() => void answer(true)}>
          {t("emailDetail.readReceiptSend")}
        </Button>
      </span>
    </div>
  )
}
