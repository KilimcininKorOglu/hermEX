import { describe, expect, it } from "vitest"
import { contactFormOf, contactSaveError, emptyContactForm, groupMembers, initials, mapQuery } from "./contactForm"

describe("contactFormOf", () => {
  it("fills every field from the contact and reads absent fields as empty", () => {
    const form = contactFormOf({ id: "1", name: "Ada Lovelace", email: "ada@hermex.test", jobTitle: "Analyst", homeCity: "London", categories: ["Red"] })
    expect(form).toEqual({ ...emptyContactForm(), name: "Ada Lovelace", email: "ada@hermex.test", jobTitle: "Analyst", homeCity: "London", categories: ["Red"] })
  })

  it("joins a group's members", () => {
    const form = contactFormOf({ id: "2", name: "Team", email: "", is_group: true, members: ["a@hermex.test", "b@hermex.test"] })
    expect(form.is_group).toBe(true)
    expect(form.members).toBe("a@hermex.test, b@hermex.test")
  })
})

describe("contactSaveError", () => {
  it("requires a name, then members for a group or an address for a contact", () => {
    expect(contactSaveError(emptyContactForm())).toBe("contacts.nameRequired")
    expect(contactSaveError({ ...emptyContactForm(), name: "A", email: " " })).toBe("contacts.emailRequired")
    expect(contactSaveError({ ...emptyContactForm(), name: "A", email: "a@hermex.test" })).toBeNull()
    expect(contactSaveError({ ...emptyContactForm(), name: "T", is_group: true })).toBe("contacts.membersRequired")
    expect(contactSaveError({ ...emptyContactForm(), name: "T", is_group: true, members: "a@hermex.test" })).toBeNull()
  })
})

describe("groupMembers", () => {
  it("splits and trims a group's members and is undefined for a contact", () => {
    expect(groupMembers({ ...emptyContactForm(), is_group: true, members: " a@x ,, b@x " })).toEqual(["a@x", "b@x"])
    expect(groupMembers({ ...emptyContactForm(), members: "a@x" })).toBeUndefined()
  })
})

describe("mapQuery", () => {
  it("prefers the work address and falls back to the home address", () => {
    expect(mapQuery({ id: "1", name: "A", email: "", workStreet: "1 Main", workCity: "Ankara", homeCity: "Izmir" })).toBe("1 Main, Ankara")
    expect(mapQuery({ id: "1", name: "A", email: "", homeStreet: "2 Side", homeCountry: "TR" })).toBe("2 Side, TR")
  })
})

describe("initials", () => {
  it("takes the first letter of up to two words", () => {
    expect(initials("ada byron lovelace")).toBe("AB")
    expect(initials("x")).toBe("X")
  })
})
