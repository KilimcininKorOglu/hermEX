// Package itip builds the email that carries an iTIP scheduling message (RFC 5546)
// as iMIP (RFC 6047): a mail from the originator to the recipients whose body is a
// text alternative plus the text/calendar part with its METHOD. Every surface that
// sends a meeting request, update, cancellation or counter proposal builds it here,
// so the MIME always comes from oxcmail.Export and never from a hand-rolled writer.
package itip

import (
	"strings"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
)

// Mail is one scheduling message to build. Calendar is the complete iCalendar
// object and must already carry the METHOD that Method names, because a receiving
// client reads the method from both places and they must agree. Text is the plain
// alternative a client without calendar support shows; it may be empty.
type Mail struct {
	From     string
	To       []string
	Subject  string
	Text     string
	Calendar []byte
	Method   string
}

// Message renders m as a complete RFC 5322 message, stamped with a Message-ID and
// the current time as its Date. A recipient given as a calendar address
// ("mailto:x") is addressed by its plain form.
func Message(m Mail) ([]byte, error) {
	msg := &oxcmail.Message{Props: mapi.PropertyValues{
		{Tag: mapi.PrSubject, Value: m.Subject},
		{Tag: mapi.PrSenderSmtpAddress, Value: m.From},
		{Tag: mapi.PrSenderEmailAddress, Value: m.From},
		{Tag: mapi.PrSenderAddrType, Value: "SMTP"},
		{Tag: mapi.PrClientSubmitTime, Value: mapi.UnixToNTTime(time.Now())},
	}}
	if m.Text != "" {
		msg.Props.Set(mapi.PrBody, m.Text)
	}
	for _, rcpt := range m.To {
		msg.Recipients = append(msg.Recipients, mapi.PropertyValues{
			{Tag: mapi.PrRecipientType, Value: int32(mapi.RecipTo)},
			{Tag: mapi.PrSmtpAddress, Value: plainAddress(rcpt)},
		})
	}
	oxcmail.EnsureMessageID(&msg.Props)
	return oxcmail.Export(msg, oxcmail.Options{CalendarBody: m.Calendar, CalendarMethod: m.Method})
}

// plainAddress drops the mailto: scheme a calendar address carries.
func plainAddress(addr string) string {
	if len(addr) >= 7 && strings.EqualFold(addr[:7], "mailto:") {
		return addr[7:]
	}
	return addr
}
