import { describe, it, expect, afterEach } from 'vitest'
import { formatAbsolute, formatDate, formatDay, formatFullDate, formatWhen, setDisplayTimeZone, withTz, zonedInputToISO, zonedInputFromISO, zonedDayKey, zonedDayStartISO, addDaysToKey, weekdayOfKey, eventDayKey } from './date'

describe('without a chosen time zone', () => {
  afterEach(() => setDisplayTimeZone(''))

  it('renders in UTC and labels a full date as UTC', () => {
    setDisplayTimeZone('')
    expect(withTz().timeZone).toBe('UTC')
    const at = '2026-03-28T12:33:00Z'
    expect(formatFullDate(at).endsWith(' UTC')).toBe(true)
    expect(formatAbsolute(at)).toContain('12:33')
    expect(zonedInputToISO('2026-03-28T15:33')).toBe('2026-03-28T15:33:00.000Z')
  })

  it('renders in the chosen zone without a label once one is set', () => {
    setDisplayTimeZone('Europe/Istanbul')
    const at = '2026-03-28T12:33:00Z'
    expect(formatAbsolute(at)).toMatch(/0?3:33|15:33/)
    expect(formatAbsolute(at).endsWith('UTC')).toBe(false)
    expect(zonedInputToISO('2026-03-28T15:33')).toBe('2026-03-28T12:33:00.000Z')
  })
})

describe('formatDate', () => {
  it('returns time for dates less than 24 hours old', () => {
    const now = new Date()
    const oneHourAgo = new Date(now.getTime() - 3600000).toISOString()
    const result = formatDate(oneHourAgo)
    // Should return a time string with colon
    expect(result).toMatch(/^\d{1,2}:\d{2}/)
  })

  it('returns short string for dates less than 7 days old', () => {
    const now = new Date()
    const twoDaysAgo = new Date(now.getTime() - 2 * 86400000).toISOString()
    const result = formatDate(twoDaysAgo)
    // Should return a short string (weekday name)
    expect(result.length).toBeLessThanOrEqual(4)
  })

  it('returns month and day for dates older than 7 days', () => {
    const tenDaysAgo = new Date(Date.now() - 10 * 86400000).toISOString()
    const result = formatDate(tenDaysAgo)
    // Should contain a space and a number (some locale format)
    expect(result).toMatch(/\s/)
    expect(result).toMatch(/\d/)
  })
})

describe('formatFullDate', () => {
  it('returns full date and time string', () => {
    const dateStr = '2024-04-15T14:30:00Z'
    const result = formatFullDate(dateStr)
    // Should contain the year
    expect(result).toContain('2024')
  })

  it('handles dates correctly', () => {
    const date = '2024-01-01T12:00:00Z' // Jan 1, 2024 in UTC, the zone shown when none is chosen
    const result = formatFullDate(date)
    // Should contain year and day
    expect(result).toContain('2024')
    expect(result).toContain('1')
  })
})

describe('display-zone inputs', () => {
  afterEach(() => setDisplayTimeZone(''))

  it('reads an instant back as the wall clock the page shows, both ways', () => {
    setDisplayTimeZone('America/New_York')
    expect(zonedInputFromISO('2026-11-26T09:00:00Z')).toBe('2026-11-26T04:00')
    expect(zonedInputToISO('2026-11-26T04:00')).toBe('2026-11-26T09:00:00.000Z')
    // Across the DST switch the offset follows the day.
    expect(zonedInputFromISO('2026-07-01T09:00:00Z')).toBe('2026-07-01T05:00')
    setDisplayTimeZone('')
    expect(zonedInputFromISO('2026-11-26T09:00:00Z')).toBe('2026-11-26T09:00')
  })

  it('finds the day an instant falls on, and the day boundaries, in the zone', () => {
    setDisplayTimeZone('Europe/Istanbul')
    expect(zonedDayKey('2026-11-26T22:30:00Z')).toBe('2026-11-27')
    expect(zonedDayStartISO('2026-11-27')).toBe('2026-11-26T21:00:00.000Z')
    expect(addDaysToKey('2026-12-31', 1)).toBe('2027-01-01')
    expect(weekdayOfKey('2026-11-27')).toBe(5)
    expect(zonedInputFromISO('nope')).toBe('')
  })

  it('puts a timed event on its display-zone day and an all-day event on its own day', () => {
    setDisplayTimeZone('America/New_York')
    expect(eventDayKey({ start: '2026-11-27T02:00:00Z' })).toBe('2026-11-26')
    expect(eventDayKey({ start: '2026-11-27', allDay: true })).toBe('2026-11-27')
    setDisplayTimeZone('Europe/Istanbul')
    expect(eventDayKey({ start: '2026-11-26T22:30:00Z' })).toBe('2026-11-27')
  })
})

describe('formatWhen and formatDay', () => {
  const t = (key: string, params?: Record<string, string>) => `${key}:${params?.n ?? ''}`
  const now = Date.parse('2026-03-28T12:00:00Z')
  afterEach(() => {
    setDisplayTimeZone('')
    document.documentElement.lang = 'en'
  })

  it('writes a time under a day away as a distance', () => {
    expect(formatWhen(now - 300, t, now)).toBe('time.justNow:')
    expect(formatWhen(now - 20000, t, now)).toBe('time.secondsAgo:20')
    expect(formatWhen(now - 5 * 60000, t, now)).toBe('time.minutesAgo:5')
    expect(formatWhen(now - 23 * 3600000, t, now)).toBe('time.hoursAgo:23')
    expect(formatWhen(now + 4 * 60000, t, now)).toBe('time.inMinutes:4')
  })

  it('writes a time a day or more away as its date in the zone and language', () => {
    setDisplayTimeZone('Europe/Istanbul')
    document.documentElement.lang = 'tr'
    expect(formatWhen('2026-03-26T12:33:00Z', t, now)).toBe('26.03.2026 15:33')
    document.documentElement.lang = 'en'
    expect(formatWhen('2026-03-26T12:33:00Z', t, now)).toBe('03/26/2026 3:33 PM')
    setDisplayTimeZone('')
    expect(formatWhen('2026-03-20T00:05:00Z', t, now)).toBe('03/20/2026 12:05 AM UTC')
  })

  it('writes a date-only value as that day in every zone', () => {
    setDisplayTimeZone('America/New_York')
    document.documentElement.lang = 'tr'
    expect(formatDay('2026-03-28')).toBe('28.03.2026')
    expect(formatWhen('garbage', t, now)).toBe('garbage')
  })
})
