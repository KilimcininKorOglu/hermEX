import { useState, useEffect, useRef } from "react"
import { X } from "lucide-react"
import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar"
import { useI18n } from "@/hooks/useI18n"
import api, { type DirectoryEntry } from "@/utils/api"

type FBStatus = "free" | "busy" | "unknown"

interface AttendeePickerProps {
  // Selected attendee email addresses.
  value: string[]
  onChange: (emails: string[]) => void
  // Optional RFC3339 window; when set, each result shows a free/busy dot for it.
  window?: { start: string; end: string }
  placeholder?: string
}

function initialsOf(s: string): string {
  return (s ? s.slice(0, 2) : "?").toUpperCase()
}

// AttendeePicker resolves people from the organization directory as the user
// types, shows their photo with a small free/busy dot, and collects the picks
// as removable chips. A free-typed address can still be added with Enter so
// external attendees and rooms keep working.
export function AttendeePicker({ value, onChange, window: win, placeholder }: AttendeePickerProps) {
  const { t } = useI18n()
  const [query, setQuery] = useState("")
  const [results, setResults] = useState<DirectoryEntry[]>([])
  const [open, setOpen] = useState(false)
  const [fb, setFb] = useState<Record<string, FBStatus>>({})
  const boxRef = useRef<HTMLDivElement>(null)

  // Debounced directory search; exclude already-picked addresses.
  useEffect(() => {
    const q = query.trim()
    if (q.length < 1) {
      setResults([])
      return
    }
    let active = true
    const timer = setTimeout(async () => {
      try {
        const res = await api.searchDirectory(q)
        if (active) setResults((res.entries ?? []).filter((e) => !value.includes(e.email)))
      } catch {
        if (active) setResults([])
      }
    }, 250)
    return () => {
      active = false
      clearTimeout(timer)
    }
  }, [query, value])

  // Free/busy lookup for the visible results over the requested window.
  useEffect(() => {
    if (!win || !win.start || !win.end || results.length === 0) return
    let active = true
    const emails = results.map((r) => r.email)
    api
      .getFreeBusy(emails, win.start, win.end)
      .then((res) => {
        if (!active) return
        const map: Record<string, FBStatus> = {}
        for (const r of res.freeBusy ?? []) {
          map[r.user] = r.busy && r.busy.length > 0 ? "busy" : "free"
        }
        setFb((prev) => ({ ...prev, ...map }))
      })
      .catch(() => undefined)
    return () => {
      active = false
    }
  }, [results, win])

  // Close the dropdown when clicking outside the picker.
  useEffect(() => {
    const onDoc = (e: MouseEvent) => {
      if (boxRef.current && !boxRef.current.contains(e.target as Node)) setOpen(false)
    }
    document.addEventListener("mousedown", onDoc)
    return () => document.removeEventListener("mousedown", onDoc)
  }, [])

  const add = (email: string) => {
    const e = email.trim()
    if (!e || value.includes(e)) return
    onChange([...value, e])
    setQuery("")
    setResults([])
    setOpen(false)
  }
  const remove = (email: string) => onChange(value.filter((v) => v !== email))

  const onKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "Enter") {
      e.preventDefault()
      if (query.trim()) add(query)
    } else if (e.key === "Backspace" && !query && value.length > 0) {
      remove(value[value.length - 1])
    }
  }

  return (
    <div ref={boxRef} className="relative">
      <div className="flex flex-wrap gap-1.5 rounded-md border bg-background p-1.5">
        {value.map((email) => (
          <span key={email} className="flex items-center gap-1.5 rounded-full bg-muted py-0.5 pl-0.5 pr-2 text-sm">
            <Avatar className="h-5 w-5">
              <AvatarFallback className="text-[10px]">{initialsOf(email)}</AvatarFallback>
            </Avatar>
            <span className="max-w-[180px] truncate">{email}</span>
            <button type="button" onClick={() => remove(email)} className="text-muted-foreground hover:text-foreground">
              <X className="h-3 w-3" />
            </button>
          </span>
        ))}
        <input
          className="min-w-[140px] flex-1 bg-transparent px-1 text-sm outline-none"
          value={query}
          placeholder={value.length === 0 ? placeholder ?? t("attendee.searchPlaceholder") : ""}
          onChange={(e) => {
            setQuery(e.target.value)
            setOpen(true)
          }}
          onFocus={() => setOpen(true)}
          onKeyDown={onKeyDown}
        />
      </div>
      {open && results.length > 0 && (
        <div className="absolute z-50 mt-1 max-h-64 w-full overflow-auto rounded-md border bg-popover p-1 shadow-md">
          {results.map((r) => {
            const status = fb[r.email] ?? "unknown"
            return (
              <button
                type="button"
                key={r.email}
                onClick={() => add(r.email)}
                className="flex w-full items-center gap-2 rounded px-2 py-1.5 text-left hover:bg-accent"
              >
                <div className="relative">
                  <Avatar className="h-7 w-7">
                    <AvatarImage src={r.photo ? api.avatarUrl(r.email) : ""} alt={r.email} />
                    <AvatarFallback className="text-[10px]">{initialsOf(r.name || r.email)}</AvatarFallback>
                  </Avatar>
                  {win && win.start && (
                    <span
                      className={`absolute -bottom-0.5 -right-0.5 h-2.5 w-2.5 rounded-full ring-2 ring-popover ${
                        status === "busy" ? "bg-red-500" : status === "free" ? "bg-green-500" : "bg-muted-foreground/40"
                      }`}
                      title={status === "busy" ? t("attendee.busy") : status === "free" ? t("attendee.free") : t("attendee.unknown")}
                    />
                  )}
                </div>
                <div className="min-w-0">
                  <p className="truncate text-sm font-medium">{r.name || r.email}</p>
                  <p className="truncate text-xs text-muted-foreground">{r.email}</p>
                </div>
              </button>
            )
          })}
        </div>
      )}
    </div>
  )
}
