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
import api, { ACLEntry, FOLDER_PERMISSION_LEVELS, rightsToLevel, levelToRights, FolderPermissionLevel } from "@/utils/api"

interface ShareFolderDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** Canonical folder name, e.g. "INBOX", "Work", "calendar-uuid" */
  folderName: string
  /** Display label shown in the dialog title */
  folderLabel: string
  /** Owner's email — for a personal folder, the logged-in user; for a shared folder, the mailbox owner */
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

  // New grant form
  const [newGrantee, setNewGrantee] = useState("")
  const [newLevel, setNewLevel] = useState<FolderPermissionLevel>("reviewer")

  // Fetch ACL when dialog opens
  useEffect(() => {
    if (!open || !owner || !folderName) return
    setLoading(true)
    api.getACL(owner, folderName)
      .then((data) => setGrants(data.acl || []))
      .catch(() => toast.error(t("share.fetchFailed")))
      .finally(() => setLoading(false))
  }, [open, owner, folderName, t])

  const handleAddGrant = async () => {
    if (!newGrantee.trim()) return
    setSaving(true)
    try {
      const rights = levelToRights(newLevel)
      await api.setACL(owner, folderName, newGrantee.trim().toLowerCase(), rights)
      const data = await api.getACL(owner, folderName)
      setGrants(data.acl || [])
      setNewGrantee("")
      setNewLevel("reviewer")
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

  const grantLevel = (rights: string) => {
    // Server returns a human-readable string; try to map it.
    // ACLRights.String() returns e.g. "lrs" — we approximate the level.
    const num = rightsToNumber(rights)
    return rightsToLevel(num)
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
          {/* Existing grants */}
          {loading ? (
            <div className="flex justify-center py-4">
              <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
            </div>
          ) : grants.length > 0 ? (
            <div className="space-y-2">
              <p className="text-sm font-medium">{t("share.currentGrants")}</p>
              {grants.map((grant) => {
                const level = grantLevel(grant.Rights)
                return (
                  <div
                    key={grant.Grantee}
                    className="flex items-center justify-between rounded-md border px-3 py-2"
                  >
                    <div className="flex items-center gap-2">
                      <span className="text-sm font-medium">{grant.Grantee}</span>
                      <Badge variant="secondary" className="text-xs">
                        {t(`share.level.${level}`)}
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
                )
              })}
              <Separator />
            </div>
          ) : (
            !loading && (
              <p className="text-sm text-muted-foreground">{t("share.noGrants")}</p>
            )
          )}

          {/* Add new grant */}
          {isOwner && (
            <div className="space-y-2">
              <p className="text-sm font-medium">{t("share.addGrant")}</p>
              <div className="flex gap-2">
                <Input
                  type="email"
                  placeholder={t("share.granteePlaceholder")}
                  value={newGrantee}
                  onChange={(e) => setNewGrantee(e.target.value)}
                  className="flex-1"
                />
                <Select
                  value={newLevel}
                  onValueChange={(v) => setNewLevel(v as FolderPermissionLevel)}
                >
                  <SelectTrigger className="w-44">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {FOLDER_PERMISSION_LEVELS.map((lvl) => (
                      <SelectItem key={lvl.value} value={lvl.value}>
                        {t(`share.level.${lvl.value}` as any)}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                <Button onClick={handleAddGrant} disabled={saving || !newGrantee.trim()}>
                  {saving ? (
                    <Loader2 className="h-4 w-4 animate-spin" />
                  ) : (
                    <UserPlus className="h-4 w-4" />
                  )}
                </Button>
              </div>
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

// Convert human-readable rights string to approximate number for level detection
function rightsToNumber(rights: string): number {
  if (!rights) return 0
  let n = 0
  for (const c of rights.toLowerCase()) {
    switch (c) {
      case "l": n |= 1; break   // ACLLookup
      case "r": n |= 2; break   // ACLRead
      case "s": n |= 4; break   // ACLSeen
      case "w": n |= 8; break   // ACLWrite
      case "i": n |= 16; break  // ACLWriteSeen (ir)
      case "d": n |= 32; break  // ACLDelete
      case "e": n |= 64; break  // ACLExpunge
      case "c": n |= 128; break // ACLCreate
      case "t": n |= 7; break   // ACLAll ("t" from "tecda" — approximated)
      case "a": n |= 239; break // ACLAll
    }
  }
  return n
}
