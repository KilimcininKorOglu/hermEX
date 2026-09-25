import { useCallback, useEffect, useState } from "react"
import { Plus, Trash2, UserCog } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Separator } from "@/components/ui/separator"
import { useI18n } from "@/hooks/useI18n"
import api, { type Delegation, type SentCopySettings } from "@/utils/api"
import { SettingRow, SettingSection } from "./setting-layout"

const NO_SENT_COPY: SentCopySettings = { forSendAs: false, forSendOnBehalf: false }

interface Grant {
  write: boolean
  sendOnBehalf: boolean
  sendAs: boolean
}

const NO_GRANT: Grant = { write: false, sendOnBehalf: false, sendAs: false }

// useDelegates manages the people the user grants access to their own mailbox.
function useDelegates() {
  const { t } = useI18n()
  const [delegations, setDelegations] = useState<Delegation[]>([])
  const [busy, setBusy] = useState(false)

  const load = useCallback(async () => {
    try {
      setDelegations((await api.getDelegations()).delegations ?? [])
    } catch {
      setDelegations([])
    }
  }, [])

  useEffect(() => { void load() }, [load])

  const add = async (grantee: string, grant: Grant): Promise<boolean> => {
    setBusy(true)
    try {
      await api.createDelegation({
        grantee,
        rights: grant.write ? ["read", "write"] : ["read"],
        canSendOnBehalf: grant.sendOnBehalf,
        canSendAs: grant.sendAs,
      })
      toast.success(t("settings.delegates.added"))
      await load()
      return true
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("settings.delegates.addFailed"))
      return false
    } finally {
      setBusy(false)
    }
  }

  const remove = async (id: string) => {
    setBusy(true)
    try {
      await api.deleteDelegation(id)
      toast.success(t("settings.delegates.removed"))
      await load()
    } catch {
      toast.error(t("settings.delegates.removeFailed"))
    } finally {
      setBusy(false)
    }
  }

  return { delegations, busy, add, remove }
}

// useSentCopy manages whether mail a delegate sends in this mailbox's name also
// lands in this mailbox's Sent Items. Both default off, so an upgrade changes
// nothing.
function useSentCopy() {
  const { t } = useI18n()
  const [sentCopy, setSentCopy] = useState<SentCopySettings>(NO_SENT_COPY)

  useEffect(() => {
    api.getSentCopy().then(setSentCopy).catch(() => setSentCopy(NO_SENT_COPY))
  }, [])

  // toggle sends the whole object, so changing one flag never drops the other.
  // The previous value is restored when the save fails, or the switch would show
  // a setting the server never stored.
  const toggle = async (key: keyof SentCopySettings) => {
    const prev = sentCopy
    const next = { ...sentCopy, [key]: !sentCopy[key] }
    setSentCopy(next)
    try {
      setSentCopy(await api.setSentCopy(next))
    } catch {
      setSentCopy(prev)
      toast.error(t("settings.delegates.copySaveFailed"))
    }
  }

  return { sentCopy, toggle }
}

function DelegateRow({ d, busy, onRemove }: { d: Delegation; busy: boolean; onRemove: () => void }) {
  const { t } = useI18n()
  const tags = [
    d.rights || t("settings.delegates.noAccess"),
    d.canSendOnBehalf ? t("settings.delegates.sendOnBehalfTag") : "",
    d.canSendAs ? t("settings.delegates.sendAsTag") : "",
  ].filter(Boolean)
  return (
    <div className="flex items-center justify-between rounded-lg border p-3">
      <div className="min-w-0">
        <p className="font-medium truncate">{d.grantee}</p>
        <p className="text-xs text-muted-foreground">{tags.join(" · ")}</p>
      </div>
      <Button variant="ghost" size="icon" className="h-8 w-8 text-destructive" onClick={onRemove} disabled={busy}>
        <Trash2 className="h-4 w-4" />
      </Button>
    </div>
  )
}

const GRANT_ROWS: { key: keyof Grant; name: string }[] = [
  { key: "write", name: "allowEditing" },
  { key: "sendOnBehalf", name: "sendOnBehalf" },
  { key: "sendAs", name: "sendAs" },
]

function AddDelegateForm({ busy, onAdd }: { busy: boolean; onAdd: (grantee: string, grant: Grant) => Promise<boolean> }) {
  const { t } = useI18n()
  const [email, setEmail] = useState("")
  const [grant, setGrant] = useState<Grant>(NO_GRANT)

  const submit = async () => {
    const grantee = email.trim().toLowerCase()
    if (!grantee) {
      toast.error(t("settings.delegates.emailRequired"))
      return
    }
    if (await onAdd(grantee, grant)) {
      setEmail("")
      setGrant(NO_GRANT)
    }
  }

  return (
    <div className="space-y-3 rounded-lg border p-3">
      <div className="space-y-2">
        <Label htmlFor="delegate-email">{t("settings.delegates.add")}</Label>
        <Input
          id="delegate-email"
          type="email"
          value={email}
          onChange={(e) => setEmail(e.target.value)}
          placeholder={t("settings.delegates.emailPlaceholder")}
        />
      </div>
      {GRANT_ROWS.map(({ key, name }) => (
        <SettingRow
          key={key}
          title={t(`settings.delegates.${name}`)}
          description={t(`settings.delegates.${name}Description`)}
          checked={grant[key]}
          onChange={() => setGrant((g) => ({ ...g, [key]: !g[key] }))}
        />
      ))}
      <Button onClick={() => void submit()} disabled={busy || !email.trim()}>
        <Plus className="mr-2 h-4 w-4" />
        {t("settings.delegates.addDelegate")}
      </Button>
    </div>
  )
}

function SentCopyOptions() {
  const { t } = useI18n()
  const { sentCopy, toggle } = useSentCopy()
  return (
    <div className="space-y-1 rounded-lg border p-3">
      <p className="text-sm font-medium">{t("settings.delegates.copyTitle")}</p>
      <SettingRow
        title={t("settings.delegates.copySendAs")}
        description={t("settings.delegates.copySendAsDescription")}
        checked={sentCopy.forSendAs}
        onChange={() => void toggle("forSendAs")}
      />
      <Separator />
      <SettingRow
        title={t("settings.delegates.copySendOnBehalf")}
        description={t("settings.delegates.copySendOnBehalfDescription")}
        checked={sentCopy.forSendOnBehalf}
        onChange={() => void toggle("forSendOnBehalf")}
      />
    </div>
  )
}

export function DelegatesSection() {
  const { t } = useI18n()
  const delegates = useDelegates()
  return (
    <SettingSection icon={UserCog} title={t("settings.delegates.title")} description={t("settings.delegates.description")}>
      <div className="space-y-4">
        {delegates.delegations.length > 0 && (
          <div className="space-y-2">
            {delegates.delegations.map((d) => (
              <DelegateRow key={d.id} d={d} busy={delegates.busy} onRemove={() => void delegates.remove(d.id)} />
            ))}
          </div>
        )}
        <AddDelegateForm busy={delegates.busy} onAdd={delegates.add} />
        <SentCopyOptions />
      </div>
    </SettingSection>
  )
}
