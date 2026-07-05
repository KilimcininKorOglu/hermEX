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

// Local contact type for the page (extends API contact with labels)
interface Contact {
  id: string
  name: string
  prefix?: string
  firstName?: string
  middleName?: string
  lastName?: string
  suffix?: string
  email: string
  email2?: string
  email3?: string
  phone?: string
  company?: string
  jobTitle?: string
  department?: string
  mobilePhone?: string
  homePhone?: string
  businessFax?: string
  birthday?: string
  anniversary?: string
  billing?: string
  nickname?: string
  fileAs?: string
  profession?: string
  spouse?: string
  categories?: string[]
  homeStreet?: string
  homeCity?: string
  homeState?: string
  homePostal?: string
  homeCountry?: string
  workStreet?: string
  workCity?: string
  workState?: string
  workPostal?: string
  workCountry?: string
  imAddress?: string
  webPage?: string
  assistant?: string
  manager?: string
  office?: string
  labels: string[]
  is_group?: boolean
  members?: string[]
}

export function ContactsPage() {
  const { t } = useI18n()
  const [contacts, setContacts] = useState<Contact[]>([])
  // The Global Address List (every directory user) shown as its own group, the
  // Exchange way — separate from the user's personal contacts.
  const [galEntries, setGalEntries] = useState<DirectoryEntry[]>([])
  const [searchQuery, setSearchQuery] = useState("")
  const [showAddDialog, setShowAddDialog] = useState(false)
  // expandedGroups holds the ids of contact groups whose member list is shown
  // inline (expanddistlist: a distribution list expanded to its member addresses).
  const [expandedGroups, setExpandedGroups] = useState<Set<string>>(new Set())
  // contactView toggles between a row list and a card grid (ContactCardView).
  const [contactView, setContactView] = useState<"list" | "grid">("list")
  const [editingContact, setEditingContact] = useState<Contact | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<Contact | null>(null)
  // photoVersion cache-busts the contact's photo avatar after an upload or
  // delete so the <img> reloads without a page refresh.
  const [photoVersion, setPhotoVersion] = useState(0)
  const [photoError, setPhotoError] = useState(false)
  const [photoBusy, setPhotoBusy] = useState(false)
  // allCategories is the user's master category list (name + color), loaded once
  // so contacts can tag onto the same PidNameKeywords list the calendar uses.
  const [allCategories, setAllCategories] = useState<{ name: string; color?: string }[]>([])
  const [, setLoading] = useState(true)
  const [formData, setFormData] = useState({
    name: "",
    prefix: "",
    firstName: "",
    middleName: "",
    lastName: "",
    suffix: "",
    email: "",
    email2: "",
    email3: "",
    phone: "",
    company: "",
    jobTitle: "",
    department: "",
    mobilePhone: "",
    homePhone: "",
    businessFax: "",
    birthday: "",
    anniversary: "",
    billing: "",
    nickname: "",
    fileAs: "",
    profession: "",
    spouse: "",
    categories: [] as string[],
    homeStreet: "",
    homeCity: "",
    homeState: "",
    homePostal: "",
    homeCountry: "",
    workStreet: "",
    workCity: "",
    workState: "",
    workPostal: "",
    workCountry: "",
    imAddress: "",
    webPage: "",
    assistant: "",
    manager: "",
    office: "",
    is_group: false,
    members: "",
  })

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
        // Convert API contacts to local format with empty labels
        const loadedContacts: Contact[] = result.contacts.map((c: ApiContact) => ({
          id: c.id,
          name: c.name,
          email: c.email,
          phone: c.phone,
          company: c.company,
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
    setFormData({ name: "", prefix: "", firstName: "", middleName: "", lastName: "", suffix: "", email: "", email2: "", email3: "", phone: "", company: "", jobTitle: "", department: "", mobilePhone: "", homePhone: "", businessFax: "", birthday: "", anniversary: "", billing: "", nickname: "", fileAs: "", profession: "", spouse: "", categories: [], homeStreet: "", homeCity: "", homeState: "", homePostal: "", homeCountry: "", workStreet: "", workCity: "", workState: "", workPostal: "", workCountry: "", imAddress: "", webPage: "", assistant: "", manager: "", office: "", is_group: false, members: "" })
    setEditingContact(null)
    setPhotoError(false)
    setShowAddDialog(true)
  }

  const handleEdit = (contact: Contact) => {
    setFormData({
      name: contact.name,
      prefix: contact.prefix || "",
      firstName: contact.firstName || "",
      middleName: contact.middleName || "",
      lastName: contact.lastName || "",
      suffix: contact.suffix || "",
      email: contact.email || "",
      email2: contact.email2 || "",
      email3: contact.email3 || "",
      phone: contact.phone || "",
      company: contact.company || "",
      jobTitle: contact.jobTitle || "",
      department: contact.department || "",
      mobilePhone: contact.mobilePhone || "",
      homePhone: contact.homePhone || "",
      businessFax: contact.businessFax || "",
      birthday: contact.birthday || "",
      anniversary: contact.anniversary || "",
      billing: contact.billing || "",
      nickname: contact.nickname || "",
      fileAs: contact.fileAs || "",
      profession: contact.profession || "",
      spouse: contact.spouse || "",
      categories: contact.categories || [],
      homeStreet: contact.homeStreet || "",
      homeCity: contact.homeCity || "",
      homeState: contact.homeState || "",
      homePostal: contact.homePostal || "",
      homeCountry: contact.homeCountry || "",
      workStreet: contact.workStreet || "",
      workCity: contact.workCity || "",
      workState: contact.workState || "",
      workPostal: contact.workPostal || "",
      workCountry: contact.workCountry || "",
      imAddress: contact.imAddress || "",
      webPage: contact.webPage || "",
      assistant: contact.assistant || "",
      manager: contact.manager || "",
      office: contact.office || "",
      is_group: contact.is_group || false,
      members: (contact.members || []).join(", "),
    })
    setEditingContact(contact)
    setPhotoError(false)
    setPhotoVersion((v) => v + 1)
    setShowAddDialog(true)
  }

  // handlePhotoUpload replaces the contact's photo with the chosen file, then
  // cache-busts the avatar so the new image renders immediately.
  const handlePhotoUpload = async (file: File) => {
    if (!editingContact) return
    setPhotoBusy(true)
    try {
      await api.uploadContactPhoto(editingContact.id, file)
      setPhotoError(false)
      setPhotoVersion((v) => v + 1)
      toast.success(t("contacts.photoUpdated"))
    } catch {
      toast.error(t("contacts.photoUpdateFailed"))
    } finally {
      setPhotoBusy(false)
    }
  }

  const handlePhotoDelete = async () => {
    if (!editingContact) return
    setPhotoBusy(true)
    try {
      await api.deleteContactPhoto(editingContact.id)
      setPhotoError(true)
      setPhotoVersion((v) => v + 1)
      toast.success(t("contacts.photoRemoved"))
    } catch {
      toast.error(t("contacts.photoUpdateFailed"))
    } finally {
      setPhotoBusy(false)
    }
  }

  const handleSave = async () => {
    if (!formData.name) {
      toast.error(t("contacts.nameRequired"))
      return
    }

    if (formData.is_group) {
      // Distribution list: members required
      if (!formData.members.trim()) {
        toast.error(t("contacts.membersRequired"))
        return
      }
    } else {
      // Regular contact: email required
      if (!formData.email.trim()) {
        toast.error(t("contacts.emailRequired"))
        return
      }
    }

    // Parse members into array
    const members = formData.is_group
      ? formData.members.split(",").map((m) => m.trim()).filter(Boolean)
      : undefined

    try {
      if (editingContact) {
        // Update existing contact
        const result = await api.updateContact(editingContact.id, {
          ...formData,
          members,
        })
        if (result.contact) {
          setContacts(contacts.map((c) =>
            c.id === editingContact.id
              ? { ...c, ...formData, members: members || [] }
              : c
          ))
          toast.success(t("contacts.contactUpdated"))
        }
      } else {
        // Create new contact
        const result = await api.createContact({
          ...formData,
          members,
        })
        if (result.contact) {
          const newContact: Contact = {
            ...formData,
            id: result.contact.id,
            labels: [],
            members: members || [],
          }
          setContacts([...contacts, newContact])
          toast.success(t("contacts.contactAdded"))
        }
      }
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

  // handleExport saves a contact as a .vcf download. The backend sets the
  // attachment filename, so a hidden <a download> click triggers the save.
  const handleExport = (contact: Contact) => {
    const a = document.createElement("a")
    a.href = api.contactVCardUrl(contact.id)
    document.body.appendChild(a)
    a.click()
    document.body.removeChild(a)
  }

  const handleExportVCard = async () => {
    try {
      const res = await fetch("/api/v1/contacts/export", { credentials: "include" })
      if (!res.ok) throw new Error()
      const blob = await res.blob()
      const url = URL.createObjectURL(blob)
      const a = document.createElement("a")
      a.href = url
      a.download = "contacts.vcf"
      document.body.appendChild(a)
      a.click()
      document.body.removeChild(a)
      URL.revokeObjectURL(url)
    } catch {
      toast.error(t("contacts.exportFailed"))
    }
  }

  const getInitials = (name: string) => {
    return name
      .split(" ")
      .map((n) => n[0])
      .join("")
      .toUpperCase()
      .slice(0, 2)
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
        <div className="flex flex-col items-center justify-center py-16 text-center">
          <div className="rounded-full bg-muted p-4">
            <User className="h-8 w-8 text-muted-foreground" />
          </div>
          <h3 className="mt-4 text-lg font-semibold">{t("contacts.noContacts")}</h3>
          <p className="text-sm text-muted-foreground">
            {searchQuery ? t("contacts.noSearchMatch") : t("contacts.emptyHint")}
          </p>
        </div>
      ) : (
        <div className="space-y-6">
          {filteredContacts.length > 0 && (
            <div>
              <div className="mb-2 flex items-center justify-between px-1">
                <h2 className="text-sm font-semibold text-muted-foreground">
                  {t("contacts.myContacts")} ({filteredContacts.length})
                </h2>
                <div className="flex rounded-md border">
                  <Button variant={contactView === "list" ? "secondary" : "ghost"} size="icon" className="h-7 w-7 rounded-r-none" onClick={() => setContactView("list")} aria-label={t("contacts.listView")}><ListIcon className="h-4 w-4" /></Button>
                  <Button variant={contactView === "grid" ? "secondary" : "ghost"} size="icon" className="h-7 w-7 rounded-l-none" onClick={() => setContactView("grid")} aria-label={t("contacts.gridView")}><LayoutGrid className="h-4 w-4" /></Button>
                </div>
              </div>
              {contactView === "grid" ? (
                <div className="grid grid-cols-2 gap-3 sm:grid-cols-3">
                  {filteredContacts.map((contact) => (
                    <button
                      key={contact.id}
                      onClick={() => handleEdit(contact)}
                      className="flex flex-col items-center gap-2 rounded-lg border bg-card p-4 text-center hover:bg-accent/50 transition-colors"
                    >
                      <Avatar className="h-12 w-12">
                        <AvatarFallback className="bg-gradient-to-br from-primary to-primary/80 text-primary-foreground font-semibold">
                          {contact.is_group ? <Users className="h-5 w-5" /> : getInitials(contact.name)}
                        </AvatarFallback>
                      </Avatar>
                      <span className="font-medium truncate w-full">{contact.name}</span>
                      <span className="text-xs text-muted-foreground truncate w-full">{contact.is_group ? `${(contact.members || []).length} ${t("contacts.membersCount")}` : contact.email}</span>
                    </button>
                  ))}
                </div>
              ) : (
              <div className="rounded-lg border bg-card">
          {filteredContacts.map((contact, index) => (
            <div key={contact.id}>
              {index > 0 && <Separator />}
              <div className="flex items-center gap-4 p-4 hover:bg-accent/50 transition-colors">
                <Avatar className="h-10 w-10">
                  <AvatarFallback className="bg-gradient-to-br from-primary to-primary/80 text-primary-foreground font-semibold">
                    {contact.is_group ? <Users className="h-4 w-4" /> : getInitials(contact.name)}
                  </AvatarFallback>
                </Avatar>
                {/* A contact group can be expanded to list its members (expanddistlist). */}
                {contact.is_group && (contact.members || []).length > 0 && (
                  <button
                    type="button"
                    aria-label={expandedGroups.has(contact.id) ? t("contacts.collapse") : t("contacts.expand")}
                    onClick={() =>
                      setExpandedGroups((prev) => {
                        const next = new Set(prev)
                        if (next.has(contact.id)) next.delete(contact.id)
                        else next.add(contact.id)
                        return next
                      })
                    }
                    className="shrink-0 rounded p-1 text-muted-foreground hover:bg-accent"
                  >
                    {expandedGroups.has(contact.id) ? <ChevronDown className="h-4 w-4" /> : <ChevronRight className="h-4 w-4" />}
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
                    {contact.is_group ? (
                      <span className="flex items-center gap-1">
                        <Users className="h-3 w-3" />
                        {(contact.members || []).length} {t("contacts.membersCount")}
                      </span>
                    ) : (
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
                    )}
                  </div>
                </div>
                <DropdownMenu>
                  <DropdownMenuTrigger asChild>
                    <Button variant="ghost" size="icon" className="h-8 w-8">
                      <MoreHorizontal className="h-4 w-4" />
                    </Button>
                  </DropdownMenuTrigger>
                  <DropdownMenuContent align="end">
                    <DropdownMenuItem onClick={() => handleEdit(contact)}>
                      <Edit className="h-4 w-4 mr-2" />
                      {t("common.edit")}
                    </DropdownMenuItem>
                    {!contact.is_group && (
                      <DropdownMenuItem onClick={() => handleExport(contact)}>
                        <Download className="h-4 w-4 mr-2" />
                        {t("contacts.exportVCard")}
                      </DropdownMenuItem>
                    )}
                    <DropdownMenuItem
                      className="text-destructive"
                      onClick={() => setDeleteTarget(contact)}
                    >
                      <Trash2 className="h-4 w-4 mr-2" />
                      {t("common.delete")}
                    </DropdownMenuItem>
                  </DropdownMenuContent>
                </DropdownMenu>
              </div>
              {/* Expanded distribution list: the group's member addresses. */}
              {contact.is_group && expandedGroups.has(contact.id) && (
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
          ))}
              </div>
              )}
            </div>
          )}
          {filteredGal.length > 0 && (
            <div>
              <h2 className="mb-2 px-1 text-sm font-semibold text-muted-foreground">
                {t("contacts.globalAddressList")} ({filteredGal.length})
              </h2>
              <div className="rounded-lg border bg-card">
                {filteredGal.map((entry, index) => (
                  <div key={entry.email}>
                    {index > 0 && <Separator />}
                    <div className="flex items-center gap-4 p-4 hover:bg-accent/50 transition-colors">
                      <Avatar className="h-10 w-10">
                        <AvatarFallback className="bg-gradient-to-br from-muted-foreground/70 to-muted-foreground text-background font-semibold">
                          {getInitials(entry.name || entry.email)}
                        </AvatarFallback>
                      </Avatar>
                      <div className="flex-1 min-w-0">
                        <div className="flex items-center gap-2">
                          <span className="font-medium">{entry.name || entry.email}</span>
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
                ))}
              </div>
            </div>
          )}
        </div>
      )}

      <div className="flex items-center justify-between">
        <span className="text-sm text-muted-foreground">
          {filteredContacts.length === 1
            ? t("contacts.contactCountSingular", { count: String(filteredContacts.length) })
            : t("contacts.contactCountPlural", { count: String(filteredContacts.length) })}
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

      <Dialog open={showAddDialog} onOpenChange={setShowAddDialog}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>
              {editingContact ? t("contacts.editContact") : t("contacts.addContact")}
            </DialogTitle>
            <DialogDescription>
              {editingContact ? t("contacts.editDescription") : t("contacts.addDescription")}
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-4">
            {/* Contact photo (edit only; a new contact has no id yet). Avatar
                loads the photo URL with a cache-busting version so an upload or
                delete re-renders without a page refresh. */}
            {editingContact && !formData.is_group && (
              <div className="flex items-center gap-4">
                <Avatar className="h-16 w-16">
                  {!photoError ? (
                    <img
                      src={api.contactPhotoUrl(editingContact.id) + "?v=" + photoVersion}
                      alt={formData.name}
                      className="h-full w-full rounded-full object-cover"
                      onError={() => setPhotoError(true)}
                    />
                  ) : null}
                  <AvatarFallback>{formData.name?.charAt(0)?.toUpperCase() || "?"}</AvatarFallback>
                </Avatar>
                <div className="flex flex-col gap-1">
                  <label className="inline-flex cursor-pointer text-sm text-primary hover:underline">
                    <span>{t("contacts.changePhoto")}</span>
                    <input
                      type="file"
                      accept="image/*"
                      className="hidden"
                      disabled={photoBusy}
                      onChange={(e) => {
                        const f = e.target.files?.[0]
                        if (f) handlePhotoUpload(f)
                        e.target.value = ""
                      }}
                    />
                  </label>
                  {!photoError && (
                    <button
                      type="button"
                      className="text-left text-sm text-destructive hover:underline disabled:opacity-50"
                      disabled={photoBusy}
                      onClick={handlePhotoDelete}
                    >
                      {t("contacts.removePhoto")}
                    </button>
                  )}
                </div>
              </div>
            )}
            <div className="col-span-2">
              <label className="text-sm font-medium">{t("common.name")}</label>
              <Input
                className="mt-1"
                placeholder={t("contacts.namePlaceholder")}
                value={formData.name}
                onChange={(e) => setFormData({ ...formData, name: e.target.value })}
              />
            </div>
            {!formData.is_group && (
              <>
                <div>
                  <label className="text-sm font-medium">{t("contacts.prefix")}</label>
                  <Input className="mt-1" placeholder="Dr." value={formData.prefix} onChange={(e) => setFormData({ ...formData, prefix: e.target.value })} />
                </div>
                <div>
                  <label className="text-sm font-medium">{t("contacts.firstName")}</label>
                  <Input className="mt-1" value={formData.firstName} onChange={(e) => setFormData({ ...formData, firstName: e.target.value })} />
                </div>
                <div>
                  <label className="text-sm font-medium">{t("contacts.middleName")}</label>
                  <Input className="mt-1" value={formData.middleName} onChange={(e) => setFormData({ ...formData, middleName: e.target.value })} />
                </div>
                <div>
                  <label className="text-sm font-medium">{t("contacts.lastName")}</label>
                  <Input className="mt-1" value={formData.lastName} onChange={(e) => setFormData({ ...formData, lastName: e.target.value })} />
                </div>
                <div>
                  <label className="text-sm font-medium">{t("contacts.suffix")}</label>
                  <Input className="mt-1" placeholder="Jr." value={formData.suffix} onChange={(e) => setFormData({ ...formData, suffix: e.target.value })} />
                </div>
              </>
            )}

            {/* Distribution list toggle */}
            <div className="flex items-center gap-3">
              <Switch
                checked={formData.is_group}
                onCheckedChange={(checked) =>
                  setFormData({ ...formData, is_group: checked })
                }
              />
              <Label className="text-sm font-normal cursor-pointer" onClick={() => setFormData({ ...formData, is_group: !formData.is_group })}>
                {t("contacts.distributionList")}
              </Label>
            </div>

            {formData.is_group ? (
              /* Members field for distribution lists */
              <div>
                <label className="text-sm font-medium">{t("contacts.members")}</label>
                <Textarea
                  className="mt-1"
                  placeholder={t("contacts.membersPlaceholder")}
                  value={formData.members}
                  onChange={(e) => setFormData({ ...formData, members: e.target.value })}
                  rows={3}
                />
                <p className="mt-1 text-xs text-muted-foreground">
                  {t("contacts.membersHint")}
                </p>
              </div>
            ) : (
              /* Regular contact fields */
              <>
                <div>
                  <label className="text-sm font-medium">{t("common.email")}</label>
                  <Input
                    className="mt-1"
                    type="email"
                    placeholder="john@example.com"
                    value={formData.email}
                    onChange={(e) => setFormData({ ...formData, email: e.target.value })}
                  />
                </div>
                <div>
                  <label className="text-sm font-medium">{t("contacts.email2")}</label>
                  <Input
                    className="mt-1"
                    type="email"
                    value={formData.email2}
                    onChange={(e) => setFormData({ ...formData, email2: e.target.value })}
                  />
                </div>
                <div>
                  <label className="text-sm font-medium">{t("contacts.email3")}</label>
                  <Input
                    className="mt-1"
                    type="email"
                    value={formData.email3}
                    onChange={(e) => setFormData({ ...formData, email3: e.target.value })}
                  />
                </div>
                <div>
                  <label className="text-sm font-medium">{t("contacts.phoneOptional")}</label>
                  <Input
                    className="mt-1"
                    placeholder="+1 555 123 4567"
                    value={formData.phone}
                    onChange={(e) => setFormData({ ...formData, phone: e.target.value })}
                  />
                </div>
                <div>
                  <label className="text-sm font-medium">{t("contacts.companyOptional")}</label>
                  <Input
                    className="mt-1"
                    placeholder={t("contacts.companyPlaceholder")}
                    value={formData.company}
                    onChange={(e) => setFormData({ ...formData, company: e.target.value })}
                  />
                </div>
                {/* Rich contact fields: job title, department, more phones, birthday,
                    home address, IM, web page. They round-trip through vCard. */}
                <div className="grid grid-cols-2 gap-3">
                  <div>
                    <label className="text-sm font-medium">{t("contacts.jobTitle")}</label>
                    <Input className="mt-1" value={formData.jobTitle} onChange={(e) => setFormData({ ...formData, jobTitle: e.target.value })} />
                  </div>
                  <div>
                    <label className="text-sm font-medium">{t("contacts.department")}</label>
                    <Input className="mt-1" value={formData.department} onChange={(e) => setFormData({ ...formData, department: e.target.value })} />
                  </div>
                  <div>
                    <label className="text-sm font-medium">{t("contacts.assistant")}</label>
                    <Input className="mt-1" value={formData.assistant} onChange={(e) => setFormData({ ...formData, assistant: e.target.value })} />
                  </div>
                  <div>
                    <label className="text-sm font-medium">{t("contacts.manager")}</label>
                    <Input className="mt-1" value={formData.manager} onChange={(e) => setFormData({ ...formData, manager: e.target.value })} />
                  </div>
                  <div>
                    <label className="text-sm font-medium">{t("contacts.office")}</label>
                    <Input className="mt-1" value={formData.office} onChange={(e) => setFormData({ ...formData, office: e.target.value })} />
                  </div>
                  <div>
                    <label className="text-sm font-medium">{t("contacts.mobilePhone")}</label>
                    <Input className="mt-1" value={formData.mobilePhone} onChange={(e) => setFormData({ ...formData, mobilePhone: e.target.value })} />
                  </div>
                  <div>
                    <label className="text-sm font-medium">{t("contacts.homePhone")}</label>
                    <Input className="mt-1" value={formData.homePhone} onChange={(e) => setFormData({ ...formData, homePhone: e.target.value })} />
                  </div>
                  <div>
                    <label className="text-sm font-medium">{t("contacts.businessFax")}</label>
                    <Input className="mt-1" value={formData.businessFax} onChange={(e) => setFormData({ ...formData, businessFax: e.target.value })} />
                  </div>
                  <div>
                    <label className="text-sm font-medium">{t("contacts.birthday")}</label>
                    <Input className="mt-1" type="date" value={formData.birthday} onChange={(e) => setFormData({ ...formData, birthday: e.target.value })} />
                  </div>
                  <div>
                    <label className="text-sm font-medium">{t("contacts.anniversary")}</label>
                    <Input className="mt-1" type="date" value={formData.anniversary} onChange={(e) => setFormData({ ...formData, anniversary: e.target.value })} />
                  </div>
                  <div>
                    <label className="text-sm font-medium">{t("contacts.billing")}</label>
                    <Input className="mt-1" value={formData.billing} onChange={(e) => setFormData({ ...formData, billing: e.target.value })} />
                  </div>
                  <div>
                    <label className="text-sm font-medium">{t("contacts.nickname")}</label>
                    <Input className="mt-1" value={formData.nickname} onChange={(e) => setFormData({ ...formData, nickname: e.target.value })} />
                  </div>
                  <div>
                    <label className="text-sm font-medium">{t("contacts.fileAs")}</label>
                    <Input className="mt-1" value={formData.fileAs} onChange={(e) => setFormData({ ...formData, fileAs: e.target.value })} />
                  </div>
                  <div>
                    <label className="text-sm font-medium">{t("contacts.profession")}</label>
                    <Input className="mt-1" value={formData.profession} onChange={(e) => setFormData({ ...formData, profession: e.target.value })} />
                  </div>
                  <div>
                    <label className="text-sm font-medium">{t("contacts.spouse")}</label>
                    <Input className="mt-1" value={formData.spouse} onChange={(e) => setFormData({ ...formData, spouse: e.target.value })} />
                  </div>
                  {allCategories.length > 0 && !formData.is_group && (
                    <div className="col-span-2 space-y-2">
                      <label className="text-sm font-medium">{t("contacts.categories")}</label>
                      <div className="flex flex-wrap gap-1.5">
                        {allCategories.map((cat) => {
                          const on = formData.categories.includes(cat.name)
                          return (
                            <button
                              key={cat.name}
                              type="button"
                              className="rounded-full border px-2.5 py-0.5 text-xs transition-colors"
                              style={{
                                borderColor: cat.color ?? "#3b82f6",
                                color: cat.color ?? "#3b82f6",
                                backgroundColor: on ? `${cat.color ?? "#3b82f6"}15` : "transparent",
                                opacity: on ? 1 : 0.5,
                              }}
                              onClick={() =>
                                setFormData((prev) => ({
                                  ...prev,
                                  categories: on
                                    ? prev.categories.filter((c) => c !== cat.name)
                                    : [...prev.categories, cat.name],
                                }))
                              }
                            >
                              {cat.name}
                            </button>
                          )
                        })}
                      </div>
                    </div>
                  )}
                  <div>
                    <label className="text-sm font-medium">{t("contacts.imAddress")}</label>
                    <Input className="mt-1" value={formData.imAddress} onChange={(e) => setFormData({ ...formData, imAddress: e.target.value })} />
                  </div>
                  <div className="col-span-2">
                    <label className="text-sm font-medium">{t("contacts.webPage")}</label>
                    <Input className="mt-1" value={formData.webPage} onChange={(e) => setFormData({ ...formData, webPage: e.target.value })} />
                  </div>
                  <div>
                    <label className="text-sm font-medium">{t("contacts.homeStreet")}</label>
                    <Input className="mt-1" value={formData.homeStreet} onChange={(e) => setFormData({ ...formData, homeStreet: e.target.value })} />
                  </div>
                  <div>
                    <label className="text-sm font-medium">{t("contacts.homeCity")}</label>
                    <Input className="mt-1" value={formData.homeCity} onChange={(e) => setFormData({ ...formData, homeCity: e.target.value })} />
                  </div>
                  <div>
                    <label className="text-sm font-medium">{t("contacts.homeState")}</label>
                    <Input className="mt-1" value={formData.homeState} onChange={(e) => setFormData({ ...formData, homeState: e.target.value })} />
                  </div>
                  <div>
                    <label className="text-sm font-medium">{t("contacts.homePostal")}</label>
                    <Input className="mt-1" value={formData.homePostal} onChange={(e) => setFormData({ ...formData, homePostal: e.target.value })} />
                  </div>
                  <div className="col-span-2">
                    <label className="text-sm font-medium">{t("contacts.homeCountry")}</label>
                    <Input className="mt-1" value={formData.homeCountry} onChange={(e) => setFormData({ ...formData, homeCountry: e.target.value })} />
                  </div>
                  {/* Work address (ADR;TYPE=work). */}
                  <div>
                    <label className="text-sm font-medium">{t("contacts.workStreet")}</label>
                    <Input className="mt-1" value={formData.workStreet} onChange={(e) => setFormData({ ...formData, workStreet: e.target.value })} />
                  </div>
                  <div>
                    <label className="text-sm font-medium">{t("contacts.workCity")}</label>
                    <Input className="mt-1" value={formData.workCity} onChange={(e) => setFormData({ ...formData, workCity: e.target.value })} />
                  </div>
                  <div>
                    <label className="text-sm font-medium">{t("contacts.workState")}</label>
                    <Input className="mt-1" value={formData.workState} onChange={(e) => setFormData({ ...formData, workState: e.target.value })} />
                  </div>
                  <div>
                    <label className="text-sm font-medium">{t("contacts.workPostal")}</label>
                    <Input className="mt-1" value={formData.workPostal} onChange={(e) => setFormData({ ...formData, workPostal: e.target.value })} />
                  </div>
                  <div className="col-span-2">
                    <label className="text-sm font-medium">{t("contacts.workCountry")}</label>
                    <Input className="mt-1" value={formData.workCountry} onChange={(e) => setFormData({ ...formData, workCountry: e.target.value })} />
                  </div>
                </div>
              </>
            )}

            <div className="flex justify-end gap-2">
              <Button variant="outline" onClick={() => setShowAddDialog(false)}>
                {t("common.cancel")}
              </Button>
              <Button onClick={handleSave}>
                {editingContact ? t("common.update") : t("common.add")}
              </Button>
            </div>
          </div>
        </DialogContent>
      </Dialog>

      <Dialog open={deleteTarget !== null} onOpenChange={(open) => { if (!open) setDeleteTarget(null) }}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("contacts.deleteContact")}</DialogTitle>
            <DialogDescription>
              {t("contacts.deleteConfirm", { name: deleteTarget?.name || t("contacts.thisContact") })}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDeleteTarget(null)}>
              {t("common.cancel")}
            </Button>
            <Button variant="destructive" onClick={handleDelete}>
              {t("common.delete")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
