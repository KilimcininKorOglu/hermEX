package fetchmail

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"hermex/internal/directory"
)

// Store is the configuration and dedup state the worker reads: the active poll entries and,
// for kept POP3 accounts, the source ids already delivered.
type Store interface {
	ListActiveFetchmail() ([]directory.FetchmailEntry, error)
	FetchmailSeen(configID int64) (map[string]bool, error)
	MarkFetchmailSeen(configID int64, uids []string) error
}

// Deliverer hands one fetched message to local delivery for the given mailbox.
type Deliverer func(mailbox string, raw []byte, received time.Time) error

// Poll runs one fetch cycle over every active configuration. It returns the number of
// messages delivered and a per-config error for each account that failed — one failing
// source never stops the others.
func Poll(s Store, deliver Deliverer, now time.Time) (int, []error) {
	configs, err := s.ListActiveFetchmail()
	if err != nil {
		return 0, []error{err}
	}
	total := 0
	var errs []error
	for _, cfg := range configs {
		n, err := pollConfig(s, deliver, cfg, now)
		total += n
		if err != nil {
			errs = append(errs, fmt.Errorf("fetchmail %s@%s: %w", cfg.SrcUser, cfg.SrcServer, err))
		}
	}
	return total, errs
}

// pollConfig dispatches one configuration to its protocol handler.
func pollConfig(s Store, deliver Deliverer, cfg directory.FetchmailEntry, now time.Time) (int, error) {
	switch strings.ToUpper(cfg.Protocol) {
	case "POP3":
		return pollPOP3(s, deliver, cfg, now)
	case "IMAP":
		return pollIMAP(deliver, cfg, now)
	default:
		return 0, fmt.Errorf("unknown protocol %q", cfg.Protocol)
	}
}

// pollPOP3 fetches new mail from a POP3 source. A kept account skips ids recorded in the
// seen store (unless fetchall) and records the newly delivered ones; a non-kept account
// deletes each message after delivery, so the source itself prevents re-fetching.
func pollPOP3(s Store, deliver Deliverer, cfg directory.FetchmailEntry, now time.Time) (int, error) {
	c, err := dialPOP3(cfg.SrcServer, cfg.SrcPort, cfg.UseSSL, cfg.SSLVerify)
	if err != nil {
		return 0, err
	}
	defer c.quit()
	if err := c.auth(cfg.SrcUser, cfg.SrcPassword); err != nil {
		return 0, err
	}
	uids, err := c.uidl()
	if err != nil {
		return 0, err
	}
	var seen map[string]bool
	if cfg.Keep && !cfg.FetchAll {
		if seen, err = s.FetchmailSeen(cfg.ID); err != nil {
			return 0, err
		}
	}
	delivered := 0
	for _, n := range sortedKeys(uids) {
		uid := uids[n]
		if seen[uid] {
			continue
		}
		raw, err := c.retr(n)
		if err != nil {
			return delivered, err
		}
		if err := deliver(cfg.Mailbox, raw, now); err != nil {
			return delivered, err
		}
		delivered++
		// Record (or delete) immediately after each delivery, never after the loop: a
		// failure on a later message must not re-deliver the ones already handled. This
		// mirrors the IMAP path, which marks each \Seen in place.
		if cfg.Keep {
			if err := s.MarkFetchmailSeen(cfg.ID, []string{uid}); err != nil {
				return delivered, err
			}
		} else if err := c.dele(n); err != nil {
			return delivered, err
		}
	}
	return delivered, nil
}

// pollIMAP fetches new mail from an IMAP source. A kept account searches UNSEEN (or ALL
// with fetchall), delivers, and marks each \Seen so the next poll skips it; a non-kept
// account searches ALL and deletes each after delivery.
func pollIMAP(deliver Deliverer, cfg directory.FetchmailEntry, now time.Time) (int, error) {
	c, err := dialIMAP(cfg.SrcServer, cfg.SrcPort, cfg.UseSSL, cfg.SSLVerify)
	if err != nil {
		return 0, err
	}
	defer c.logout()
	if err := c.login(cfg.SrcUser, cfg.SrcPassword); err != nil {
		return 0, err
	}
	if err := c.selectFolder(cfg.SrcFolder); err != nil {
		return 0, err
	}
	criteria := "UNSEEN"
	if cfg.FetchAll || !cfg.Keep {
		criteria = "ALL"
	}
	uids, err := c.search(criteria)
	if err != nil {
		return 0, err
	}
	delivered := 0
	for _, uid := range uids {
		raw, err := c.fetchBody(uid)
		if err != nil {
			return delivered, err
		}
		if err := deliver(cfg.Mailbox, raw, now); err != nil {
			return delivered, err
		}
		delivered++
		if cfg.Keep {
			if err := c.markSeen(uid); err != nil {
				return delivered, err
			}
		} else if err := c.deleteMessage(uid); err != nil {
			return delivered, err
		}
	}
	return delivered, nil
}

// sortedKeys returns the map's integer keys in ascending order, so POP3 messages are
// fetched in a deterministic message-number order.
func sortedKeys(m map[int]string) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}
