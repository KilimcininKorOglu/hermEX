import { useState, useEffect } from "react"
import { toast } from "sonner"
import { UserPlus, Trash2, Loader2 } from "lucide-react"
import { useI18n } from "@/hooks/useI18n"
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogFooter,
} from "@/components/ui/dialog"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Badge } from "@/components/ui/badge"
import { Separator } from "@/components/ui/separator"
import { Checkbox } from "@/components/ui/checkbox"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"
import api, {
  ACLEntry,
  FOLDER_PROFILES,
  FOLDER_RIGHTS,
  FolderProfile,
  rightsToProfile,
  profileToRights,
} from "@/utils/api"

interface ShareFolderDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** Canonical folder name, e.g. "INBOX", "Work", "calendar-uuid" */
  folderName: string
  /** Display label shown in the dialog title */
  folderLabel: string
  /** Owner's email, logged-in user for a personal folder, else the mailbox owner */
  owner: string
  /** Whether the current user is the folder owner (can manage ACL) */
  isOwner: boolean
}

export function ShareFolderDialog({
  open,
  onOpenChange,
  folderName,
  folderLabel,
  owner,
  isOwner,
}: ShareFolderDialogProps) {
  const { t } = useI18n()
  const [grants, setGrants] = useState<ACLEntry[]>([])
  const [loading, setLoading] = useState(false)
  const [saving, setSaving] = useState(false)

  // New grant form: grantee, the raw Frights bitmask, and whether to cascade.
  const [newGrantee, setNewGrantee] = useState("")
  const [rights, setRights] = useState<number>(profileToRights("reviewer")!)
  const [recursive, setRecursive] = useState(false)

  const profile = rightsToProfile(rights)

  useEffect(() => {
    if (!open || !owner || !folderName) return
    setLoading(true)
    api.getACL(owner, folderName)
      .then((data) => setGrants(data.acl || []))
      .catch(() => toast.error(t("share.fetchFailed")))
      .finally(() => setLoading(false))
  }, [open, owner, folderName, t])

  // Selecting a named profile snaps the bitmask to its preset; "custom" is only
  // ever a display state, so it leaves the current bits untouched.
  const handleProfileChange = (value: string) => {
    const preset = profileToRights(value as FolderProfile)
    if (preset !== undefined) setRights(preset)
  }

  const toggleRight = (bit: number, on: boolean) => {
    setRights((prev) => (on ? prev | bit : prev & ~bit))
  }

  const handleAddGrant = async () => {
    if (!newGrantee.trim()) return
    setSaving(true)
    try {
      await api.setACL(owner, folderName, newGrantee.trim().toLowerCase(), rights, recursive)
      const data = await api.getACL(owner, folderName)
      setGrants(data.acl || [])
      setNewGrantee("")
      setRights(profileToRights("reviewer")!)
      setRecursive(false)
      toast.success(t("share.grantAdded"))
    } catch {
      toast.error(t("share.grantFailed"))
    } finally {
      setSaving(false)
    }
  }

  const handleRemoveGrant = async (grantee: string) => {
    setSaving(true)
    try {
      await api.deleteACL(owner, folderName, grantee)
      setGrants((prev) => prev.filter((g) => g.Grantee !== grantee))
      toast.success(t("share.grantRemoved"))
    } catch {
      toast.error(t("share.removeFailed"))
    } finally {
      setSaving(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>{t("share.dialogTitle")}</DialogTitle>
          <DialogDescription>
            {t("share.dialogDescription", { folder: folderLabel })}
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4 py-2">
          {loading ? (
            <div className="flex justify-center py-4">
              <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
            </div>
          ) : grants.length > 0 ? (
            <div className="space-y-2">
              <p className="text-sm font-medium">{t("share.currentGrants")}</p>
              {grants.map((grant) => (
                <div
                  key={grant.Grantee}
                  className="flex items-center justify-between rounded-md border px-3 py-2"
                >
                  <div className="flex items-center gap-2">
                    <span className="text-sm font-medium">{grant.Grantee}</span>
                    <Badge variant="secondary" className="text-xs">
                      {t(`share.profile.${rightsToProfile(grant.Rights)}`)}
                    </Badge>
                  </div>
                  {isOwner && (
                    <Button
                      variant="ghost"
                      size="sm"
                      className="text-destructive hover:text-destructive"
                      onClick={() => handleRemoveGrant(grant.Grantee)}
                      disabled={saving}
                    >
                      <Trash2 className="h-4 w-4" />
                    </Button>
                  )}
                </div>
              ))}
              <Separator />
            </div>
          ) : (
            !loading && (
              <p className="text-sm text-muted-foreground">{t("share.noGrants")}</p>
            )
          )}

          {isOwner && (
            <div className="space-y-3">
              <p className="text-sm font-medium">{t("share.addGrant")}</p>
              <Input
                type="email"
                placeholder={t("share.granteePlaceholder")}
                value={newGrantee}
                onChange={(e) => setNewGrantee(e.target.value)}
              />

              <div className="space-y-1.5">
                <Label className="text-xs text-muted-foreground">
                  {t("share.profileLabel")}
                </Label>
                <Select value={profile} onValueChange={handleProfileChange}>
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {FOLDER_PROFILES.map((p) => (
                      <SelectItem key={p.value} value={p.value}>
                        {t(`share.profile.${p.value}`)}
                      </SelectItem>
                    ))}
                    {profile === "custom" && (
                      <SelectItem value="custom">
                        {t("share.profile.custom")}
                      </SelectItem>
                    )}
                  </SelectContent>
                </Select>
              </div>

              <div className="space-y-1.5">
                <Label className="text-xs text-muted-foreground">
                  {t("share.customRights")}
                </Label>
                <div className="grid grid-cols-2 gap-2 rounded-md border p-3">
                  {FOLDER_RIGHTS.map((r) => (
                    <label
                      key={r.key}
                      className="flex items-center gap-2 text-sm"
                    >
                      <Checkbox
                        checked={(rights & r.bit) !== 0}
                        onCheckedChange={(c) => toggleRight(r.bit, c === true)}
                      />
                      {t(`share.right.${r.key}`)}
                    </label>
                  ))}
                </div>
              </div>

              <div className="flex items-center justify-between">
                <Label htmlFor="acl-recursive" className="text-sm">
                  {t("share.applyRecursive")}
                </Label>
                <Switch
                  id="acl-recursive"
                  checked={recursive}
                  onCheckedChange={setRecursive}
                />
              </div>

              <Button
                onClick={handleAddGrant}
                disabled={saving || !newGrantee.trim()}
                className="w-full"
              >
                {saving ? (
                  <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                ) : (
                  <UserPlus className="mr-2 h-4 w-4" />
                )}
                {t("share.addGrant")}
              </Button>
            </div>
          )}
        </div>

        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {t("share.close")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
