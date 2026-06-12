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
	"hermex/internal/smtp"
	"hermex/internal/store"
)

const inboxName = "INBOX"

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

// deliver appends a raw message to the INBOX of the mailbox at path, creating
// the INBOX on first delivery.
//
// The lookup-then-create of INBOX is not yet race-safe across concurrent
// deliveries to the same brand-new mailbox; a unique (parent, name) constraint
// will harden it once that concurrency is in play.
func deliver(path string, raw []byte, received time.Time) error {
	st, err := store.Open(path)
	if err != nil {
		return err
	}
	defer st.Close()

	inbox, ok, err := st.FolderByName(nil, inboxName)
	if err != nil {
		return err
	}
	if !ok {
		if inbox, err = st.CreateFolder(nil, inboxName); err != nil {
			return err
		}
	}
	_, err = st.AppendMessage(inbox, raw, received, 0)
	return err
}
