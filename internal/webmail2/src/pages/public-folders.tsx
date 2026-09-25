import { useState, useEffect } from "react"
import { FolderOpen, ChevronRight, ChevronLeft, Mail as MailIcon } from "lucide-react"
import { Skeleton } from "@/components/ui/skeleton"
import { escapeTextBody, sanitizeEmailBody } from "@/utils/sanitize"
import api, { PublicFolder, Mail } from "@/utils/api"

const skeletons = (
  <div className="p-4 space-y-2">
    {[1, 2, 3].map((i) => (
      <Skeleton key={i} className="h-14 w-full rounded-md" />
    ))}
  </div>
)

// ListBody shows skeletons while loading, an empty notice for no rows, or the rows.
function ListBody({
  loading,
  empty,
  icon: Icon,
  emptyText,
  children,
}: {
  loading: boolean
  empty: boolean
  icon: React.ElementType
  emptyText: string
  children: React.ReactNode
}) {
  if (loading) return skeletons
  if (empty) {
    return (
      <div className="flex flex-col items-center justify-center h-64 text-muted-foreground">
        <Icon className="h-12 w-12 mb-3 opacity-30" />
        <p className="text-sm">{emptyText}</p>
      </div>
    )
  }
  return <div className="p-2">{children}</div>
}

// BackHeader is a view title with a back button.
function BackHeader({ onBack, children }: { onBack: () => void; children: React.ReactNode }) {
  return (
    <div className="flex items-center gap-2 border-b px-4 py-3">
      <button onClick={onBack} className="p-1 rounded hover:bg-accent" aria-label="Back">
        <ChevronLeft className="h-5 w-5" />
      </button>
      {children}
    </div>
  )
}

// PublicMessageView shows one public-folder message, read-only.
function PublicMessageView({ message, onBack }: { message: Mail; onBack: () => void }) {
  const body = message.bodyType === "text" ? escapeTextBody(message.body) : message.body
  const { html } = sanitizeEmailBody(body, true)
  return (
    <div className="flex flex-col h-full">
      <BackHeader onBack={onBack}>
        <h1 className="font-semibold truncate">{message.subject || "(no subject)"}</h1>
      </BackHeader>
      <div className="flex-1 overflow-y-auto p-4">
        <div className="text-sm text-muted-foreground mb-4">
          <span className="font-medium text-foreground">{message.fromName || message.from}</span>
          {message.fromName && <span> &lt;{message.from}&gt;</span>}
          {message.date && <span> - {new Date(message.date).toLocaleString()}</span>}
        </div>
        <div className="prose prose-sm max-w-none dark:prose-invert" dangerouslySetInnerHTML={{ __html: html }} />
      </div>
    </div>
  )
}

// MessageRow is one message in a public folder's list.
function MessageRow({ m, onOpen }: { m: Mail; onOpen: (m: Mail) => void }) {
  return (
    <button
      onClick={() => onOpen(m)}
      className="w-full flex items-center gap-3 px-3 py-3 rounded-md hover:bg-accent transition-colors text-left"
    >
      <MailIcon className="h-5 w-5 text-muted-foreground shrink-0" />
      <div className="flex-1 min-w-0">
        <p className="font-medium truncate">{m.subject || "(no subject)"}</p>
        <p className="text-xs text-muted-foreground truncate">
          {m.fromName || m.from}
          {m.date ? " - " + new Date(m.date).toLocaleDateString() : ""}
        </p>
      </div>
      <ChevronRight className="h-4 w-4 text-muted-foreground shrink-0" />
    </button>
  )
}

// FolderRow is one public folder in the folder list.
function FolderRow({ f, onOpen }: { f: PublicFolder; onOpen: (f: PublicFolder) => void }) {
  return (
    <button
      onClick={() => onOpen(f)}
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
  )
}

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

  // One message, read-only.
  if (message && folder) {
    return <PublicMessageView message={message} onBack={() => setMessage(null)} />
  }

  // One folder's messages.
  if (folder) {
    return (
      <div className="flex flex-col h-full">
        <BackHeader onBack={() => setFolder(null)}>
          <FolderOpen className="h-5 w-5 text-muted-foreground" />
          <h1 className="font-semibold truncate">{folder.name}</h1>
        </BackHeader>
        <div className="flex-1 overflow-y-auto">
          <ListBody loading={msgLoading} empty={messages.length === 0} icon={MailIcon} emptyText="No messages in this folder">
            {messages.map((m) => (
              <MessageRow key={m.id} m={m} onOpen={openMessage} />
            ))}
          </ListBody>
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
        <ListBody loading={loading} empty={folders.length === 0} icon={FolderOpen} emptyText="No public folders available">
          {folders.map((f) => (
            <FolderRow key={f.id} f={f} onOpen={openFolder} />
          ))}
        </ListBody>
      </div>
    </div>
  )
}
