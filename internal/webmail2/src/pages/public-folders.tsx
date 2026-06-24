import { useState, useEffect } from "react"
import { FolderOpen, ChevronRight, ChevronLeft, Mail as MailIcon } from "lucide-react"
import { Skeleton } from "@/components/ui/skeleton"
import { sanitizeEmailBody } from "@/utils/sanitize"
import api, { PublicFolder, Mail } from "@/utils/api"

/**
 * PublicFoldersPage is the read-only organization public-folders browser: a list
 * of the folders the caller may see, then one folder's messages, then one
 * message. Access is gated server-side per the publicfolder service.
 */
export function PublicFoldersPage() {
  const [folders, setFolders] = useState<PublicFolder[]>([])
  const [owner, setOwner] = useState("")
  const [loading, setLoading] = useState(true)
  const [folder, setFolder] = useState<PublicFolder | null>(null)
  const [messages, setMessages] = useState<Mail[]>([])
  const [msgLoading, setMsgLoading] = useState(false)
  const [message, setMessage] = useState<Mail | null>(null)

  useEffect(() => {
    let cancelled = false
    api.getPublicFolders()
      .then((res) => {
        if (!cancelled) {
          setFolders(res.folders ?? [])
          setOwner(res.owner ?? "")
        }
      })
      .catch(() => {})
      .finally(() => { if (!cancelled) setLoading(false) })
    return () => { cancelled = true }
  }, [])

  const openFolder = (f: PublicFolder) => {
    setFolder(f)
    setMessage(null)
    setMessages([])
    setMsgLoading(true)
    api.getPublicFolderMessages(f.id)
      .then((res) => setMessages(res.emails ?? []))
      .catch(() => {})
      .finally(() => setMsgLoading(false))
  }

  const openMessage = (m: Mail) => {
    if (!folder) return
    api.getPublicMessage(folder.id, m.id).then(setMessage).catch(() => {})
  }

  const skeletons = (
    <div className="p-4 space-y-2">
      {[1, 2, 3].map((i) => (
        <Skeleton key={i} className="h-14 w-full rounded-md" />
      ))}
    </div>
  )

  // One message, read-only.
  if (message && folder) {
    const { html } = sanitizeEmailBody(message.body, true)
    return (
      <div className="flex flex-col h-full">
        <div className="flex items-center gap-2 border-b px-4 py-3">
          <button onClick={() => setMessage(null)} className="p-1 rounded hover:bg-accent" aria-label="Back">
            <ChevronLeft className="h-5 w-5" />
          </button>
          <h1 className="font-semibold truncate">{message.subject || "(no subject)"}</h1>
        </div>
        <div className="flex-1 overflow-y-auto p-4">
          <div className="text-sm text-muted-foreground mb-4">
            <span className="font-medium text-foreground">{message.fromName || message.from}</span>
            {message.fromName && <span> &lt;{message.from}&gt;</span>}
            {message.date && <span> — {new Date(message.date).toLocaleString()}</span>}
          </div>
          <div className="prose prose-sm max-w-none dark:prose-invert" dangerouslySetInnerHTML={{ __html: html }} />
        </div>
      </div>
    )
  }

  // One folder's messages.
  if (folder) {
    return (
      <div className="flex flex-col h-full">
        <div className="flex items-center gap-2 border-b px-4 py-3">
          <button onClick={() => setFolder(null)} className="p-1 rounded hover:bg-accent" aria-label="Back">
            <ChevronLeft className="h-5 w-5" />
          </button>
          <FolderOpen className="h-5 w-5 text-muted-foreground" />
          <h1 className="font-semibold truncate">{folder.name}</h1>
        </div>
        <div className="flex-1 overflow-y-auto">
          {msgLoading ? skeletons : messages.length === 0 ? (
            <div className="flex flex-col items-center justify-center h-64 text-muted-foreground">
              <MailIcon className="h-12 w-12 mb-3 opacity-30" />
              <p className="text-sm">No messages in this folder</p>
            </div>
          ) : (
            <div className="p-2">
              {messages.map((m) => (
                <button
                  key={m.id}
                  onClick={() => openMessage(m)}
                  className="w-full flex items-center gap-3 px-3 py-3 rounded-md hover:bg-accent transition-colors text-left"
                >
                  <MailIcon className="h-5 w-5 text-muted-foreground shrink-0" />
                  <div className="flex-1 min-w-0">
                    <p className="font-medium truncate">{m.subject || "(no subject)"}</p>
                    <p className="text-xs text-muted-foreground truncate">
                      {m.fromName || m.from}
                      {m.date ? " — " + new Date(m.date).toLocaleDateString() : ""}
                    </p>
                  </div>
                  <ChevronRight className="h-4 w-4 text-muted-foreground shrink-0" />
                </button>
              ))}
            </div>
          )}
        </div>
      </div>
    )
  }

  // The folder list.
  return (
    <div className="flex flex-col h-full">
      <div className="flex items-center gap-3 border-b px-4 py-3">
        <FolderOpen className="h-5 w-5 text-muted-foreground" />
        <h1 className="font-semibold">Public Folders</h1>
        {owner && <span className="text-xs text-muted-foreground">{owner}</span>}
      </div>
      <div className="flex-1 overflow-y-auto">
        {loading ? skeletons : folders.length === 0 ? (
          <div className="flex flex-col items-center justify-center h-64 text-muted-foreground">
            <FolderOpen className="h-12 w-12 mb-3 opacity-30" />
            <p className="text-sm">No public folders available</p>
          </div>
        ) : (
          <div className="p-2">
            {folders.map((f) => (
              <button
                key={f.id}
                onClick={() => openFolder(f)}
                className="w-full flex items-center gap-3 px-3 py-3 rounded-md hover:bg-accent transition-colors text-left"
              >
                <FolderOpen className="h-5 w-5 text-muted-foreground shrink-0" />
                <div className="flex-1 min-w-0">
                  <p className="font-medium truncate">{f.name}</p>
                  <p className="text-xs text-muted-foreground truncate">
                    {f.total} message{f.total === 1 ? "" : "s"}
                    {f.unread > 0 ? ` · ${f.unread} unread` : ""}
                  </p>
                </div>
                <ChevronRight className="h-4 w-4 text-muted-foreground shrink-0" />
              </button>
            ))}
          </div>
        )}
      </div>
    </div>
  )
}
