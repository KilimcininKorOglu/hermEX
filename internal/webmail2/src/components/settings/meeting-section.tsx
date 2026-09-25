import { useEffect, useState } from "react"
import { CalendarCheck } from "lucide-react"
import { toast } from "sonner"
import { Separator } from "@/components/ui/separator"
import { useI18n } from "@/hooks/useI18n"
import { useBusyGate } from "@/hooks/useBusyGate"
import api, { type MeetingSettings } from "@/utils/api"
import { SettingRow, SettingSection } from "./setting-layout"

type MeetingKey = keyof MeetingSettings

const ROWS: [MeetingKey, string][] = [
  ["processCancellations", "processCancellations"],
  ["removeRequestOnResponse", "removeRequest"],
]

// useMeetingSettings loads the meeting handling settings and saves one switch at
// a time; the gate holds a second toggle until the first save settles. A failed
// save restores the stored value, so the switch never shows a setting that was
// not kept.
function useMeetingSettings() {
  const { t } = useI18n()
  const [settings, setSettings] = useState<MeetingSettings | null>(null)
  const gate = useBusyGate()

  useEffect(() => {
    api.getMeetingSettings().then(setSettings, () => toast.error(t("settings.notLoaded")))
  }, [t])

  const toggle = async (key: MeetingKey) => {
    if (!settings || !gate.begin()) return
    const before = settings
    const next = { ...settings, [key]: !settings[key] }
    setSettings(next)
    try {
      setSettings(await api.setMeetingSettings(next))
      toast.success(t("settings.settingUpdated"))
    } catch {
      setSettings(before)
      toast.error(t("settings.settingSaveFailed"))
    } finally {
      gate.end()
    }
  }
  return { settings, toggle }
}

// MeetingSection holds how the mailbox handles meeting mail it receives.
export function MeetingSection() {
  const { t } = useI18n()
  const { settings, toggle } = useMeetingSettings()
  return (
    <SettingSection icon={CalendarCheck} title={t("settings.meeting.title")} description={t("settings.meeting.description")}>
      <div className="space-y-1">
        {settings && ROWS.map(([key, name], i) => (
          <div key={key}>
            {i > 0 && <Separator />}
            <SettingRow
              title={t(`settings.meeting.${name}`)}
              description={t(`settings.meeting.${name}Description`)}
              checked={settings[key]}
              onChange={() => void toggle(key)}
            />
          </div>
        ))}
      </div>
    </SettingSection>
  )
}
