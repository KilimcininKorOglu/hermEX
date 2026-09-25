import { Separator } from "@/components/ui/separator"
import { useI18n } from "@/hooks/useI18n"
import type { CalendarToastKeys, DisplaySettings } from "@/hooks/useDisplaySettings"
import type { CalendarSettings } from "@/utils/api"
import { FieldRow, numberOptions, SelectField, SettingRow } from "./setting-layout"

const WEEKDAY_KEYS = ["sun", "mon", "tue", "wed", "thu", "fri", "sat"].map((d) => `calendar.weekdays.${d}`)
const HOURS = Array.from({ length: 24 }, (_, h) => h)
const hourLabel = (h: number) => `${String(h).padStart(2, "0")}:00`

const WORK_HOURS_KEYS: CalendarToastKeys = {
  saved: "settings.appearance.workHoursSaved",
  failed: "settings.appearance.workHoursSaveFailed",
}
const SETTING_KEYS: CalendarToastKeys = { failed: "settings.settingSaveFailed" }

// toggledDay adds a day to the work days or removes it.
function toggledDay(days: number[], day: number): number[] {
  return days.includes(day) ? days.filter((d) => d !== day) : [...days, day]
}

function WorkDayPicker({ days, onChange }: { days: number[]; onChange: (days: number[]) => void }) {
  const { t } = useI18n()
  return (
    <div className="flex gap-1">
      {WEEKDAY_KEYS.map((key, idx) => {
        const label = t(key)
        const on = days.includes(idx)
        return (
          <button
            key={idx}
            type="button"
            title={label}
            className={`h-8 w-8 rounded text-xs ${on ? "bg-primary text-primary-foreground" : "bg-muted text-muted-foreground"}`}
            onClick={() => onChange(toggledDay(days, idx))}
          >
            {label.charAt(0)}
          </button>
        )
      })}
    </div>
  )
}

// CalendarGridFields are the week start, the slot resolution and the working
// hours of the calendar grids.
function CalendarGridFields({ calendar, save }: { calendar: CalendarSettings; save: DisplaySettings["saveCalendar"] }) {
  const { t } = useI18n()
  const minutes = (n: number) => t("calendar.resolutionMinutes", { n: String(n) })
  const hours = numberOptions(HOURS, hourLabel)
  return (
    <>
      <SelectField
        title={t("settings.appearance.firstDayOfWeek")}
        description={t("settings.appearance.firstDayOfWeekDescription")}
        value={String(calendar.firstDayOfWeek)}
        options={WEEKDAY_KEYS.map((key, idx) => ({ value: String(idx), label: t(key) }))}
        onChange={(v) => void save({ firstDayOfWeek: Number(v) }, {
          saved: "settings.appearance.firstDaySaved", failed: "settings.appearance.firstDaySaveFailed",
        })}
      />
      <Separator />
      <SelectField
        title={t("settings.appearance.resolution")}
        description={t("settings.appearance.resolutionDescription")}
        value={String(calendar.resolution)}
        options={numberOptions([5, 10, 15, 30, 60], minutes)}
        onChange={(v) => void save({ resolution: Number(v) }, {
          saved: "settings.appearance.resolutionSaved", failed: "settings.appearance.resolutionSaveFailed",
        })}
      />
      <Separator />
      <SelectField
        title={t("settings.appearance.workDayStart")}
        description={t("settings.appearance.workHoursDescription")}
        value={String(calendar.workDayStart)}
        options={hours}
        onChange={(v) => void save({ workDayStart: Number(v) }, WORK_HOURS_KEYS)}
      />
      <Separator />
      <SelectField
        title={t("settings.appearance.workDayEnd")}
        description={t("settings.appearance.workHoursDescription")}
        value={String(calendar.workDayEnd)}
        options={hours}
        onChange={(v) => void save({ workDayEnd: Number(v) }, WORK_HOURS_KEYS)}
      />
      <Separator />
      <SettingRow
        title={t("settings.appearance.showNonWorkingHours")}
        description={t("settings.appearance.workHoursDescription")}
        checked={calendar.showNonWorkingHours}
        onChange={() => void save({ showNonWorkingHours: !calendar.showNonWorkingHours }, WORK_HOURS_KEYS)}
      />
    </>
  )
}

// CalendarControls are the calendar settings, shown in the appearance section.
export function CalendarControls({ display }: { display: DisplaySettings }) {
  const { t } = useI18n()
  const { calendar, saveCalendar: save } = display
  const minutes = (n: number) => t("calendar.resolutionMinutes", { n: String(n) })
  return (
    <>
      <CalendarGridFields calendar={calendar} save={save} />
      <Separator />
      <FieldRow title={t("settings.appearance.workDays")} description={t("settings.appearance.workDaysDescription")}>
        <WorkDayPicker days={calendar.workDays} onChange={(workDays) => void save({ workDays }, SETTING_KEYS)} />
      </FieldRow>
      <Separator />
      <SelectField
        title={t("settings.appearance.defaultDuration")}
        description={t("settings.appearance.defaultDurationDescription")}
        value={String(calendar.defaultDuration)}
        options={numberOptions([15, 30, 45, 60, 90], minutes)}
        onChange={(v) => void save({ defaultDuration: Number(v) }, SETTING_KEYS)}
      />
      <Separator />
      <SelectField
        title={t("settings.appearance.defaultReminder")}
        description={t("settings.appearance.defaultReminderDescription")}
        value={String(calendar.defaultReminder)}
        options={[{ value: "0", label: t("settings.appearance.noReminder") }, ...numberOptions([5, 10, 15, 30, 60], minutes)]}
        onChange={(v) => void save({ defaultReminder: Number(v) }, SETTING_KEYS)}
      />
    </>
  )
}
