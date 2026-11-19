package relay

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-msgauth/dmarc"

	"hermex/internal/tlsrpt"
)

// TestAPanickingDeliveryIsHeldNotLost queues mail for two domains and makes
// delivery to one of them panic. The relay runs in a background loop with no
// handler above it, so an unrecovered panic would end the MTA and, because the
// recipient stays queued, end it again on every restart. The fault is in this
// server, so the message must not be lost either: it is held on the queue with no
// attempt counted and no bounce, later passes leave it alone however much time
// passes, the other recipient is delivered, and an administrator's retry sends it.
func TestAPanickingDeliveryIsHeldNotLost(t *testing.T) {
	sink, addr := startSink(t)
	sp := openSpool(t)
	t0 := time.Unix(3_000_000, 0)
	raw := []byte("From: alice@local\r\nSubject: out\r\n\r\nhi\r\n")
	mustNoErr(t, sp.Enqueue("alice@local", []string{"carol@poison.test", "bob@remote"}, raw, t0), "enqueue")

	broken := true
	bounces, poisonTries := 0, 0
	w := &Worker{
		Spool:       sp,
		HeloName:    "mx.test",
		MaxAttempts: 1,
		Router: func(domain string) ([]string, error) {
			if domain == "poison.test" && broken {
				poisonTries++
				panic("resolver bug")
			}
			return []string{"sink"}, nil
		},
		Dialer:   func(string) (net.Conn, error) { return net.Dial("tcp", addr) },
		OnGiveUp: func(Item, error) error { bounces++; return nil },
	}

	sent, err := w.ProcessDue(context.Background(), t0)
	mustNoErr(t, err, "the pass")
	wantEq(t, sent, 1, "recipients delivered")
	wantEq(t, len(sink.recorded()), 1, "messages the reachable recipient received")

	// A week of later passes neither retries nor bounces the held recipient.
	sent, err = w.ProcessDue(context.Background(), t0.Add(7*24*time.Hour))
	mustNoErr(t, err, "a later pass")
	wantEq(t, sent, 0, "recipients delivered by a later pass")
	wantEq(t, bounces, 0, "bounces sent for a fault inside this server")
	wantEq(t, poisonTries, 1, "delivery attempts made to the held recipient")

	queued, err := sp.List()
	mustNoErr(t, err, "list the queue")
	if len(queued) != 1 || queued[0].Recipient != "carol@poison.test" {
		t.Fatalf("queue = %+v, want only the recipient whose delivery panicked", queued)
	}
	q := queued[0]
	wantEq(t, q.Attempts, 0, "attempts counted for the held recipient")
	if !q.Held || q.Interrupted || !strings.Contains(q.LastError, "panicked") {
		t.Errorf("queued row = %+v, want it held, recording the panic", q)
	}

	// Once the fault is fixed, the administrator's retry delivers it.
	broken = false
	later := t0.Add(8 * 24 * time.Hour)
	mustNoErr(t, sp.RetryNow(q.RecipientID, later), "retry the held recipient")
	sent, err = w.ProcessDue(context.Background(), later)
	mustNoErr(t, err, "the pass after the retry")
	wantEq(t, sent, 1, "recipients delivered after the retry")
	wantEq(t, len(sink.recorded()), 2, "messages received once the held one went out")
}

// TestAPanickingReportFailsOnlyItsDomain makes the lookup of a policy domain's
// DMARC record panic during the daily report pass. The pass must return, and the
// domain must be marked reported for that day, so the next pass does not run into
// the same panic again.
func TestAPanickingReportFailsOnlyItsDomain(t *testing.T) {
	sp := openSpool(t)
	seedDMARC(t, sp)
	w := dmarcWorker(sp, "")
	w.DMARCLookup = func(string) (*dmarc.Record, error) { panic("record parser bug") }

	mustNoErr(t, w.sendDMARCReports(context.Background(), reportDay), "the report pass")
	left, err := sp.UnreportedDMARCDomains(reportDay)
	mustNoErr(t, err, "list unreported domains")
	wantEq(t, len(left), 0, "domains still waiting for a report after the panic")
}

// TestAPanickingTLSReportFailsOnlyItsDomain is the TLS-RPT half: the policy lookup
// of one domain panics, the other domain's report is still queued, and both are
// marked reported for the day.
func TestAPanickingTLSReportFailsOnlyItsDomain(t *testing.T) {
	sp := openSpool(t)
	seedCounters(t, sp, "poison.test")
	seedCounters(t, sp, "example.test")
	w := &Worker{
		Spool: sp,
		TLSResolver: &tlsrpt.Resolver{LookupTXT: func(name string) ([]string, error) {
			if strings.HasSuffix(name, "poison.test") {
				panic("txt parser bug")
			}
			return []string{"v=TLSRPTv1; rua=mailto:tls-reports@example.test"}, nil
		}},
		ReportOrg:    "hermex.test",
		ReportDomain: "hermex.test",
	}

	mustNoErr(t, w.sendTLSReports(context.Background(), reportDay), "the report pass")
	queued, err := sp.List()
	mustNoErr(t, err, "list the queue")
	if len(queued) != 1 || queued[0].Recipient != "tls-reports@example.test" {
		t.Errorf("queue = %+v, want the one report of the domain that did not panic", queued)
	}
	left, err := sp.UnreportedDomains(reportDay)
	mustNoErr(t, err, "list unreported domains")
	wantEq(t, len(left), 0, "domains still waiting for a report after the panic")
}
