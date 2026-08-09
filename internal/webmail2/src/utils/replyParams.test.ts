import { describe, it, expect } from 'vitest'
import { forwardParams, replyAllParams, replyParams, type QuoteLabels, type ReplySource } from './replyParams'

const labels: QuoteLabels = {
  replyHeader: 'On Monday, Alice wrote:',
  forwardedMessage: 'Forwarded message',
  from: 'From',
  date: 'Date',
  subject: 'Subject',
  to: 'To',
}

const mail: ReplySource = {
  from: 'Alice',
  fromEmail: 'alice@example.com',
  to: ['me@hermex.test', 'Bob <bob@example.com>', 'carol@example.com'],
  subject: 'lunch',
  date: 'Mon, 3 Aug 2026 10:00:00 +0000',
  content: '<p>original</p>',
}

describe('replyParams', () => {
  it('addresses the sender and prefixes the subject once', () => {
    const p = replyParams(mail, labels, false)
    expect(p.get('replyTo')).toBe('alice@example.com')
    expect(p.get('subject')).toBe('Re: lunch')

    const again = replyParams({ ...mail, subject: 'Re: lunch' }, labels, false)
    expect(again.get('subject')).toBe('Re: lunch')
  })

  it('quotes the original with its attribution', () => {
    const body = replyParams(mail, labels, false).get('body') ?? ''
    expect(body).toContain('On Monday, Alice wrote:')
    expect(body).toContain('<p>original</p>')
  })

  it('omits the quote when the user asked for no original', () => {
    expect(replyParams(mail, labels, true).get('body')).toBeNull()
  })
})

describe('replyAllParams', () => {
  it('moves the other recipients to Cc, without the sender or the user', () => {
    const cc = replyAllParams(mail, labels, 'me@hermex.test', false).get('cc')
    expect(cc).toBe('bob@example.com,carol@example.com')
  })

  it('matches the user case-insensitively, so a differently cased To still drops out', () => {
    const src = { ...mail, to: ['ME@Hermex.Test', 'bob@example.com'] }
    expect(replyAllParams(src, labels, 'me@hermex.test', false).get('cc')).toBe('bob@example.com')
  })

  it('never puts the original sender in Cc, whatever the To list says', () => {
    const src = { ...mail, to: ['Alice <alice@example.com>', 'bob@example.com'] }
    expect(replyAllParams(src, labels, 'me@hermex.test', false).get('cc')).toBe('bob@example.com')
  })

  it('sets no Cc when nobody else is left', () => {
    const src = { ...mail, to: ['me@hermex.test'] }
    expect(replyAllParams(src, labels, 'me@hermex.test', false).get('cc')).toBeNull()
  })

  it('still carries the reply target and subject', () => {
    const p = replyAllParams(mail, labels, 'me@hermex.test', true)
    expect(p.get('replyTo')).toBe('alice@example.com')
    expect(p.get('subject')).toBe('Re: lunch')
    expect(p.get('body')).toBeNull()
  })
})

describe('forwardParams', () => {
  it('prefixes the subject once and repeats the original headers in the body', () => {
    const p = forwardParams(mail, labels)
    expect(p.get('subject')).toBe('Fwd: lunch')
    const body = p.get('body') ?? ''
    expect(body).toContain('Forwarded message')
    expect(body).toContain('From: Alice <alice@example.com>')
    expect(body).toContain('To: me@hermex.test, Bob <bob@example.com>, carol@example.com')
    expect(body).toContain('<p>original</p>')

    expect(forwardParams({ ...mail, subject: 'Fwd: lunch' }, labels).get('subject')).toBe('Fwd: lunch')
  })

  it('picks no recipient, since forwarding is to someone the user has not chosen yet', () => {
    expect(forwardParams(mail, labels).get('replyTo')).toBeNull()
  })
})
