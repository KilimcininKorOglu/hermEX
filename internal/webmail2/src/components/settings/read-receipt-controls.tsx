import { useEffect, useState } from "react"
import { toast } from "sonner"
import { Separator } from "@/components/ui/separator"
import { useBusyGate } from "@/hooks/useBusyGate"
import { useI18n } from "@/hooks/useI18n"
import api, { type ReadReceiptResponse, type ReadReceiptSettings } from "@/utils/api"
import { SelectField, SettingRow } from "./setting-layout"

const RESPONSES: ReadReceiptResponse[] = ["ask", "always", "never"]

// useReadReceiptSettings loads the read-receipt settings and saves the whole
// object on each change; the gate holds a second change until the first save
// settles. A failed save restores the stored value, so a control never shows a
// setting that was not kept.
function useReadReceiptSettings() {
  const { t } = useI18n()
  const [settings, setSettings] = useState<ReadReceiptSettings | null>(null)
  const gate = useBusyGate()

  useEffect(() => {
    api.getReadReceiptSettings().then(setSettings, () => toast.error(t("settings.notLoaded")))
  }, [t])

  const save = async (patch: Partial<ReadReceiptSettings>) => {
    if (!settings || !gate.begin()) return
    const before = settings
    const next = { ...settings, ...patch }
    setSettings(next)
    try {
      setSettings(await api.setReadReceiptSettings(next))
      toast.success(t("settings.settingUpdated"))
    } catch {
      setSettings(before)
      toast.error(t("settings.settingSaveFailed"))
    } finally {
      gate.end()
    }
  }
  return { settings, save, busy: gate.busy }
}

// ReadReceiptControls holds how the mailbox answers a message that asks for a
// read receipt: in webmail (ask, always, never) and on ActiveSync devices. A
// failed load shows nothing, so a save can never overwrite the stored settings
// with defaults.
export function ReadReceiptControls() {
  const { t } = useI18n()
  const { settings, save, busy } = useReadReceiptSettings()
  if (!settings) return null
  return (
    <>
      <div className="py-3">
        <SelectField
          title={t("settings.privacy.readReceiptResponse")}
          description={t("settings.privacy.readReceiptResponseDescription")}
          value={settings.response}
          disabled={busy}
          options={RESPONSES.map((r) => ({ value: r, label: t(`settings.privacy.readReceiptResponse_${r}`) }))}
          onChange={(v) => void save({ response: v as ReadReceiptResponse })}
        />
      </div>
      <Separator />
      <SettingRow
        title={t("settings.privacy.readReceiptActiveSync")}
        description={t("settings.privacy.readReceiptActiveSyncDescription")}
        checked={!settings.suppressActiveSync}
        onChange={() => void save({ suppressActiveSync: !settings.suppressActiveSync })}
      />
    </>
  )
}
