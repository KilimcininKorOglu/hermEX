import { describe, expect, it } from "vitest"
import type { FilterAction, FilterCondition, FilterInput } from "@/utils/api"
import { draftOf, emptyAction, emptyCondition, toggledInput, validateDraft } from "./filterDraft"

const cond = (patch: Partial<FilterCondition>): FilterCondition => ({ ...emptyCondition(), value: "x", ...patch })
const draft = (patch: Partial<FilterInput>): FilterInput => ({
  name: "Rule",
  enabled: true,
  matchAll: true,
  conditions: [cond({})],
  exceptions: [],
  actions: [{ type: "markRead" }],
  ...patch,
})
const withAction = (a: FilterAction) => draft({ actions: [a] })

describe("validateDraft", () => {
  it("accepts a complete draft", () => {
    expect(validateDraft(draft({}))).toBeNull()
  })

  it("requires a name, a condition and an action, in that order", () => {
    expect(validateDraft(draft({ name: "  ", conditions: [], actions: [] }))).toBe("filters.validation.nameRequired")
    expect(validateDraft(draft({ conditions: [], actions: [] }))).toBe("filters.validation.conditionRequired")
    expect(validateDraft(draft({ actions: [] }))).toBe("filters.validation.actionRequired")
  })

  it("requires a value on a typed condition but not on a flag, oof or level condition", () => {
    expect(validateDraft(draft({ conditions: [cond({ value: " " })] }))).toBe("filters.validation.conditionValue")
    for (const field of ["flag", "oof", "importance", "sensitivity"] as const) {
      expect(validateDraft(draft({ conditions: [cond({ field, value: "" })] }))).toBeNull()
    }
  })

  it("requires a header name on a header condition, and checks exceptions too", () => {
    expect(validateDraft(draft({ conditions: [cond({ field: "header" })] }))).toBe("filters.validation.headerName")
    expect(validateDraft(draft({ exceptions: [cond({ value: "" })] }))).toBe("filters.validation.conditionValue")
  })

  it("reports a row problem before a missing action", () => {
    expect(validateDraft(draft({ conditions: [cond({ value: "" })], actions: [] }))).toBe("filters.validation.conditionValue")
  })

  it.each<[FilterAction, string]>([
    [{ type: "moveToFolder", target: " " }, "filters.validation.targetFolder"],
    [{ type: "copyToFolder" }, "filters.validation.targetFolder"],
    [{ type: "forward" }, "filters.validation.destinationAddress"],
    [{ type: "forwardAsAttachment" }, "filters.validation.destinationAddress"],
    [{ type: "redirect", forwardTo: "" }, "filters.validation.destinationAddress"],
    [{ type: "reject" }, "filters.validation.rejectMessage"],
    [{ type: "addHeader", headerName: "X-A" }, "filters.validation.addHeaderFields"],
    [{ type: "addHeader", headerValue: "v" }, "filters.validation.addHeaderFields"],
    [{ type: "deleteHeader" }, "filters.validation.deleteHeaderName"],
    [{ type: "flag" }, "filters.validation.flagName"],
  ])("rejects the incomplete action %o", (action, key) => {
    expect(validateDraft(withAction(action))).toBe(key)
  })

  it.each<FilterAction>([
    { type: "moveToFolder", target: "Archive" },
    { type: "forward", forwardTo: "bob@hermex.test" },
    { type: "addHeader", headerName: "X-A", headerValue: "v" },
    { type: "vacation" },
    { type: "categorize" },
    { type: "delete" },
    { type: "stop" },
  ])("accepts the action %o", (action) => {
    expect(validateDraft(withAction(action))).toBeNull()
  })

  it("reports the first incomplete action", () => {
    expect(validateDraft(draft({ actions: [{ type: "reject" }, { type: "flag" }] }))).toBe("filters.validation.rejectMessage")
  })
})

describe("draftOf", () => {
  it("gives an empty rule one blank condition and action row", () => {
    const d = draftOf({ id: "f1", name: "R", enabled: false, matchAll: false, conditions: [], actions: [] } as never)
    expect(d).toEqual({ name: "R", enabled: false, matchAll: false, conditions: [emptyCondition()], exceptions: [], actions: [emptyAction()] })
  })
})

describe("toggledInput", () => {
  it("flips enabled and sends every other field back, exceptions included", () => {
    const filter = {
      id: "f1", name: "R", enabled: true, matchAll: false, priority: 2,
      conditions: [cond({})], exceptions: [cond({ value: "skip" })], actions: [{ type: "markRead" as const }],
    }
    expect(toggledInput(filter)).toEqual({
      name: "R", enabled: false, matchAll: false,
      conditions: filter.conditions, exceptions: filter.exceptions, actions: filter.actions,
    })
  })
})
