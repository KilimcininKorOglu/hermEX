import type { Filter, FilterAction, FilterCondition, FilterInput } from "@/utils/api"

// Condition fields that take no value: a flag/out-of-office test, or an importance/
// sensitivity level chosen from a fixed list rather than typed.
export const VALUELESS_FIELDS = new Set<FilterCondition["field"]>(["flag", "oof"])
export const LEVEL_FIELDS = new Set<FilterCondition["field"]>(["importance", "sensitivity"])

export function emptyCondition(): FilterCondition {
  return { field: "from", operator: "contains", value: "" }
}

export function emptyAction(): FilterAction {
  return { type: "moveToFolder", target: "" }
}

export function emptyDraft(): FilterInput {
  return {
    name: "",
    enabled: true,
    matchAll: true,
    conditions: [emptyCondition()],
    exceptions: [],
    actions: [emptyAction()],
  }
}

// draftOf fills the editor from a stored filter; an empty condition or action
// list gets one blank row so the editor always shows something to fill in.
export function draftOf(filter: Filter): FilterInput {
  return {
    name: filter.name,
    enabled: filter.enabled,
    matchAll: filter.matchAll,
    conditions: filter.conditions.length ? filter.conditions : [emptyCondition()],
    exceptions: filter.exceptions ?? [],
    actions: filter.actions.length ? filter.actions : [emptyAction()],
  }
}

// toggledInput is the update body that flips a filter's enabled state. The
// update handler replaces the whole stored filter, so every other field,
// exceptions included, is sent back unchanged.
export function toggledInput(filter: Filter): FilterInput {
  return {
    name: filter.name,
    enabled: !filter.enabled,
    matchAll: filter.matchAll,
    conditions: filter.conditions,
    exceptions: filter.exceptions,
    actions: filter.actions,
  }
}

const blank = (s?: string): boolean => !s?.trim()

// conditionError returns the i18n key of the first problem in one condition row.
// flag/out-of-office take no value; importance/sensitivity default to a level.
function conditionError(c: FilterCondition): string | null {
  const typed = !VALUELESS_FIELDS.has(c.field) && !LEVEL_FIELDS.has(c.field)
  if (typed && blank(c.value)) return "filters.validation.conditionValue"
  if (c.field === "header" && blank(c.headerName)) return "filters.validation.headerName"
  return null
}

// ACTION_CHECKS names, per action type, the field that must be filled and the
// i18n key reported when it is not.
const ACTION_CHECKS: Partial<Record<FilterAction["type"], { missing: (a: FilterAction) => boolean; key: string }>> = {
  moveToFolder: { missing: (a) => blank(a.target), key: "filters.validation.targetFolder" },
  copyToFolder: { missing: (a) => blank(a.target), key: "filters.validation.targetFolder" },
  forward: { missing: (a) => blank(a.forwardTo), key: "filters.validation.destinationAddress" },
  forwardAsAttachment: { missing: (a) => blank(a.forwardTo), key: "filters.validation.destinationAddress" },
  redirect: { missing: (a) => blank(a.forwardTo), key: "filters.validation.destinationAddress" },
  reject: { missing: (a) => blank(a.message), key: "filters.validation.rejectMessage" },
  addHeader: { missing: (a) => blank(a.headerName) || blank(a.headerValue), key: "filters.validation.addHeaderFields" },
  deleteHeader: { missing: (a) => blank(a.headerName), key: "filters.validation.deleteHeaderName" },
  flag: { missing: (a) => blank(a.flagName), key: "filters.validation.flagName" },
}

// actionError returns the i18n key of the problem in one action row, if any.
function actionError(a: FilterAction): string | null {
  const check = ACTION_CHECKS[a.type]
  return check?.missing(a) ? check.key : null
}

// firstError returns the first non-null result of check over items.
function firstError<T>(items: T[], check: (item: T) => string | null): string | null {
  for (const item of items) {
    const err = check(item)
    if (err) return err
  }
  return null
}

// validateDraft returns the i18n key of the first problem that stops a draft
// from being saved, or null. Exceptions are optional, but any present row must
// be as complete as a condition.
export function validateDraft(draft: FilterInput): string | null {
  if (blank(draft.name)) return "filters.validation.nameRequired"
  if (draft.conditions.length === 0) return "filters.validation.conditionRequired"
  const rowError = firstError([...draft.conditions, ...(draft.exceptions ?? [])], conditionError)
  if (rowError) return rowError
  if (draft.actions.length === 0) return "filters.validation.actionRequired"
  return firstError(draft.actions, actionError)
}
