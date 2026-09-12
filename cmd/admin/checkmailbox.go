package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"slices"

	"hermex/internal/objectstore"
)

// runCheckMailbox reports what is wrong with one mailbox or with every mailbox,
// and repairs it when asked. The check itself writes nothing, so it is safe on a
// running deployment; both repair modes hold the mailbox exclusively and refuse
// while a daemon has it open.
//
// The exit status is what a scheduled run reads: a mailbox that still carries a
// finding after the requested repairs exits non-zero.
func runCheckMailbox(c *cmdContext) {
	repair := slices.Contains(c.args, "--repair")
	recover := slices.Contains(c.args, "--recover")
	for _, arg := range c.args[2:] {
		if arg != "--repair" && arg != "--recover" {
			log.Fatalf("hermex-admin: unknown option %q; want --repair or --recover", arg)
		}
	}
	maildirs := resolveMaildirs(c.dir, c.args[1])

	var checked, withFindings int
	for _, md := range maildirs {
		findings, ok := checkOneMailbox(md, repair, recover)
		if !ok {
			continue
		}
		checked++
		if len(findings) > 0 {
			withFindings++
		}
	}
	requireOpened(checked, maildirs)
	fmt.Printf("checked %d mailbox(es), %d still carrying a finding\n", checked, withFindings)
	if withFindings > 0 {
		os.Exit(1)
	}
}

// checkOneMailbox checks one mailbox, applies the requested repairs, and re-checks
// so the reported findings are the ones that survived. ok is false when the
// mailbox could not be read at all, which is reported and does not stop the run.
func checkOneMailbox(maildir string, repair, recoverDBs bool) ([]objectstore.Finding, bool) {
	findings, err := objectstore.CheckMailbox(maildir)
	if err != nil {
		log.Printf("hermex-admin: check %s: %v", maildir, err)
		return nil, false
	}
	if !repair && !recoverDBs {
		reportFindings(maildir, findings)
		return findings, true
	}
	if recoverDBs {
		recoverDamaged(maildir, findings)
	}
	if repair {
		repairMailbox(maildir)
	}
	findings, err = objectstore.CheckMailbox(maildir)
	if err != nil {
		log.Printf("hermex-admin: re-check %s: %v", maildir, err)
		return nil, false
	}
	reportFindings(maildir, findings)
	return findings, true
}

// reportFindings prints one line per surviving finding, or a clean line.
func reportFindings(maildir string, findings []objectstore.Finding) {
	if len(findings) == 0 {
		fmt.Printf("%s: ok\n", maildir)
		return
	}
	for _, f := range findings {
		fmt.Printf("%s: %s\n", maildir, f)
	}
}

// recoverDamaged rebuilds every database the check reported as damaged. It says
// per table how many rows were salvaged and how many were lost, because a rebuilt
// database that reads cleanly says nothing about what is no longer in it.
func recoverDamaged(maildir string, findings []objectstore.Finding) {
	var names []string
	for _, f := range findings {
		if f.Kind == objectstore.FindingCorruptDatabase || f.Kind == objectstore.FindingUnreadableDatabase {
			names = append(names, f.File)
		}
	}
	if len(names) == 0 {
		return
	}
	reports, err := objectstore.RecoverMailboxDatabases(maildir, names)
	for _, r := range reports {
		reportRecovery(maildir, r)
	}
	if errors.Is(err, objectstore.ErrMailboxBusy) {
		log.Fatalf("hermex-admin: %s is open by a running daemon or another session; "+
			"stop the mail services for this mailbox and retry", maildir)
	}
	if err != nil {
		log.Printf("hermex-admin: recover %s: %v", maildir, err)
	}
}

// reportRecovery prints what one rebuild salvaged, naming every table that lost
// rows, because a rebuilt database that reads cleanly says nothing about what is
// no longer in it.
func reportRecovery(maildir string, r objectstore.RecoverReport) {
	fmt.Printf("%s: rebuilt %s, damaged file kept at %s, %d row(s) lost\n", maildir, r.Path, r.Backup, r.Lost)
	for _, t := range r.Tables {
		if t.LostRows > 0 || t.Unreadable {
			fmt.Printf("%s:   table %s recovered %d row(s), lost %d, unreadable=%t\n",
				maildir, t.Name, t.Rows, t.LostRows, t.Unreadable)
		}
	}
}

// repairMailbox reconciles the IMAP index with the object store and reclaims
// orphan content, the repairs that lose nothing.
func repairMailbox(maildir string) {
	store, err := objectstore.OpenExisting(maildir)
	if err != nil {
		log.Printf("hermex-admin: open mailbox %s: %v", maildir, err)
		return
	}
	defer func() { _ = store.Close() }()
	report, err := store.RepairMailbox()
	if errors.Is(err, objectstore.ErrMailboxBusy) {
		log.Fatalf("hermex-admin: %s is open by a running daemon or another session; "+
			"stop the mail services for this mailbox and retry", maildir)
	}
	if err != nil {
		log.Printf("hermex-admin: repair %s: %v", maildir, err)
		return
	}
	fmt.Printf("%s: reindexed %d folder(s), reclaimed %d content file(s)\n",
		maildir, report.Folders, report.ContentRemoved)
}
