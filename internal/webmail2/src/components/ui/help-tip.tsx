import { CircleHelp } from "lucide-react"

import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip"
import { useI18n } from "@/hooks/useI18n"

// HelpTip is a small "?" button beside a setting's label. Its explanation shows
// on hover and on keyboard focus. It is a button of type "button", so a click
// inside a form or a label neither submits the form nor toggles a control.
export function HelpTip({ text }: { text: string }) {
  const { t } = useI18n()
  return (
    <TooltipProvider delayDuration={150}>
      <Tooltip>
        <TooltipTrigger asChild>
          <button
            type="button"
            aria-label={t("help.label")}
            onClick={(e) => e.preventDefault()}
            className="inline-flex h-4 w-4 shrink-0 items-center justify-center rounded-full align-middle text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <CircleHelp className="h-4 w-4" aria-hidden="true" />
          </button>
        </TooltipTrigger>
        <TooltipContent className="max-w-[280px] whitespace-normal text-xs leading-snug">{text}</TooltipContent>
      </Tooltip>
    </TooltipProvider>
  )
}
