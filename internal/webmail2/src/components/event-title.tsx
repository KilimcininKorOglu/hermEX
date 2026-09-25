import { useI18n } from "@/hooks/useI18n"
import type { CalendarEvent } from "@/utils/api"

// eventTitleText is the event's title as a tooltip reads it: a cancelled meeting
// says so first, because the strike-through alone does not reach a screen reader
// or a tooltip.
export function eventTitleText(ev: CalendarEvent, canceledLabel: string): string {
  return ev.canceled ? `${canceledLabel}: ${ev.summary}` : ev.summary
}

// EventTitle renders an event's title. A meeting its organizer cancelled is
// labelled and struck through, so it reads as called off until the user removes
// it.
export function EventTitle({ ev }: { ev: CalendarEvent }) {
  const { t } = useI18n()
  if (!ev.canceled) return <>{ev.summary}</>
  return (
    <>
      <span className="mr-1 font-semibold text-destructive">{t("calendar.canceled")}</span>
      <span className="line-through">{ev.summary}</span>
    </>
  )
}
