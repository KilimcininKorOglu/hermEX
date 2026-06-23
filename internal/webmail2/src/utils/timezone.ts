// Shared IANA timezone helpers used by onboarding and settings.

// detectTimeZone returns the device's IANA timezone, falling back to UTC.
export function detectTimeZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC"
  } catch {
    return "UTC"
  }
}

// listTimeZones returns the IANA zones the runtime knows, falling back to a
// small curated set on engines without Intl.supportedValuesOf.
export function listTimeZones(): string[] {
  const intl = Intl as unknown as { supportedValuesOf?: (key: string) => string[] }
  if (typeof intl.supportedValuesOf === "function") {
    try {
      return intl.supportedValuesOf("timeZone")
    } catch {
      // fall through to the curated list
    }
  }
  return [
    "UTC",
    "Europe/Istanbul",
    "Europe/London",
    "Europe/Berlin",
    "America/New_York",
    "America/Chicago",
    "America/Los_Angeles",
    "Asia/Tokyo",
    "Asia/Shanghai",
    "Asia/Dubai",
    "Australia/Sydney",
  ]
}
