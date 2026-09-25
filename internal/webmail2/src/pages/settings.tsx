import { useI18n } from "@/hooks/useI18n"
import { useServerVersion } from "@/hooks/useServerVersion"
import { useDisplaySettings } from "@/hooks/useDisplaySettings"
import { usePreferences } from "@/hooks/usePreferences"
import { useProfile } from "@/hooks/useProfile"
import { SecondFactorCard } from "@/components/settings/second-factor-card"
import { AppPasswordsCard } from "@/components/settings/app-passwords-card"
import { AccountSection, PushSection, SessionsSection } from "@/components/settings/account-sections"
import { AppearanceSection } from "@/components/settings/appearance-section"
import { AutoReplySection } from "@/components/settings/auto-reply-section"
import { DelegatesSection } from "@/components/settings/delegates-section"
import {
  AboutSection,
  AdvancedSection,
  FilePreviewSection,
  InboxNavSection,
  MailColumnsSection,
  SettingsFooter,
  ShortcutsSection,
} from "@/components/settings/display-sections"
import { CompositionSection, NotificationsSection, PrivacySection } from "@/components/settings/preference-sections"
import { ProfilePhotoSection, ProfileSection, StorageSection } from "@/components/settings/profile-sections"
import { CategoriesSection, RecipientRulesSection, SafeSendersSection } from "@/components/settings/sender-list-sections"
import { SignatureSection, TemplateSection } from "@/components/settings/snippet-sections"

// SettingsPage lays out the settings sections. Each section owns its own state;
// the page holds only the records several sections share: the directory profile,
// the appearance and calendar records, and the preference switches.
export function SettingsPage() {
  const { t } = useI18n()
  const serverVersion = useServerVersion()
  const profile = useProfile()
  const display = useDisplaySettings()
  const prefs = usePreferences()

  return (
    <div className="space-y-6 max-w-3xl">
      <div>
        <h2 className="text-2xl font-bold">{t("nav.settings")}</h2>
        <p className="text-muted-foreground">{t("settings.description")}</p>
      </div>
      <ProfilePhotoSection />
      <ProfileSection
        profile={profile.profile}
        onChange={profile.setProfile}
        busy={profile.profileBusy}
        onSave={() => void profile.saveProfile()}
      />
      <StorageSection quota={profile.quota} />
      <AppearanceSection
        display={display}
        timezone={profile.timezone}
        onTimezoneChange={(tz) => void profile.changeTimezone(tz)}
      />
      <NotificationsSection prefs={prefs} />
      <CompositionSection prefs={prefs} />
      <AutoReplySection />
      <SignatureSection />
      <TemplateSection />
      <CategoriesSection />
      <SafeSendersSection />
      <RecipientRulesSection />
      <DelegatesSection />
      <PrivacySection prefs={prefs} />
      <ShortcutsSection display={display} />
      <PushSection />
      <SessionsSection />
      <AccountSection />
      {/* Second factor, next to the password it backs, and the per-client
          credentials it makes necessary. */}
      <SecondFactorCard />
      <AppPasswordsCard />
      <MailColumnsSection display={display} />
      <InboxNavSection display={display} />
      <FilePreviewSection display={display} />
      <AdvancedSection display={display} />
      <AboutSection version={serverVersion} />
      <SettingsFooter version={serverVersion} display={display} />
    </div>
  )
}
