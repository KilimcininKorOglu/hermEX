import { useEffect, useState } from "react"
import { toast } from "sonner"
import { useAuth } from "@/contexts/AuthContext"
import { useI18n } from "@/hooks/useI18n"
import api from "@/utils/api"

export interface ProfileFields {
  display_name: string
  title: string
  department: string
  phone: string
}

export interface QuotaInfo {
  used: number
  limit: number
  warn: number
  prohibitSend: number
}

// useProfile owns the directory profile (shown in the GAL and on Outlook contact
// cards), the read-only storage usage with its quota thresholds (absolute bytes,
// 0 = disabled or unlimited), and the display time zone, all read from one
// GET /profile.
export function useProfile() {
  const { updatePrefs } = useAuth()
  const { t } = useI18n()
  const [profile, setProfile] = useState<ProfileFields>({ display_name: "", title: "", department: "", phone: "" })
  const [profileBusy, setProfileBusy] = useState(false)
  const [quota, setQuota] = useState<QuotaInfo>({ used: 0, limit: 0, warn: 0, prohibitSend: 0 })
  // timezone is the IANA zone every date is rendered in; empty means "follow
  // this device".
  const [timezone, setTimezone] = useState("")

  useEffect(() => {
    api.getProfile()
      .then((p) => {
        setProfile({
          display_name: p.display_name ?? "",
          title: p.title ?? "",
          department: p.department ?? "",
          phone: p.phone ?? "",
        })
        setQuota({
          used: p.quota_used ?? 0,
          limit: p.quota_limit ?? 0,
          warn: p.quota_warn ?? 0,
          prohibitSend: p.quota_prohibit_send ?? 0,
        })
        setTimezone(p.timezone ?? "")
      })
      .catch(() => undefined)
  }, [])

  const saveProfile = async () => {
    setProfileBusy(true)
    try {
      await api.updateProfile(profile)
      toast.success(t("settings.profile.updated"))
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("settings.profile.updateFailed"))
    } finally {
      setProfileBusy(false)
    }
  }

  // changeTimezone saves the zone and applies it app-wide. A failed save returns
  // the select to the stored zone.
  const changeTimezone = async (tz: string) => {
    const prev = timezone
    setTimezone(tz)
    try {
      await api.updateProfile({ timezone: tz })
      updatePrefs({ timezone: tz })
      toast.success(t("settings.appearance.timezoneSaved"))
    } catch (err) {
      setTimezone(prev)
      toast.error(err instanceof Error ? err.message : t("settings.appearance.timezoneSaveFailed"))
    }
  }

  return { profile, setProfile, profileBusy, saveProfile, quota, timezone, changeTimezone }
}
