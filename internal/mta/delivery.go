// Package mta delivers accepted SMTP messages into recipient mailboxes. It
// adapts the protocol-only smtp.Backend to the store, resolving recipients
// through a directory.Accounts and appending each message to the recipient's
// INBOX.
package mta

import (
	"fmt"
	"io"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/smtp"
)

// Backend is an smtp.Backend that delivers to per-recipient mailbox stores.
type Backend struct {
	Accounts directory.Accounts
}

// NewSession implements smtp.Backend.
func (b *Backend) NewSession(string) (smtp.Session, error) {
	return &session{accounts: b.Accounts}, nil
}

type session struct {
	accounts directory.Accounts
	from     string
	targets  []string // resolved mailbox store paths
}

func (s *session) Mail(from string) error {
	s.from = from
	return nil
}

func (s *session) Rcpt(to string) error {
	path, ok := s.accounts.Resolve(to)
	if !ok {
		return fmt.Errorf("relay denied for <%s>", to)
	}
	s.targets = append(s.targets, path)
	return nil
}

func (s *session) Data(r io.Reader) error {
	raw, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	received := time.Now()
	for _, path := range s.targets {
		if err := deliver(path, raw, received); err != nil {
			return err
		}
	}
	return nil
}

func (s *session) Reset()        { s.from = ""; s.targets = nil }
func (s *session) Logout() error { return nil }

// Deliver resolves each recipient address to its local mailbox and appends the
// raw message to that mailbox's INBOX. Addresses with no local mailbox are
// returned as unresolved (there is no outbound relay yet), so callers can
// report partial delivery rather than silently dropping them.
func Deliver(accounts directory.Accounts, recipients []string, raw []byte, received time.Time) (unresolved []string, err error) {
	for _, rcpt := range recipients {
		path, ok := accounts.Resolve(rcpt)
		if !ok {
			unresolved = append(unresolved, rcpt)
			continue
		}
		if err := deliver(path, raw, received); err != nil {
			return unresolved, err
		}
	}
	return unresolved, nil
}

// deliver appends a raw message to the inbox of the mailbox at path. The inbox
// is a built-in folder provisioned when the mailbox is created, so it is
// addressed directly by its fixed id.
func deliver(path string, raw []byte, received time.Time) error {
	st, err := objectstore.Open(path)
	if err != nil {
		return err
	}
	defer st.Close()

	_, err = st.AppendMessage(int64(mapi.PrivateFIDInbox), raw, received, 0)
	return err
}
