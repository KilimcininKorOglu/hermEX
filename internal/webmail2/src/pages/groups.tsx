import { useState, useEffect, useCallback, useRef } from "react"
import { Users, Upload, Save } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { toast } from "sonner"
import { useI18n } from "@/hooks/useI18n"
import api from "@/utils/api"

/**
 * GroupsPage lets a distribution-list owner manage the membership of the lists they
 * own (the Exchange managedBy owner). Members can be edited by hand or imported from
 * a CSV; the backend gates every operation on ownership.
 */
export function GroupsPage() {
  const { t } = useI18n()
  const [groups, setGroups] = useState<{ address: string; ldapMastered?: boolean }[]>([])
  const [loading, setLoading] = useState(true)
  const [selected, setSelected] = useState<string>("")
  const [members, setMembers] = useState<string>("")
  const [saving, setSaving] = useState(false)
  const fileRef = useRef<HTMLInputElement>(null)

  useEffect(() => {
    api
      .getGroups()
      .then((g) => setGroups(g || []))
      .catch(() => toast.error(t("groups.loadError")))
      .finally(() => setLoading(false))
  }, [t])

  const openGroup = useCallback(
    (address: string) => {
      setSelected(address)
      setMembers("")
      api
        .getGroupMembers(address)
        .then((r) => setMembers((r.members || []).join("\n")))
        .catch(() => toast.error(t("groups.membersError")))
    },
    [t]
  )

  const save = useCallback(async () => {
    if (!selected) return
    const list = members
      .split("\n")
      .map((s) => s.trim())
      .filter(Boolean)
    setSaving(true)
    try {
      await api.setGroupMembers(selected, list)
      toast.success(t("groups.saved"))
    } catch {
      toast.error(t("groups.saveError"))
    } finally {
      setSaving(false)
    }
  }, [selected, members, t])

  const importCsv = useCallback(
    (e: React.ChangeEvent<HTMLInputElement>) => {
      const file = e.target.files?.[0]
      if (!file) return
      const reader = new FileReader()
      reader.onload = () => {
        const text = String(reader.result || "")
        // Accept addresses separated by comma, semicolon, or whitespace (a one-column
        // CSV or a delimited list); merge with the current set, de-duplicated.
        const incoming = text
          .split(/[\s,;]+/)
          .map((s) => s.trim())
          .filter((s) => s.includes("@"))
        const existing = members
          .split("\n")
          .map((s) => s.trim())
          .filter(Boolean)
        const merged = Array.from(new Set([...existing, ...incoming]))
        setMembers(merged.join("\n"))
        toast.success(t("groups.imported", { count: String(incoming.length) }))
      }
      reader.readAsText(file)
      e.target.value = ""
    },
    [members, t]
  )

  return (
    <div className="p-6 max-w-3xl mx-auto">
      <h1 className="text-2xl font-semibold flex items-center gap-2 mb-4">
        <Users className="h-6 w-6" /> {t("groups.title")}
      </h1>
      <p className="text-sm text-muted-foreground mb-6">{t("groups.description")}</p>

      {loading ? (
        <Skeleton className="h-24 w-full" />
      ) : groups.length === 0 ? (
        <p className="text-sm text-muted-foreground">{t("groups.empty")}</p>
      ) : (
        <div className="grid gap-6 md:grid-cols-[200px_1fr]">
          <ul className="space-y-1">
            {groups.map((g) => (
              <li key={g.address}>
                <button
                  className={`w-full text-left px-3 py-2 rounded text-sm ${
                    selected === g.address ? "bg-primary text-primary-foreground" : "hover:bg-muted"
                  }`}
                  onClick={() => openGroup(g.address)}
                >
                  {g.address}
                </button>
              </li>
            ))}
          </ul>

          {selected &&
            (() => {
              // An AD-synced (LDAP-mastered) group is read-only here: the directory
              // sync owns its membership and would overwrite a local edit.
              const mastered = groups.find((g) => g.address === selected)?.ldapMastered ?? false
              return (
                <div className="space-y-3">
                  <div className="flex items-center justify-between">
                    <span className="font-medium text-sm">{selected}</span>
                    {!mastered && (
                      <div className="flex gap-2">
                        <input
                          ref={fileRef}
                          type="file"
                          accept=".csv,text/csv,text/plain"
                          className="hidden"
                          onChange={importCsv}
                        />
                        <Button variant="outline" size="sm" onClick={() => fileRef.current?.click()}>
                          <Upload className="h-4 w-4 mr-1" /> {t("groups.importCsv")}
                        </Button>
                        <Button size="sm" onClick={save} disabled={saving}>
                          <Save className="h-4 w-4 mr-1" /> {t("groups.save")}
                        </Button>
                      </div>
                    )}
                  </div>
                  {mastered && (
                    <p className="text-xs text-muted-foreground">{t("groups.managedBySync")}</p>
                  )}
                  <textarea
                    className="w-full h-72 rounded border bg-background p-3 font-mono text-sm disabled:opacity-60"
                    value={members}
                    onChange={(e) => setMembers(e.target.value)}
                    placeholder={t("groups.placeholder")}
                    readOnly={mastered}
                  />
                </div>
              )
            })()}
        </div>
      )}
    </div>
  )
}
