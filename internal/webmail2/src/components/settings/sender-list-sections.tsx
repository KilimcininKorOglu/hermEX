import { useEffect, useState } from "react"
import { Mail, Plus, ShieldCheck, Tag, X } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { useI18n } from "@/hooks/useI18n"
import api, { type Category, type RecipientRule } from "@/utils/api"
import { SettingSection } from "./setting-layout"

// useLoadedList loads a list once. The loader is a module-level function, so the
// effect runs once.
function useLoadedList<T>(load: () => Promise<T[]>) {
  const [items, setItems] = useState<T[]>([])
  useEffect(() => {
    let cancelled = false
    load()
      .then((list) => { if (!cancelled) setItems(list) })
      .catch(() => {})
    return () => { cancelled = true }
  }, [load])
  return [items, setItems] as const
}

// useOptimisticList shows a changed list at once and stores the whole list. A
// failed store puts the previous list back and reports the failure.
function useOptimisticList<T>(load: () => Promise<T[]>, store: (next: T[]) => Promise<T[] | undefined>, failKey: string) {
  const { t } = useI18n()
  const [items, setItems] = useLoadedList(load)
  const [busy, setBusy] = useState(false)

  const save = async (next: T[]) => {
    const prev = items
    setItems(next)
    setBusy(true)
    try {
      setItems((await store(next)) ?? next)
    } catch {
      setItems(prev)
      toast.error(t(failKey))
    } finally {
      setBusy(false)
    }
  }

  return { items, busy, save }
}

// AddRow is an input with an Add button; Enter adds too.
function AddRow({ value, onChange, onAdd, busy, placeholder, children }: {
  value: string
  onChange: (value: string) => void
  onAdd: () => void
  busy: boolean
  placeholder: string
  children?: React.ReactNode
}) {
  const { t } = useI18n()
  return (
    <div className="flex items-center gap-2">
      {children}
      <Input
        value={value}
        onChange={(e) => onChange(e.target.value)}
        onKeyDown={(e) => { if (e.key === "Enter") onAdd() }}
        placeholder={placeholder}
      />
      <Button onClick={onAdd} disabled={busy || !value.trim()}>
        <Plus className="mr-1 h-4 w-4" />
        {t("common.add")}
      </Button>
    </div>
  )
}

const loadCategories = async () => (await api.getCategories()).categories ?? []
const storeCategories = async (next: Category[]) => (await api.setCategories(next)).categories

// CategoriesSection manages the named, colored labels the user applies to messages.
export function CategoriesSection() {
  const { t } = useI18n()
  const list = useOptimisticList(loadCategories, storeCategories, "settings.categories.saveFailed")
  const [name, setName] = useState("")
  const [color, setColor] = useState("#ef4444")

  const add = () => {
    const trimmed = name.trim()
    if (!trimmed) return
    if (list.items.some((c) => c.name.toLowerCase() === trimmed.toLowerCase())) {
      toast.error(t("settings.categories.exists"))
      return
    }
    setName("")
    void list.save([...list.items, { name: trimmed, color }])
  }

  return (
    <SettingSection icon={Tag} title={t("settings.categories.title")} description={t("settings.categories.description")}>
      <div className="space-y-3">
        <div className="flex flex-wrap items-center gap-2">
          {list.items.length === 0 && <p className="text-sm text-muted-foreground">{t("settings.categories.empty")}</p>}
          {list.items.map((c) => (
            <span
              key={c.name}
              className="inline-flex items-center gap-1 rounded-full px-2.5 py-0.5 text-xs font-medium text-white"
              style={{ backgroundColor: c.color || "#64748b" }}
            >
              {c.name}
              <button
                onClick={() => void list.save(list.items.filter((x) => x.name !== c.name))}
                aria-label={t("settings.categories.removeAria", { name: c.name })}
                className="opacity-80 hover:opacity-100"
              >
                <X className="h-3 w-3" />
              </button>
            </span>
          ))}
        </div>
        <AddRow value={name} onChange={setName} onAdd={add} busy={list.busy} placeholder={t("settings.categories.namePlaceholder")}>
          <input
            type="color"
            value={color}
            onChange={(e) => setColor(e.target.value)}
            className="h-9 w-12 cursor-pointer rounded border bg-transparent"
            aria-label={t("settings.categories.colorAria")}
          />
        </AddRow>
      </div>
    </SettingSection>
  )
}

const loadSafeSenders = async () => (await api.getSafeSenders()).safeSenders ?? []
const storeSafeSenders = async (next: string[]) => (await api.setSafeSenders(next)).safeSenders

// SafeSendersSection manages the addresses and domains whose messages load
// remote images automatically.
export function SafeSendersSection() {
  const { t } = useI18n()
  const list = useOptimisticList(loadSafeSenders, storeSafeSenders, "settings.safeSenders.saveFailed")
  const [input, setInput] = useState("")

  const add = () => {
    const addr = input.trim().toLowerCase()
    if (!addr) return
    setInput("")
    if (!list.items.includes(addr)) void list.save([...list.items, addr])
  }

  return (
    <SettingSection icon={ShieldCheck} title={t("settings.safeSenders.title")} description={t("settings.safeSenders.description")}>
      <div className="space-y-3">
        <div className="flex flex-wrap items-center gap-2">
          {list.items.length === 0 && <p className="text-sm text-muted-foreground">{t("settings.safeSenders.empty")}</p>}
          {list.items.map((addr) => (
            <span key={addr} className="inline-flex items-center gap-1 rounded-full border bg-muted px-2.5 py-0.5 text-xs font-medium">
              {addr}
              <button
                onClick={() => void list.save(list.items.filter((s) => s !== addr))}
                aria-label={t("settings.safeSenders.removeAria", { sender: addr })}
                className="opacity-60 hover:opacity-100"
              >
                <X className="h-3 w-3" />
              </button>
            </span>
          ))}
        </div>
        <AddRow value={input} onChange={setInput} onAdd={add} busy={list.busy} placeholder={t("settings.safeSenders.placeholder")} />
      </div>
    </SettingSection>
  )
}

const loadRecipientRules = async () => (await api.getRecipientRules()).rules ?? []

function RuleBadge({ action }: { action: string }) {
  const { t } = useI18n()
  const block = action === "block"
  const color = block
    ? "bg-red-100 text-red-700 dark:bg-red-950 dark:text-red-300"
    : "bg-green-100 text-green-700 dark:bg-green-950 dark:text-green-300"
  return (
    <span className={`rounded-full px-2 py-0.5 text-xs font-medium ${color}`}>
      {block ? t("settings.recipientRules.block") : t("settings.recipientRules.allow")}
    </span>
  )
}

// useRecipientRules manages the personal allow and block rules the MTA applies
// at delivery.
function useRecipientRules() {
  const { t } = useI18n()
  const [rules, setRules] = useLoadedList<RecipientRule>(loadRecipientRules)
  const [busy, setBusy] = useState(false)

  const add = async (pattern: string, action: "allow" | "block"): Promise<boolean> => {
    setBusy(true)
    try {
      await api.setRecipientRule(pattern, action)
      setRules(await loadRecipientRules())
      return true
    } catch {
      toast.error(t("settings.recipientRules.saveFailed"))
      return false
    } finally {
      setBusy(false)
    }
  }

  const remove = async (pattern: string) => {
    const prev = rules
    setRules(rules.filter((r) => r.pattern !== pattern))
    try {
      await api.deleteRecipientRule(pattern)
    } catch {
      setRules(prev)
      toast.error(t("settings.recipientRules.saveFailed"))
    }
  }

  return { rules, busy, add, remove }
}

export function RecipientRulesSection() {
  const { t } = useI18n()
  const rules = useRecipientRules()
  const [input, setInput] = useState("")
  const [action, setAction] = useState<"allow" | "block">("allow")

  const add = async () => {
    const pattern = input.trim().toLowerCase()
    if (!pattern) return
    if (await rules.add(pattern, action)) setInput("")
  }

  return (
    <SettingSection icon={Mail} title={t("settings.recipientRules.title")} description={t("settings.recipientRules.description")}>
      <div className="space-y-3">
        {rules.rules.length === 0 && <p className="text-sm text-muted-foreground">{t("settings.recipientRules.empty")}</p>}
        {rules.rules.length > 0 && (
          <div className="space-y-2">
            {rules.rules.map((r) => (
              <div key={r.pattern} className="flex items-center justify-between rounded-lg border p-2 pl-3">
                <div className="flex min-w-0 items-center gap-2">
                  <RuleBadge action={r.action} />
                  <span className="truncate text-sm">{r.pattern}</span>
                </div>
                <button
                  onClick={() => void rules.remove(r.pattern)}
                  aria-label={t("settings.recipientRules.removeAria", { pattern: r.pattern })}
                  className="opacity-60 hover:opacity-100"
                >
                  <X className="h-4 w-4" />
                </button>
              </div>
            ))}
          </div>
        )}
        <AddRow value={input} onChange={setInput} onAdd={() => void add()} busy={rules.busy} placeholder={t("settings.recipientRules.placeholder")}>
          <select
            value={action}
            onChange={(e) => setAction(e.target.value as "allow" | "block")}
            className="h-9 rounded-md border bg-background px-2 text-sm"
            aria-label={t("settings.recipientRules.actionAria")}
          >
            <option value="allow">{t("settings.recipientRules.allow")}</option>
            <option value="block">{t("settings.recipientRules.block")}</option>
          </select>
        </AddRow>
      </div>
    </SettingSection>
  )
}
