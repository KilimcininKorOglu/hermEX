import { useCallback, useEffect, useState } from "react"
import { Plane } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Separator } from "@/components/ui/separator"
import { Textarea } from "@/components/ui/textarea"
import { useI18n } from "@/hooks/useI18n"
import api, { type VacationAutoReply } from "@/utils/api"
import { dateToRFC3339, rfc3339ToDate } from "@/utils/settingsRecords"
import { SettingRow, SettingSection } from "./setting-layout"

const emptyVacation: VacationAutoReply = {
  enabled: false,
  subject: "Out of Office",
  message: "",
  external_message: "",
  audience: "all",
}

// useVacation manages the out-of-office auto-reply (backed by /api/v1/vacation).
function useVacation() {
  const { t } = useI18n()
  const [vacation, setVacation] = useState<VacationAutoReply>(emptyVacation)
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      setVacation({ ...emptyVacation, ...(await api.getVacation()) })
    } catch {
      setVacation(emptyVacation)
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => { void load() }, [load])

  const run = async (action: () => Promise<unknown>, okKey: string, failKey: string) => {
    setSaving(true)
    try {
      await action()
      toast.success(t(okKey))
      await load()
    } catch {
      toast.error(t(failKey))
    } finally {
      setSaving(false)
    }
  }

  const save = async () => {
    // The subject is optional on purpose: EWS and ActiveSync carry no subject
    // field, so an empty one is the normal state for a mailbox configured from
    // Outlook or a phone, and the server composes the subject for it.
    if (vacation.enabled && !vacation.message.trim()) {
      toast.error(t("settings.autoReply.messageRequired"))
      return
    }
    await run(() => api.setVacation(vacation), "settings.autoReply.saved", "settings.autoReply.saveFailed")
  }

  const disable = () => run(() => api.deleteVacation(), "settings.autoReply.disabled", "settings.autoReply.disableFailed")

  return { vacation, setVacation, loading, saving, save, disable }
}

type VacationEdit = { vacation: VacationAutoReply; onChange: (v: VacationAutoReply) => void }

function SubjectAndAudience({ vacation, onChange }: VacationEdit) {
  const { t } = useI18n()
  // The placeholder is what an empty field actually produces: the server's
  // prefix followed by the subject of the message it answers.
  const placeholder = vacation.subject_prefix ? `${vacation.subject_prefix}: ...` : t("settings.autoReply.subjectPlaceholder")
  return (
    <>
      <div className="space-y-2">
        <Label htmlFor="vacation-subject">{t("common.subject")}</Label>
        <Input
          id="vacation-subject"
          value={vacation.subject}
          onChange={(e) => onChange({ ...vacation, subject: e.target.value })}
          placeholder={placeholder}
          disabled={!vacation.enabled}
        />
        <p className="text-xs text-muted-foreground">{t("settings.autoReply.subjectOptional")}</p>
      </div>
      <div className="space-y-2">
        <Label htmlFor="vacation-audience">{t("settings.autoReply.audience")}</Label>
        <select
          id="vacation-audience"
          value={vacation.audience || "all"}
          onChange={(e) => onChange({ ...vacation, audience: e.target.value })}
          disabled={!vacation.enabled}
          className="max-w-[20rem] rounded-lg border bg-background px-3 py-2 text-sm outline-none focus:ring-2 focus:ring-primary/20"
        >
          <option value="all">{t("settings.autoReply.audienceAll")}</option>
          <option value="internal">{t("settings.autoReply.audienceInternal")}</option>
          <option value="external">{t("settings.autoReply.audienceExternal")}</option>
        </select>
      </div>
    </>
  )
}

function Messages({ vacation, onChange }: VacationEdit) {
  const { t } = useI18n()
  return (
    <>
      <div className="space-y-2">
        <Label htmlFor="vacation-message">{t("settings.autoReply.internalMessage")}</Label>
        <Textarea
          id="vacation-message"
          value={vacation.message}
          onChange={(e) => onChange({ ...vacation, message: e.target.value })}
          placeholder={t("settings.autoReply.messagePlaceholder")}
          rows={4}
          disabled={!vacation.enabled}
        />
      </div>
      {vacation.audience !== "internal" && (
        <div className="space-y-2">
          <Label htmlFor="vacation-external-message">{t("settings.autoReply.externalMessage")}</Label>
          <Textarea
            id="vacation-external-message"
            value={vacation.external_message || ""}
            onChange={(e) => onChange({ ...vacation, external_message: e.target.value })}
            placeholder={t("settings.autoReply.externalMessagePlaceholder")}
            rows={4}
            disabled={!vacation.enabled}
          />
          <p className="text-xs text-muted-foreground">{t("settings.autoReply.externalMessageHelp")}</p>
        </div>
      )}
    </>
  )
}

function DateRange({ vacation, onChange }: VacationEdit) {
  const { t } = useI18n()
  const fields: { id: string; key: "start_date" | "end_date"; label: string }[] = [
    { id: "vacation-start", key: "start_date", label: "settings.autoReply.startDate" },
    { id: "vacation-end", key: "end_date", label: "settings.autoReply.endDate" },
  ]
  return (
    <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
      {fields.map((f) => (
        <div key={f.id} className="space-y-2">
          <Label htmlFor={f.id}>{t(f.label)}</Label>
          <Input
            id={f.id}
            type="date"
            value={rfc3339ToDate(vacation[f.key])}
            onChange={(e) => onChange({ ...vacation, [f.key]: dateToRFC3339(e.target.value) })}
            disabled={!vacation.enabled}
          />
        </div>
      ))}
    </div>
  )
}

export function AutoReplySection() {
  const { t } = useI18n()
  const v = useVacation()
  return (
    <SettingSection icon={Plane} title={t("settings.autoReply.title")} description={t("settings.autoReply.description")}>
      {v.loading ? (
        <p className="text-sm text-muted-foreground py-3">{t("common.loading")}</p>
      ) : (
        <div className="space-y-4">
          <SettingRow
            title={t("settings.autoReply.enable")}
            description={t("settings.autoReply.enableDescription")}
            checked={v.vacation.enabled}
            onChange={() => v.setVacation({ ...v.vacation, enabled: !v.vacation.enabled })}
          />
          <Separator />
          <SubjectAndAudience vacation={v.vacation} onChange={v.setVacation} />
          <Messages vacation={v.vacation} onChange={v.setVacation} />
          <DateRange vacation={v.vacation} onChange={v.setVacation} />
          <div className="flex items-center gap-2">
            <Button onClick={() => void v.save()} disabled={v.saving}>{t("common.save")}</Button>
            <Button variant="outline" onClick={() => void v.disable()} disabled={v.saving}>{t("settings.disable")}</Button>
          </div>
        </div>
      )}
    </SettingSection>
  )
}
