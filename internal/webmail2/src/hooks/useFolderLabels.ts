import { useEffect, useState } from "react"
import api, { folderLabel, type UserFolder } from "@/utils/api"

// BUILTIN_LABEL_KEYS maps each built-in folder slug to the i18n key of its label.
const BUILTIN_LABEL_KEYS: Record<string, string> = {
  inbox: "nav.inbox",
  sent: "nav.sent",
  drafts: "nav.drafts",
  trash: "nav.trash",
  spam: "nav.spam",
  archive: "nav.archive",
}

/**
 * slugLabel names the folder a message id or folder field addresses by slug: a
 * built-in folder by its translated name, a user folder by its label (its name,
 * prefixed by its parent's when another folder shares it), anything else as the
 * slug itself.
 */
export function slugLabel(slug: string, user: UserFolder[], t: (key: string) => string): string {
  const key = BUILTIN_LABEL_KEYS[slug]
  if (key) return t(key)
  const folder = user.find((f) => f.id === slug)
  return folder ? folderLabel(folder, user) : slug
}

// useUserFolders loads the user's own folders once, for labelling folder slugs.
export function useUserFolders(): UserFolder[] {
  const [folders, setFolders] = useState<UserFolder[]>([])
  useEffect(() => {
    let cancelled = false
    api.getMailboxes()
      .then((res) => { if (!cancelled) setFolders(res.folders ?? []) })
      .catch(() => { if (!cancelled) setFolders([]) })
    return () => { cancelled = true }
  }, [])
  return folders
}
