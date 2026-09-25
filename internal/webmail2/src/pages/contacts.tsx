import { useState, useEffect } from "react"
import {
  Plus,
  Search,
  Mail,
  Phone,
  Edit,
  Trash2,
  ChevronLeft,
  ChevronRight,
  ChevronDown,
  MoreHorizontal,
  User,
  Download,
  Users,
  LayoutGrid,
  List as ListIcon,
  MapPin,
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Avatar, AvatarFallback } from "@/components/ui/avatar"
import { Badge } from "@/components/ui/badge"
import { Separator } from "@/components/ui/separator"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { toast } from "sonner"
import api, { Contact as ApiContact, DirectoryEntry } from "@/utils/api"
import { useI18n } from "@/hooks/useI18n"
import { Switch } from "@/components/ui/switch"
import { Textarea } from "@/components/ui/textarea"
import { Label } from "@/components/ui/label"
import { CategoryChips, toggledCategories, type CategoryOption } from "@/components/category-chips"
import {
  contactFormOf,
  contactSaveError,
  emptyContactForm,
  groupMembers,
  initials,
  mapQuery,
  type ContactForm,
  type ContactTextKey,
} from "@/utils/contactForm"

// Local contact type for the page (extends API contact with labels)
type Contact = ApiContact & { labels: string[] }

type TFunc = (key: string, params?: Record<string, string>) => string
type SetForm = React.Dispatch<React.SetStateAction<ContactForm>>

// memberCount is a group's member count.
const memberCount = (contact: Contact): number => (contact.members || []).length

// ContactAvatar shows a group icon or the contact's initials.
function ContactAvatar({ contact, size }: { contact: Contact; size: "sm" | "lg" }) {
  const lg = size === "lg"
  return (
    <Avatar className={lg ? "h-12 w-12" : "h-10 w-10"}>
      <AvatarFallback className="bg-gradient-to-br from-primary to-primary/80 text-primary-foreground font-semibold">
        {contact.is_group ? <Users className={lg ? "h-5 w-5" : "h-4 w-4"} /> : initials(contact.name)}
      </AvatarFallback>
    </Avatar>
  )
}

// ContactCard is one contact in the grid view; a click opens the editor.
function ContactCard({ contact, t, onEdit }: { contact: Contact; t: TFunc; onEdit: (c: Contact) => void }) {
  return (
    <button
      onClick={() => onEdit(contact)}
      className="flex flex-col items-center gap-2 rounded-lg border bg-card p-4 text-center hover:bg-accent/50 transition-colors"
    >
      <ContactAvatar contact={contact} size="lg" />
      <span className="font-medium truncate w-full">{contact.name}</span>
      <span className="text-xs text-muted-foreground truncate w-full">{contact.is_group ? `${memberCount(contact)} ${t("contacts.membersCount")}` : contact.email}</span>
    </button>
  )
}

// ContactDetails is the line under a contact's name: a group's member count,
// or the contact's address, phone and company.
function ContactDetails({ contact, t }: { contact: Contact; t: TFunc }) {
  if (contact.is_group) {
    return (
      <span className="flex items-center gap-1">
        <Users className="h-3 w-3" />
        {memberCount(contact)} {t("contacts.membersCount")}
      </span>
    )
  }
  return (
    <>
      <span className="flex items-center gap-1">
        <Mail className="h-3 w-3" />
        {contact.email}
      </span>
      {contact.phone && (
        <span className="flex items-center gap-1">
          <Phone className="h-3 w-3" />
          {contact.phone}
        </span>
      )}
      {contact.company && (
        <span className="text-xs">{contact.company}</span>
      )}
    </>
  )
}

interface ContactActions {
  onEdit: (contact: Contact) => void
  onExport: (contact: Contact) => void
  onDelete: (contact: Contact) => void
}

// ContactMenu is a contact row's actions menu.
function ContactMenu({ contact, t, actions }: { contact: Contact; t: TFunc; actions: ContactActions }) {
  const hasAddress = !contact.is_group && Boolean(contact.homeStreet || contact.workStreet)
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="ghost" size="icon" className="h-8 w-8">
          <MoreHorizontal className="h-4 w-4" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        <DropdownMenuItem onClick={() => actions.onEdit(contact)}>
          <Edit className="h-4 w-4 mr-2" />
          {t("common.edit")}
        </DropdownMenuItem>
        <DropdownMenuItem onClick={() => actions.onExport(contact)}>
          <Download className="h-4 w-4 mr-2" />
          {t("contacts.exportVCard")}
        </DropdownMenuItem>
        {hasAddress && (
          <DropdownMenuItem
            onClick={() => window.open(`https://www.openstreetmap.org/search?query=${encodeURIComponent(mapQuery(contact))}`, "_blank")}
          >
            <MapPin className="h-4 w-4 mr-2" />
            {t("contacts.showOnMap")}
          </DropdownMenuItem>
        )}
        <DropdownMenuItem
          className="text-destructive"
          onClick={() => actions.onDelete(contact)}
        >
          <Trash2 className="h-4 w-4 mr-2" />
          {t("common.delete")}
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

// ContactRow is one contact in the list view. A contact group can be expanded
// to list its members (expanddistlist: a distribution list expanded to its
// member addresses).
function ContactRow({
  contact,
  index,
  expanded,
  t,
  onToggleExpand,
  actions,
}: {
  contact: Contact
  index: number
  expanded: boolean
  t: TFunc
  onToggleExpand: () => void
  actions: ContactActions
}) {
  const expandable = Boolean(contact.is_group) && memberCount(contact) > 0
  return (
    <div>
      {index > 0 && <Separator />}
      <div className="flex items-center gap-4 p-4 hover:bg-accent/50 transition-colors">
        <ContactAvatar contact={contact} size="sm" />
        {expandable && (
          <button
            type="button"
            aria-label={expanded ? t("contacts.collapse") : t("contacts.expand")}
            onClick={onToggleExpand}
            className="shrink-0 rounded p-1 text-muted-foreground hover:bg-accent"
          >
            {expanded ? <ChevronDown className="h-4 w-4" /> : <ChevronRight className="h-4 w-4" />}
          </button>
        )}
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-2">
            <span className="font-medium">{contact.name}</span>
            {contact.labels.map((label) => (
              <Badge key={label} variant="secondary" className="text-[10px]">
                {label}
              </Badge>
            ))}
          </div>
          <div className="flex items-center gap-4 text-sm text-muted-foreground">
            <ContactDetails contact={contact} t={t} />
          </div>
        </div>
        <ContactMenu contact={contact} t={t} actions={actions} />
      </div>
      {/* Expanded distribution list: the group's member addresses. */}
      {contact.is_group && expanded && (
        <ul className="border-t bg-muted/20 px-4 py-2 text-sm text-muted-foreground">
          {(contact.members || []).map((m) => (
            <li key={m} className="flex items-center gap-1 py-0.5">
              <Mail className="h-3 w-3" />
              {m}
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

// MyContacts is the personal contacts section, as a list or a card grid
// (ContactCardView).
function MyContacts({ contacts, t, actions }: { contacts: Contact[]; t: TFunc; actions: ContactActions }) {
  const [view, setView] = useState<"list" | "grid">("list")
  const [expandedGroups, setExpandedGroups] = useState<Set<string>>(new Set())
  const toggleGroup = (id: string) =>
    setExpandedGroups((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  if (contacts.length === 0) return null
  return (
    <div>
      <div className="mb-2 flex items-center justify-between px-1">
        <h2 className="text-sm font-semibold text-muted-foreground">
          {t("contacts.myContacts")} ({contacts.length})
        </h2>
        <div className="flex rounded-md border">
          <Button variant={view === "list" ? "secondary" : "ghost"} size="icon" className="h-7 w-7 rounded-r-none" onClick={() => setView("list")} aria-label={t("contacts.listView")}><ListIcon className="h-4 w-4" /></Button>
          <Button variant={view === "grid" ? "secondary" : "ghost"} size="icon" className="h-7 w-7 rounded-l-none" onClick={() => setView("grid")} aria-label={t("contacts.gridView")}><LayoutGrid className="h-4 w-4" /></Button>
        </div>
      </div>
      {view === "grid" ? (
        <div className="grid grid-cols-2 gap-3 sm:grid-cols-3">
          {contacts.map((contact) => (
            <ContactCard key={contact.id} contact={contact} t={t} onEdit={actions.onEdit} />
          ))}
        </div>
      ) : (
        <div className="rounded-lg border bg-card">
          {contacts.map((contact, index) => (
            <ContactRow
              key={contact.id}
              contact={contact}
              index={index}
              expanded={expandedGroups.has(contact.id)}
              t={t}
              onToggleExpand={() => toggleGroup(contact.id)}
              actions={actions}
            />
          ))}
        </div>
      )}
    </div>
  )
}

// GalRow is one Global Address List entry.
function GalRow({ entry, index, t }: { entry: DirectoryEntry; index: number; t: TFunc }) {
  const label = entry.name || entry.email
  return (
    <div>
      {index > 0 && <Separator />}
      <div className="flex items-center gap-4 p-4 hover:bg-accent/50 transition-colors">
        <Avatar className="h-10 w-10">
          <AvatarFallback className="bg-gradient-to-br from-muted-foreground/70 to-muted-foreground text-background font-semibold">
            {initials(label)}
          </AvatarFallback>
        </Avatar>
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-2">
            <span className="font-medium">{label}</span>
            <Badge variant="outline" className="text-[10px]">
              {t("contacts.directoryBadge")}
            </Badge>
          </div>
          <div className="flex items-center gap-4 text-sm text-muted-foreground">
            <span className="flex items-center gap-1">
              <Mail className="h-3 w-3" />
              {entry.email}
            </span>
          </div>
        </div>
      </div>
    </div>
  )
}

// GalSection is the Global Address List, shown as its own group.
function GalSection({ entries, t }: { entries: DirectoryEntry[]; t: TFunc }) {
  if (entries.length === 0) return null
  return (
    <div>
      <h2 className="mb-2 px-1 text-sm font-semibold text-muted-foreground">
        {t("contacts.globalAddressList")} ({entries.length})
      </h2>
      <div className="rounded-lg border bg-card">
        {entries.map((entry, index) => (
          <GalRow key={entry.email} entry={entry} index={index} t={t} />
        ))}
      </div>
    </div>
  )
}

// NoContacts is the empty state, worded for an empty book or a search miss.
function NoContacts({ searching, t }: { searching: boolean; t: TFunc }) {
  return (
    <div className="flex flex-col items-center justify-center py-16 text-center">
      <div className="rounded-full bg-muted p-4">
        <User className="h-8 w-8 text-muted-foreground" />
      </div>
      <h3 className="mt-4 text-lg font-semibold">{t("contacts.noContacts")}</h3>
      <p className="text-sm text-muted-foreground">
        {searching ? t("contacts.noSearchMatch") : t("contacts.emptyHint")}
      </p>
    </div>
  )
}

// PhotoEditor shows the contact photo with change and remove controls. The
// avatar loads the photo URL with a cache-busting version so an upload or delete
// re-renders without a page refresh.
function PhotoEditor({
  contactId,
  name,
  version,
  error,
  busy,
  t,
  onError,
  onUpload,
  onDelete,
}: {
  contactId: string
  name: string
  version: number
  error: boolean
  busy: boolean
  t: TFunc
  onError: () => void
  onUpload: (file: File) => void
  onDelete: () => void
}) {
  return (
    <div className="flex items-center gap-4">
      <Avatar className="h-16 w-16">
        {!error ? (
          <img
            src={api.contactPhotoUrl(contactId) + "?v=" + version}
            alt={name}
            className="h-full w-full rounded-full object-cover"
            onError={onError}
          />
        ) : null}
        <AvatarFallback>{name?.charAt(0)?.toUpperCase() || "?"}</AvatarFallback>
      </Avatar>
      <div className="flex flex-col gap-1">
        <label className="inline-flex cursor-pointer text-sm text-primary hover:underline">
          <span>{t("contacts.changePhoto")}</span>
          <input
            type="file"
            accept="image/*"
            className="hidden"
            disabled={busy}
            onChange={(e) => {
              const f = e.target.files?.[0]
              if (f) onUpload(f)
              e.target.value = ""
            }}
          />
        </label>
        {!error && (
          <button
            type="button"
            className="text-left text-sm text-destructive hover:underline disabled:opacity-50"
            disabled={busy}
            onClick={onDelete}
          >
            {t("contacts.removePhoto")}
          </button>
        )}
      </div>
    </div>
  )
}

interface FieldDef {
  key: ContactTextKey
  label: string
  type?: string
  placeholder?: string
  placeholderKey?: string
  wide?: boolean
}

// NAME_FIELDS are the structured name parts.
const NAME_FIELDS: FieldDef[] = [
  { key: "prefix", label: "contacts.prefix", placeholder: "Dr." },
  { key: "firstName", label: "contacts.firstName" },
  { key: "middleName", label: "contacts.middleName" },
  { key: "lastName", label: "contacts.lastName" },
  { key: "suffix", label: "contacts.suffix", placeholder: "Jr." },
]

// PRIMARY_FIELDS are the addresses, phone and company of a contact.
const PRIMARY_FIELDS: FieldDef[] = [
  { key: "email", label: "common.email", type: "email", placeholder: "john@example.com" },
  { key: "email2", label: "contacts.email2", type: "email" },
  { key: "email3", label: "contacts.email3", type: "email" },
  { key: "phone", label: "contacts.phoneOptional", placeholder: "+1 555 123 4567" },
  { key: "company", label: "contacts.companyOptional", placeholderKey: "contacts.companyPlaceholder" },
]

// Rich contact fields: job title, department, more phones, birthday, home
// address, IM, web page. They round-trip through vCard. WORK_FIELDS come before
// the categories, ADDRESS_FIELDS after them.
const WORK_FIELDS: FieldDef[] = [
  { key: "jobTitle", label: "contacts.jobTitle" },
  { key: "department", label: "contacts.department" },
  { key: "assistant", label: "contacts.assistant" },
  { key: "manager", label: "contacts.manager" },
  { key: "office", label: "contacts.office" },
  { key: "mobilePhone", label: "contacts.mobilePhone" },
  { key: "homePhone", label: "contacts.homePhone" },
  { key: "businessFax", label: "contacts.businessFax" },
  { key: "birthday", label: "contacts.birthday", type: "date" },
  { key: "anniversary", label: "contacts.anniversary", type: "date" },
  { key: "billing", label: "contacts.billing" },
  { key: "nickname", label: "contacts.nickname" },
  { key: "fileAs", label: "contacts.fileAs" },
  { key: "profession", label: "contacts.profession" },
  { key: "spouse", label: "contacts.spouse" },
]

// addressFields lists the five parts of one postal address (vCard ADR), the
// country spanning the row.
const addressFields = (kind: "home" | "work" | "other"): FieldDef[] =>
  (["Street", "City", "State", "Postal", "Country"] as const).map((part) => ({
    key: `${kind}${part}` as ContactTextKey,
    label: `contacts.${kind}${part}`,
    wide: part === "Country",
  }))

const ADDRESS_FIELDS: FieldDef[] = [
  { key: "imAddress", label: "contacts.imAddress" },
  { key: "webPage", label: "contacts.webPage", wide: true },
  ...addressFields("home"),
  ...addressFields("work"),
  ...addressFields("other"),
]

// FormField is one labelled text input bound to a form field.
function FormField({ field, form, setForm, t }: { field: FieldDef; form: ContactForm; setForm: SetForm; t: TFunc }) {
  const placeholder = field.placeholderKey ? t(field.placeholderKey) : field.placeholder
  return (
    <div className={field.wide ? "col-span-2" : undefined}>
      <label className="text-sm font-medium">{t(field.label)}</label>
      <Input
        className="mt-1"
        type={field.type}
        placeholder={placeholder}
        value={form[field.key]}
        onChange={(e) => setForm({ ...form, [field.key]: e.target.value })}
      />
    </div>
  )
}

// FormFields renders a list of field definitions.
function FormFields({ fields, form, setForm, t }: { fields: FieldDef[]; form: ContactForm; setForm: SetForm; t: TFunc }) {
  return (
    <>
      {fields.map((field) => (
        <FormField key={field.key} field={field} form={form} setForm={setForm} t={t} />
      ))}
    </>
  )
}

// GroupMembersField edits a distribution list's members.
function GroupMembersField({ form, setForm, t }: { form: ContactForm; setForm: SetForm; t: TFunc }) {
  return (
    <div>
      <label className="text-sm font-medium">{t("contacts.members")}</label>
      <Textarea
        className="mt-1"
        placeholder={t("contacts.membersPlaceholder")}
        value={form.members}
        onChange={(e) => setForm({ ...form, members: e.target.value })}
        rows={3}
      />
      <p className="mt-1 text-xs text-muted-foreground">
        {t("contacts.membersHint")}
      </p>
    </div>
  )
}

// ContactFields edits the fields of a contact that is not a group.
function ContactFields({
  form,
  setForm,
  categories,
  t,
}: {
  form: ContactForm
  setForm: SetForm
  categories: CategoryOption[]
  t: TFunc
}) {
  return (
    <>
      <FormFields fields={PRIMARY_FIELDS} form={form} setForm={setForm} t={t} />
      <div className="grid grid-cols-2 gap-3">
        <FormFields fields={WORK_FIELDS} form={form} setForm={setForm} t={t} />
        {categories.length > 0 && (
          <div className="col-span-2 space-y-2">
            <label className="text-sm font-medium">{t("contacts.categories")}</label>
            <CategoryChips
              categories={categories}
              selected={form.categories}
              onToggle={(name, on) =>
                setForm((prev) => ({ ...prev, categories: toggledCategories(prev.categories, name, on) }))
              }
            />
          </div>
        )}
        <FormFields fields={ADDRESS_FIELDS} form={form} setForm={setForm} t={t} />
      </div>
    </>
  )
}

interface PhotoState {
  version: number
  error: boolean
  busy: boolean
  setError: (error: boolean) => void
  upload: (file: File) => void
  remove: () => void
}

// ContactDialog adds or edits a contact or a distribution list.
function ContactDialog({
  open,
  onOpenChange,
  editing,
  form,
  setForm,
  categories,
  photo,
  t,
  onSave,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  editing: Contact | null
  form: ContactForm
  setForm: SetForm
  categories: CategoryOption[]
  photo: PhotoState
  t: TFunc
  onSave: () => void
}) {
  const group = form.is_group
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            {editing ? t("contacts.editContact") : t("contacts.addContact")}
          </DialogTitle>
          <DialogDescription>
            {editing ? t("contacts.editDescription") : t("contacts.addDescription")}
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-4">
          {/* Contact photo (edit only; a new contact has no id yet). */}
          {editing && !group && (
            <PhotoEditor
              contactId={editing.id}
              name={form.name}
              version={photo.version}
              error={photo.error}
              busy={photo.busy}
              t={t}
              onError={() => photo.setError(true)}
              onUpload={photo.upload}
              onDelete={photo.remove}
            />
          )}
          <div className="col-span-2">
            <label className="text-sm font-medium">{t("common.name")}</label>
            <Input
              className="mt-1"
              placeholder={t("contacts.namePlaceholder")}
              value={form.name}
              onChange={(e) => setForm({ ...form, name: e.target.value })}
            />
          </div>
          {!group && <FormFields fields={NAME_FIELDS} form={form} setForm={setForm} t={t} />}

          {/* Distribution list toggle */}
          <div className="flex items-center gap-3">
            <Switch
              checked={group}
              onCheckedChange={(checked) =>
                setForm({ ...form, is_group: checked })
              }
            />
            <Label className="text-sm font-normal cursor-pointer" onClick={() => setForm({ ...form, is_group: !group })}>
              {t("contacts.distributionList")}
            </Label>
          </div>

          {group ? (
            <GroupMembersField form={form} setForm={setForm} t={t} />
          ) : (
            <ContactFields form={form} setForm={setForm} categories={categories} t={t} />
          )}

          <div className="flex justify-end gap-2">
            <Button variant="outline" onClick={() => onOpenChange(false)}>
              {t("common.cancel")}
            </Button>
            <Button onClick={onSave}>
              {editing ? t("common.update") : t("common.add")}
            </Button>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  )
}

// DeleteContactDialog asks before a contact is deleted.
function DeleteContactDialog({
  target,
  t,
  onCancel,
  onConfirm,
}: {
  target: Contact | null
  t: TFunc
  onCancel: () => void
  onConfirm: () => void
}) {
  return (
    <Dialog open={target !== null} onOpenChange={(open) => { if (!open) onCancel() }}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("contacts.deleteContact")}</DialogTitle>
          <DialogDescription>
            {t("contacts.deleteConfirm", { name: target?.name || t("contacts.thisContact") })}
          </DialogDescription>
        </DialogHeader>
        <DialogFooter>
          <Button variant="outline" onClick={onCancel}>
            {t("common.cancel")}
          </Button>
          <Button variant="destructive" onClick={onConfirm}>
            {t("common.delete")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// ContactsFooter shows the contact count and the (inactive) pager.
function ContactsFooter({ count, t }: { count: number; t: TFunc }) {
  return (
    <div className="flex items-center justify-between">
      <span className="text-sm text-muted-foreground">
        {t(count === 1 ? "contacts.contactCountSingular" : "contacts.contactCountPlural", { count: String(count) })}
      </span>
      <div className="flex items-center gap-2">
        <Button variant="outline" size="icon" disabled>
          <ChevronLeft className="h-4 w-4" />
        </Button>
        <Button variant="outline" size="icon" disabled>
          <ChevronRight className="h-4 w-4" />
        </Button>
      </div>
    </div>
  )
}

// downloadUrl saves url through a hidden <a download> click.
function downloadUrl(url: string, filename?: string) {
  const a = document.createElement("a")
  a.href = url
  if (filename) a.download = filename
  document.body.appendChild(a)
  a.click()
  document.body.removeChild(a)
}

// usePhoto holds the photo state of the contact being edited. The version
// cache-busts the contact's photo avatar after an upload or delete so the <img>
// reloads without a page refresh.
function usePhoto(editing: Contact | null, t: TFunc) {
  const [version, setVersion] = useState(0)
  const [error, setError] = useState(false)
  const [busy, setBusy] = useState(false)

  // run performs one photo change, then marks the photo present or absent and
  // cache-busts the avatar.
  const run = async (change: (id: string) => Promise<unknown>, removed: boolean, okKey: string) => {
    if (!editing) return
    setBusy(true)
    try {
      await change(editing.id)
      setError(removed)
      setVersion((v) => v + 1)
      toast.success(t(okKey))
    } catch {
      toast.error(t("contacts.photoUpdateFailed"))
    } finally {
      setBusy(false)
    }
  }

  const photo: PhotoState = {
    version,
    error,
    busy,
    setError,
    upload: (file) => void run((id) => api.uploadContactPhoto(id, file), false, "contacts.photoUpdated"),
    remove: () => void run((id) => api.deleteContactPhoto(id), true, "contacts.photoRemoved"),
  }
  // reset clears the error for a newly opened dialog; bump also reloads the photo.
  const reset = (bump: boolean) => {
    setError(false)
    if (bump) setVersion((v) => v + 1)
  }
  return { photo, reset }
}

export function ContactsPage() {
  const { t } = useI18n()
  const [contacts, setContacts] = useState<Contact[]>([])
  // The Global Address List (every directory user) shown as its own group, the
  // Exchange way, separate from the user's personal contacts.
  const [galEntries, setGalEntries] = useState<DirectoryEntry[]>([])
  const [searchQuery, setSearchQuery] = useState("")
  const [showAddDialog, setShowAddDialog] = useState(false)
  const [editingContact, setEditingContact] = useState<Contact | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<Contact | null>(null)
  // allCategories is the user's master category list (name + color), loaded once
  // so contacts can tag onto the same PidNameKeywords list the calendar uses.
  const [allCategories, setAllCategories] = useState<CategoryOption[]>([])
  const [, setLoading] = useState(true)
  const [formData, setFormData] = useState<ContactForm>(emptyContactForm)
  const { photo, reset: resetPhoto } = usePhoto(editingContact, t)

  // Load contacts from API on mount
  useEffect(() => {
    loadContacts()
    api.getCategories()
      .then((res) => setAllCategories(res.categories ?? []))
      .catch(() => setAllCategories([]))
  }, [])

  const loadContacts = async () => {
    setLoading(true)
    try {
      const result = await api.getContacts()
      if (result.contacts) {
        // Keep every field the server returned: the editor fills from this
        // record, and an update replaces the stored contact with what it sends.
        const loadedContacts: Contact[] = result.contacts.map((c: ApiContact) => ({
          ...c,
          labels: c.labels || [],
          is_group: c.is_group || false,
          members: c.members || [],
        }))
        setContacts(loadedContacts)
      }
    } catch (err) {
      console.error('Failed to load contacts:', err)
      toast.error(t("contacts.loadFailed"))
    } finally {
      setLoading(false)
    }
    // Load the Global Address List as a separate group (best-effort: a directory
    // failure must not break the personal contacts view).
    try {
      const dir = await api.searchDirectory("")
      setGalEntries(dir.entries ?? [])
    } catch (err) {
      console.error('Failed to load the global address list:', err)
    }
  }

  const matchesSearch = (name: string, email: string) =>
    name.toLowerCase().includes(searchQuery.toLowerCase()) ||
    email.toLowerCase().includes(searchQuery.toLowerCase())

  const filteredContacts = contacts.filter((c) => matchesSearch(c.name, c.email))
  const filteredGal = galEntries.filter((e) => matchesSearch(e.name || "", e.email))

  const handleAdd = () => {
    setFormData(emptyContactForm())
    setEditingContact(null)
    resetPhoto(false)
    setShowAddDialog(true)
  }

  const handleEdit = (contact: Contact) => {
    setFormData(contactFormOf(contact))
    setEditingContact(contact)
    resetPhoto(true)
    setShowAddDialog(true)
  }

  // saveContact stores the form as a new contact or over the one being edited,
  // and updates the list to match.
  const saveContact = async (members: string[] | undefined) => {
    const input = { ...formData, members }
    if (editingContact) {
      const result = await api.updateContact(editingContact.id, input)
      if (!result.contact) return
      // The update stores a new object, so the contact continues under the id
      // the server returns; the old id no longer names anything.
      const id = result.contact.id
      setContacts(contacts.map((c) =>
        c.id === editingContact.id
          ? { ...c, ...formData, id, members: members || [] }
          : c
      ))
      toast.success(t("contacts.contactUpdated"))
      return
    }
    const result = await api.createContact(input)
    if (!result.contact) return
    const newContact: Contact = {
      ...formData,
      id: result.contact.id,
      labels: [],
      members: members || [],
    }
    setContacts([...contacts, newContact])
    toast.success(t("contacts.contactAdded"))
  }

  const handleSave = async () => {
    const error = contactSaveError(formData)
    if (error) {
      toast.error(t(error))
      return
    }
    try {
      await saveContact(groupMembers(formData))
    } catch (err) {
      console.error('Failed to save contact:', err)
      toast.error(t("contacts.saveFailed"))
    }
    setShowAddDialog(false)
  }

  const handleDelete = async () => {
    if (!deleteTarget) return
    try {
      await api.deleteContact(deleteTarget.id)
      setContacts(contacts.filter((c) => c.id !== deleteTarget.id))
      toast.success(t("contacts.contactDeleted"))
    } catch (err) {
      console.error('Failed to delete contact:', err)
      toast.error(t("contacts.deleteFailed"))
    } finally {
      setDeleteTarget(null)
    }
  }

  const handleExportVCard = async () => {
    try {
      const res = await fetch("/api/v1/contacts/export", { credentials: "include" })
      if (!res.ok) throw new Error()
      const url = URL.createObjectURL(await res.blob())
      downloadUrl(url, "contacts.vcf")
      URL.revokeObjectURL(url)
    } catch {
      toast.error(t("contacts.exportFailed"))
    }
  }

  // handleExport saves a contact as a .vcf download. The backend sets the
  // attachment filename.
  const actions: ContactActions = {
    onEdit: handleEdit,
    onExport: (contact) => downloadUrl(api.contactVCardUrl(contact.id)),
    onDelete: setDeleteTarget,
  }

  return (
    <div className="space-y-4">
      <div className="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
        <div className="relative max-w-md flex-1">
          <Search className="absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
          <Input
            placeholder={t("contacts.searchPlaceholder")}
            className="pl-9"
            value={searchQuery}
            onChange={(e) => setSearchQuery(e.target.value)}
          />
        </div>
        <Button onClick={handleAdd}>
          <Plus className="h-4 w-4 mr-1" />
          {t("contacts.addContact")}
        </Button>
        <Button variant="outline" onClick={handleExportVCard}>
          <Download className="h-4 w-4 mr-1" />
          {t("contacts.exportVCard")}
        </Button>
      </div>

      {filteredContacts.length === 0 && filteredGal.length === 0 ? (
        <NoContacts searching={searchQuery !== ""} t={t} />
      ) : (
        <div className="space-y-6">
          <MyContacts contacts={filteredContacts} t={t} actions={actions} />
          <GalSection entries={filteredGal} t={t} />
        </div>
      )}

      <ContactsFooter count={filteredContacts.length} t={t} />

      <ContactDialog
        open={showAddDialog}
        onOpenChange={setShowAddDialog}
        editing={editingContact}
        form={formData}
        setForm={setFormData}
        categories={allCategories}
        photo={photo}
        t={t}
        onSave={() => void handleSave()}
      />

      <DeleteContactDialog
        target={deleteTarget}
        t={t}
        onCancel={() => setDeleteTarget(null)}
        onConfirm={() => void handleDelete()}
      />
    </div>
  )
}
