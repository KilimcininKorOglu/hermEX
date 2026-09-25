import type { Contact as ApiContact } from "@/utils/api"

// ContactForm is the contact dialog's state. Every text field is a string so
// the controlled inputs never switch between controlled and uncontrolled, and
// a group's members are edited as one comma-separated string.
export interface ContactForm {
  name: string
  prefix: string
  firstName: string
  middleName: string
  lastName: string
  suffix: string
  email: string
  email2: string
  email3: string
  phone: string
  company: string
  jobTitle: string
  department: string
  mobilePhone: string
  homePhone: string
  businessFax: string
  birthday: string
  anniversary: string
  billing: string
  nickname: string
  fileAs: string
  profession: string
  spouse: string
  categories: string[]
  homeStreet: string
  homeCity: string
  homeState: string
  homePostal: string
  homeCountry: string
  workStreet: string
  workCity: string
  workState: string
  workPostal: string
  workCountry: string
  otherStreet: string
  otherCity: string
  otherState: string
  otherPostal: string
  otherCountry: string
  imAddress: string
  webPage: string
  assistant: string
  manager: string
  office: string
  is_group: boolean
  members: string
}

// ContactTextKey names the plain text fields of the form.
export type ContactTextKey = Exclude<keyof ContactForm, "categories" | "is_group">

const TEXT_KEYS: ContactTextKey[] = [
  "name", "prefix", "firstName", "middleName", "lastName", "suffix", "email", "email2", "email3", "phone",
  "company", "jobTitle", "department", "mobilePhone", "homePhone", "businessFax", "birthday", "anniversary",
  "billing", "nickname", "fileAs", "profession", "spouse", "homeStreet", "homeCity", "homeState", "homePostal",
  "homeCountry", "workStreet", "workCity", "workState", "workPostal", "workCountry", "otherStreet", "otherCity",
  "otherState", "otherPostal", "otherCountry", "imAddress", "webPage", "assistant", "manager", "office", "members",
]

// emptyContactForm returns a blank form for a new contact.
export function emptyContactForm(): ContactForm {
  const form = { categories: [] as string[], is_group: false } as ContactForm
  for (const key of TEXT_KEYS) form[key] = ""
  return form
}

// contactFormOf fills the form from a contact; an absent field reads as empty.
export function contactFormOf(contact: ApiContact): ContactForm {
  const form = emptyContactForm()
  const source = contact as unknown as Record<string, string | undefined>
  for (const key of TEXT_KEYS) {
    if (key !== "members") form[key] = source[key] || ""
  }
  form.categories = contact.categories || []
  form.is_group = contact.is_group || false
  form.members = (contact.members || []).join(", ")
  return form
}

// contactSaveError returns the i18n key of the problem that stops the form from
// being saved, or null: a name is always required, and a group needs members
// where a contact needs an address.
export function contactSaveError(form: ContactForm): string | null {
  if (!form.name) return "contacts.nameRequired"
  if (form.is_group) return form.members.trim() ? null : "contacts.membersRequired"
  return form.email.trim() ? null : "contacts.emailRequired"
}

// groupMembers parses a group's comma-separated members, or undefined for a
// contact that is not a group.
export function groupMembers(form: ContactForm): string[] | undefined {
  if (!form.is_group) return undefined
  return form.members.split(",").map((m) => m.trim()).filter(Boolean)
}

// mapQuery is the address a map search opens for a contact: the work address
// when it has one, the home address otherwise.
export function mapQuery(contact: ApiContact): string {
  return [contact.workStreet, contact.workCity, contact.workCountry].filter(Boolean).join(", ") ||
    [contact.homeStreet, contact.homeCity, contact.homeCountry].filter(Boolean).join(", ")
}

// initials returns up to two upper-case initials of a name.
export function initials(name: string): string {
  return name
    .split(" ")
    .map((n) => n[0])
    .join("")
    .toUpperCase()
    .slice(0, 2)
}
