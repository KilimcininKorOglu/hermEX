// Reply, reply-all and forward all open the composer through a query string.
// Building it is where the rules live (subject prefixing, who ends up in Cc,
// whether the original is quoted), so it sits here as pure functions the tests
// can drive directly.

// ReplySource is the part of a read message these builders need.
export interface ReplySource {
  from: string
  fromEmail: string
  to: string[]
  subject: string
  date: string
  content: string
}

// QuoteLabels are the localized strings the caller resolves; keeping them as
// arguments leaves these functions free of the i18n context.
export interface QuoteLabels {
  replyHeader: string // already interpolated with sender and date
  forwardedMessage: string
  from: string
  date: string
  subject: string
  to: string
}

// prefixOnce adds a prefix unless the subject already carries it, so a thread
// does not accumulate "Re: Re: Re:".
function prefixOnce(subject: string, prefix: string): string {
  return subject.startsWith(prefix) ? subject : `${prefix}${subject}`
}

// quotedBody is the reply quote: a blank line, the attribution, then the
// original message.
function quotedBody(src: ReplySource, labels: QuoteLabels): string {
  return `\n\n${labels.replyHeader}\n${src.content}`
}

// replyParams builds the composer query string for a reply. omitOriginal drops
// the quote (the user's preference), and nothing else about the reply changes.
export function replyParams(src: ReplySource, labels: QuoteLabels, omitOriginal: boolean): URLSearchParams {
  const params = new URLSearchParams({
    replyTo: src.fromEmail,
    subject: prefixOnce(src.subject, 'Re: '),
  })
  if (!omitOriginal) params.set('body', quotedBody(src, labels))
  return params
}

// replyAllParams builds the reply-all query string. The other To recipients
// become Cc, minus the original sender (already the reply target) and the user
// themselves, so replying to all does not mail the account back a copy.
export function replyAllParams(
  src: ReplySource,
  labels: QuoteLabels,
  self: string | undefined,
  omitOriginal: boolean,
): URLSearchParams {
  const me = self?.toLowerCase()
  const others = src.to
    .map((t) => {
      const m = t.match(/<([^>]+)>/) // "Name <addr>" carries the address in brackets
      return (m ? m[1] : t).trim()
    })
    .filter((e) => e && e.toLowerCase() !== me && e.toLowerCase() !== src.fromEmail.toLowerCase())

  const params = replyParams(src, labels, omitOriginal)
  if (others.length > 0) params.set('cc', others.join(','))
  return params
}

// forwardParams builds the forward query string: the full original with its
// headers repeated in the body, and no recipient, since the user picks one.
export function forwardParams(src: ReplySource, labels: QuoteLabels): URLSearchParams {
  const quoted =
    `\n\n---------- ${labels.forwardedMessage} ----------\n` +
    `${labels.from}: ${src.from} <${src.fromEmail}>\n` +
    `${labels.date}: ${src.date}\n` +
    `${labels.subject}: ${src.subject}\n` +
    `${labels.to}: ${src.to.join(', ')}\n\n` +
    src.content
  return new URLSearchParams({
    subject: prefixOnce(src.subject, 'Fwd: '),
    body: quoted,
  })
}
