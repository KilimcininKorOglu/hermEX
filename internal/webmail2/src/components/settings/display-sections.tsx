import { Fragment } from "react"
import { Columns3, FileText, Info, Keyboard, MoveVertical, Wrench } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Separator } from "@/components/ui/separator"
import { useI18n } from "@/hooks/useI18n"
import type { DisplaySettings } from "@/hooks/useDisplaySettings"
import type { MailListColumns } from "@/utils/api"
import { detectBrowser } from "@/utils/settingsRecords"
import { CheckboxField, FieldRow, PageSizeInput, SELECT_CLASS, SelectField, SettingSection } from "./setting-layout"

export function ShortcutsSection({ display }: { display: DisplaySettings }) {
  const { t } = useI18n()
  const { appearance, saveAppearance } = display
  return (
    <SettingSection icon={Keyboard} title={t("settings.shortcuts.title")} description={t("settings.shortcuts.description")}>
      <div className="space-y-3">
        <SelectField
          title={t("settings.shortcuts.mode")}
          description={t("settings.shortcuts.modeDescription")}
          value={appearance.shortcutMode}
          options={[
            { value: "off", label: t("settings.shortcuts.modeOff") },
            { value: "basic", label: t("settings.shortcuts.modeBasic") },
            { value: "extended", label: t("settings.shortcuts.modeExtended") },
          ]}
          onChange={(shortcutMode) => saveAppearance({ shortcutMode })}
        />
        <p className="text-sm text-muted-foreground">{t("settings.shortcuts.help")}</p>
        <Button variant="outline" onClick={() => document.dispatchEvent(new CustomEvent("toggle-shortcuts"))}>
          {t("settings.shortcuts.view")}
        </Button>
      </div>
    </SettingSection>
  )
}

// OPTIONAL_COLUMNS are the message-list columns a user can hide; sender, subject
// and date always show.
const OPTIONAL_COLUMNS: (keyof MailListColumns)[] = ["preview", "attachment", "importance", "categories", "size", "flag"]

export function MailColumnsSection({ display }: { display: DisplaySettings }) {
  const { t } = useI18n()
  const { appearance, saveAppearance } = display
  return (
    <SettingSection icon={Columns3} title={t("settings.mailColumns.title")} description={t("settings.mailColumns.description")}>
      {OPTIONAL_COLUMNS.map((key, i) => (
        <Fragment key={key}>
          {i > 0 && <Separator />}
          <CheckboxField
            title={t(`settings.mailColumns.${key}`)}
            description={t(`settings.mailColumns.${key}Description`)}
            checked={appearance.mailListColumns[key]}
            onChange={(checked) => saveAppearance({ mailListColumns: { ...appearance.mailListColumns, [key]: checked } })}
          />
        </Fragment>
      ))}
    </SettingSection>
  )
}

// InboxNavSection chooses whether the message list paginates or scrolls
// infinitely, and the page or block size.
export function InboxNavSection({ display }: { display: DisplaySettings }) {
  const { t } = useI18n()
  const { appearance, saveAppearance } = display
  return (
    <SettingSection icon={MoveVertical} title={t("settings.inboxNav.title")} description={t("settings.inboxNav.description")}>
      <SelectField
        title={t("settings.inboxNav.mode")}
        description={t("settings.inboxNav.modeDescription")}
        value={appearance.inboxNavMode}
        options={[
          { value: "pagination", label: t("settings.inboxNav.pagination") },
          { value: "infinite", label: t("settings.inboxNav.infinite") },
        ]}
        onChange={(inboxNavMode) => saveAppearance({ inboxNavMode })}
        className="rounded-md border bg-background px-3 py-2 text-sm"
      />
      <Separator />
      <FieldRow title={t("settings.inboxNav.pageSize")} description={t("settings.inboxNav.pageSizeDescription")}>
        <PageSizeInput
          key={appearance.inboxPageSize}
          value={appearance.inboxPageSize}
          onCommit={(inboxPageSize) => saveAppearance({ inboxPageSize })}
        />
      </FieldRow>
    </SettingSection>
  )
}

// FilePreviewSection controls the inline preview of PDF and image attachments,
// and the PDF zoom mode.
export function FilePreviewSection({ display }: { display: DisplaySettings }) {
  const { t } = useI18n()
  const { appearance, saveAppearance } = display
  return (
    <SettingSection icon={FileText} title={t("settings.filePreview.title")} description={t("settings.filePreview.description")}>
      <CheckboxField
        title={t("settings.filePreview.enable")}
        description={t("settings.filePreview.enableDescription")}
        checked={appearance.filePreview}
        onChange={(filePreview) => saveAppearance({ filePreview })}
      />
      <Separator />
      <SelectField
        title={t("settings.filePreview.pdfZoom")}
        description={t("settings.filePreview.pdfZoomDescription")}
        value={appearance.pdfZoom}
        disabled={!appearance.filePreview}
        options={[
          { value: "auto", label: t("settings.filePreview.zoomAuto") },
          { value: "page-actual", label: t("settings.filePreview.zoomActual") },
          { value: "page-width", label: t("settings.filePreview.zoomWidth") },
        ]}
        onChange={(pdfZoom) => saveAppearance({ pdfZoom })}
        className={`${SELECT_CLASS} disabled:opacity-50`}
      />
    </SettingSection>
  )
}

// AdvancedSection holds the developer tools.
export function AdvancedSection({ display }: { display: DisplaySettings }) {
  const { t } = useI18n()
  const { appearance, saveAppearance } = display
  return (
    <SettingSection icon={Wrench} title={t("settings.advanced.title")} description={t("settings.advanced.description")}>
      <p className="mb-3 text-sm font-medium">{t("settings.advanced.developerTools")}</p>
      <CheckboxField
        title={t("settings.advanced.itemData")}
        description={t("settings.advanced.itemDataDescription")}
        checked={appearance.showItemData}
        onChange={(showItemData) => saveAppearance({ showItemData })}
      />
    </SettingSection>
  )
}

// AboutSection shows the system information.
export function AboutSection({ version }: { version: string }) {
  const { t } = useI18n()
  const ua = typeof navigator === "undefined" ? "" : navigator.userAgent
  return (
    <SettingSection icon={Info} title={t("settings.about.title")} description={t("settings.about.description")}>
      <dl className="grid grid-cols-[max-content_1fr] gap-x-6 gap-y-2 text-sm">
        <dt className="font-medium">{t("settings.about.application")}</dt>
        <dd className="text-muted-foreground">hermEX Webmail</dd>
        <dt className="font-medium">{t("settings.about.version")}</dt>
        <dd className="text-muted-foreground">{version}</dd>
        <dt className="font-medium">{t("settings.about.signedInVia")}</dt>
        <dd className="text-muted-foreground">{t("settings.about.password")}</dd>
        <dt className="font-medium">{t("settings.about.browser")}</dt>
        <dd className="text-muted-foreground">{detectBrowser(ua)}</dd>
      </dl>
    </SettingSection>
  )
}

// SettingsFooter shows the version, resets the settings and asks for the
// browser's notification permission.
export function SettingsFooter({ version, display }: { version: string; display: DisplaySettings }) {
  const { t } = useI18n()
  return (
    <div className="text-center text-sm text-muted-foreground pb-8 space-y-2">
      <p>hermEX Webmail {version}</p>
      <p>{t("settings.footer.tagline")}</p>
      <Button
        variant="outline"
        size="sm"
        className="mt-2"
        disabled={display.resetting}
        onClick={() => {
          if (window.confirm(t("settings.reset.confirm"))) void display.resetSettings()
        }}
      >
        {display.resetting ? t("common.loading") : t("settings.reset.resetSettings")}
      </Button>
      <div>
        <Button
          variant="outline"
          size="sm"
          className="mt-2"
          onClick={() => {
            if (typeof Notification === "undefined") return
            Notification.requestPermission().catch(() => undefined)
          }}
        >
          {t("settings.notifications.enable")}
        </Button>
        <p className="mt-1 text-xs">{t("settings.notifications.description")}</p>
      </div>
    </div>
  )
}
