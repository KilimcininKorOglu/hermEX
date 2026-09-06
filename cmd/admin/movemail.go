package main

import (
	"fmt"
	"log"
	"strconv"
	"strings"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// wellKnownFolders are the folders an operator names on the command line. The
// numeric id is accepted too, for a folder the user created, which has no name
// that is stable across languages.
var wellKnownFolders = map[string]int64{
	"inbox":    int64(mapi.PrivateFIDInbox),
	"sent":     int64(mapi.PrivateFIDSentItems),
	"drafts":   int64(mapi.PrivateFIDDraft),
	"junk":     int64(mapi.PrivateFIDJunk),
	"spam":     int64(mapi.PrivateFIDJunk),
	"trash":    int64(mapi.PrivateFIDDeletedItems),
	"deleted":  int64(mapi.PrivateFIDDeletedItems),
	"outbox":   int64(mapi.PrivateFIDOutbox),
	"archive":  int64(mapi.PrivateFIDArchive),
	"calendar": int64(mapi.PrivateFIDCalendar),
	"contacts": int64(mapi.PrivateFIDContacts),
	"tasks":    int64(mapi.PrivateFIDTasks),
	"notes":    int64(mapi.PrivateFIDNotes),
}

// resolveFolderArg turns a folder argument into a folder id. A name is matched
// case-insensitively against the well-known set; anything else must be a number,
// because a user-created folder has no name this tool can rely on.
func resolveFolderArg(arg string) (int64, error) {
	if fid, ok := wellKnownFolders[strings.ToLower(strings.TrimSpace(arg))]; ok {
		return fid, nil
	}
	fid, err := strconv.ParseInt(strings.TrimSpace(arg), 10, 64)
	if err != nil || fid <= 0 {
		return 0, fmt.Errorf("folder %q: name a well-known folder (inbox, sent, drafts, junk, trash, ...) or give its numeric id", arg)
	}
	return fid, nil
}

// runMoveMail moves messages between two folders of one mailbox, or copies them
// when the last argument is --copy.
//
// The operator's only way to relocate a message was the quarantine release in
// the panel, which moves from Junk to Inbox and nowhere else. This is the
// general form: recovering mail a rule filed wrongly, or emptying a folder that
// is about to be deleted, without asking the user to do it from their client.
func runMoveMail(c *cmdContext) {
	args, copyOnly := c.args[1:], false
	if n := len(args); n > 0 && args[n-1] == "--copy" {
		args, copyOnly = args[:n-1], true
	}
	// The table's minimum counts --copy, so the stripped list is checked here.
	if len(args) < 4 {
		log.Fatal("hermex-admin: move-mail needs a mailbox, a source folder, a destination folder and at least one message id")
	}
	target := args[0]
	src, dst := folderPair(args[1], args[2])
	uids, err := parseUIDs(args[3:])
	if err != nil {
		log.Fatalf("hermex-admin: %v", err)
	}

	maildir, ok := c.dir.Resolve(target)
	if !ok {
		log.Fatalf("hermex-admin: unknown or unreceivable mailbox: %s", target)
	}
	// OpenExisting, not Open: a typo in the address must not provision an empty
	// mailbox and then report that it moved nothing.
	store, err := objectstore.OpenExisting(maildir)
	if err != nil {
		log.Fatalf("hermex-admin: open mailbox %s: %v", maildir, err)
	}
	defer store.Close()

	reportRelocation(relocateAll(store, src, dst, uids, copyOnly), len(uids), target, copyOnly)
}

// relocateAll relocates every message and returns how many succeeded. One
// message that cannot be moved must not abandon the rest: a run that relocates
// all but one is worth more than one that stops.
func relocateAll(store *objectstore.Store, src, dst int64, uids []uint32, copyOnly bool) int {
	moved := 0
	for _, uid := range uids {
		if err := relocate(store, src, dst, uid, copyOnly); err != nil {
			log.Printf("hermex-admin: uid %d: %v", uid, err)
			continue
		}
		moved++
	}
	return moved
}

// reportRelocation prints the count and fails the run when any message was left
// behind, because the exit status is what a script checks and a partial run must
// not read as a clean one.
func reportRelocation(moved, total int, target string, copyOnly bool) {
	verb := "moved"
	if copyOnly {
		verb = "copied"
	}
	fmt.Printf("%s %d of %d message(s) in %s\n", verb, moved, total, target)
	if moved < total {
		log.Fatal("hermex-admin: some messages were not relocated")
	}
}

// folderPair resolves the two folder arguments and refuses a pair that names one
// folder twice, which would move a message onto itself.
func folderPair(srcArg, dstArg string) (int64, int64) {
	src, err := resolveFolderArg(srcArg)
	if err != nil {
		log.Fatalf("hermex-admin: %v", err)
	}
	dst, err := resolveFolderArg(dstArg)
	if err != nil {
		log.Fatalf("hermex-admin: %v", err)
	}
	if src == dst {
		log.Fatal("hermex-admin: the source and destination folders are the same")
	}
	return src, dst
}

// relocate moves or copies one message.
func relocate(store *objectstore.Store, src, dst int64, uid uint32, copyOnly bool) error {
	if copyOnly {
		info, err := store.MessageByUID(src, uid)
		if err != nil {
			return err
		}
		raw, err := store.GetMessageRaw(src, uid)
		if err != nil {
			return err
		}
		_, err = store.AppendMessage(dst, raw, info.InternalDate, info.Flags)
		return err
	}
	_, err := store.MoveMessage(src, uid, dst)
	return err
}

// parseUIDs turns the message-id arguments into uids. Each is a separate
// argument, the way every other list this tool takes is written.
func parseUIDs(args []string) ([]uint32, error) {
	uids := make([]uint32, 0, len(args))
	for _, a := range args {
		n, err := strconv.ParseUint(strings.TrimSpace(a), 10, 32)
		if err != nil || n == 0 {
			return nil, fmt.Errorf("message id %q: want a positive whole number", a)
		}
		uids = append(uids, uint32(n))
	}
	return uids, nil
}
