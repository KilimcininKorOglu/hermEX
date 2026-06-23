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
  MoreHorizontal,
  User,
  Download,
  Users,
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
import api, { Contact as ApiContact } from "@/utils/api"
import { useI18n } from "@/hooks/useI18n"
import { Switch } from "@/components/ui/switch"
import { Textarea } from "@/components/ui/textarea"
import { Label } from "@/components/ui/label"

// Local contact type for the page (extends API contact with labels)
interface Contact {
  id: string
  name: string
  email: string
  phone?: string
  company?: string
  labels: string[]
  is_group?: boolean
  members?: string[]
}

export function ContactsPage() {
  const { t } = useI18n()
  const [contacts, setContacts] = useState<Contact[]>([])
  const [searchQuery, setSearchQuery] = useState("")
  const [showAddDialog, setShowAddDialog] = useState(false)
  const [editingContact, setEditingContact] = useState<Contact | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<Contact | null>(null)
  const [, setLoading] = useState(true)
  const [formData, setFormData] = useState({
    name: "",
    email: "",
    phone: "",
    company: "",
    is_group: false,
    members: "",
  })

  // Load contacts from API on mount
  useEffect(() => {
    loadContacts()
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
  }

  const filteredContacts = contacts.filter(
    (c) =>
      c.name.toLowerCase().includes(searchQuery.toLowerCase()) ||
      c.email.toLowerCase().includes(searchQuery.toLowerCase())
  )

  const handleAdd = () => {
    setFormData({ name: "", email: "", phone: "", company: "", is_group: false, members: "" })
    setEditingContact(null)
    setShowAddDialog(true)
  }

  const handleEdit = (contact: Contact) => {
    setFormData({
      name: contact.name,
      email: contact.email || "",
      phone: contact.phone || "",
      company: contact.company || "",
      is_group: contact.is_group || false,
      members: (contact.members || []).join(", "),
    })
    setEditingContact(contact)
    setShowAddDialog(true)
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
          name: formData.name,
          email: formData.email,
          phone: formData.phone,
          company: formData.company,
          is_group: formData.is_group,
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
          name: formData.name,
          email: formData.email,
          phone: formData.phone,
          company: formData.company,
          is_group: formData.is_group,
          members,
        })
        if (result.contact) {
          const newContact: Contact = {
            id: result.contact.id,
            name: formData.name,
            email: formData.email,
            phone: formData.phone,
            company: formData.company,
            labels: [],
            is_group: formData.is_group,
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

      {filteredContacts.length === 0 ? (
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
            </div>
          ))}
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
            <div>
              <label className="text-sm font-medium">{t("common.name")}</label>
              <Input
                className="mt-1"
                placeholder={t("contacts.namePlaceholder")}
                value={formData.name}
                onChange={(e) => setFormData({ ...formData, name: e.target.value })}
              />
            </div>

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
