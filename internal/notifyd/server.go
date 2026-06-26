// Package notifyd is the central push-notification relay: a tiny in-memory SSE
// fan-out that carries mailbox-change WAKE signals from the writer daemons (the
// MTA, IMAP, DAV, …) to the long-poll consumers (MAPI/HTTP NotificationWait, EAS
// Ping, EWS streaming, IMAP IDLE). A producer POSTs one Event to /publish; every
// consumer holding a /events stream receives it and wakes the matching mailbox's
// long-poll, which then runs its own authoritative diff.
//
// The relay is deliberately dumb and best-effort. It holds no per-mailbox
// subscription state — it broadcasts every event to every consumer, which filters
// locally. A slow consumer's buffer overflows into dropped events rather than
// back-pressure on the producer; the consumer's poll cadence is the floor that
// catches anything dropped. Nothing here is on the mail-delivery path: if this
// daemon is down, producers fail their publish silently and consumers fall back to
// polling, so mail still flows.
package notifyd

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"sync"

	"hermex/internal/logging"
)

// consumerBuffer is the per-consumer event queue depth. A consumer that cannot
// drain this fast (its long-poll is mid-diff, the network stalled) drops further
// events until it catches up — the consumer's poll cadence covers the gap, so a
// dropped wake costs at most one cadence interval, never a missed change.
const consumerBuffer = 256

// Event is one mailbox-change wake. It is the wire shape relayed verbatim from a
// producer's objectstore.ChangeEvent to the consumers; the notify daemon links
// neither package, so the shape is duplicated here as the contract between them.
type Event struct {
	Mailbox string `json:"mailbox"` // the per-mailbox store directory (store.Dir()) — the key a consumer matches against its active long-polls
	Op      string `json:"op"`      // the mutation kind (create|modify|flags|delete|folder|…): observability and optional consumer-side pre-filtering only
	CN      uint64 `json:"cn"`      // the change number stamped on the write; 0 for a hard delete, which carries no CN
	Mid     string `json:"mid"`     // the message id for a delete that bumped no CN; empty otherwise
}

// Server is the in-memory SSE relay. subs is the set of connected consumers, each
// a buffered channel the publish handler fans out to; secret guards both endpoints
// (empty disables the check, for dev).
type Server struct {
	secret string
	logger *logging.Logger

	mu   sync.Mutex
	subs map[chan Event]struct{}
}

// New builds a relay authenticated by secret (empty = no auth). logger may be nil.
func New(secret string, logger *logging.Logger) *Server {
	return &Server{secret: secret, logger: logger, subs: make(map[chan Event]struct{})}
}

// Handler routes the two relay endpoints: producers POST /publish, consumers hold
// a GET /events SSE stream.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/publish", s.handlePublish)
	mux.HandleFunc("/events", s.handleEvents)
	return mux
}

// authorized reports whether the request carries the shared bearer secret. An
// empty configured secret disables the check (dev only). The compare is
// constant-time so the endpoint does not leak the secret through timing.
func (s *Server) authorized(r *http.Request) bool {
	if s.secret == "" {
		return true
	}
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if len(h) <= len(prefix) || h[:len(prefix)] != prefix {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(h[len(prefix):]), []byte(s.secret)) == 1
}

// handlePublish accepts one Event from a producer and fans it out to every
// connected consumer without blocking — a consumer whose buffer is full drops the
// event (best-effort relay). It always answers fast (204) so a producer's publish
// never stalls a write path.
func (s *Server) handlePublish(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var ev Event
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&ev); err != nil {
		http.Error(w, "bad event", http.StatusBadRequest)
		return
	}
	s.broadcast(ev)
	w.WriteHeader(http.StatusNoContent)
}

// broadcast delivers ev to every consumer channel without blocking on any one of
// them: a full channel is skipped (the consumer's poll cadence is its safety net).
func (s *Server) broadcast(ev Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.subs {
		select {
		case ch <- ev:
		default: // consumer is behind; drop rather than block the publisher
		}
	}
}

// handleEvents holds a Server-Sent-Events stream open for one consumer: it
// registers a buffered channel, then writes each broadcast event as a `data:` line
// and flushes, until the client disconnects. The response is unbounded (no
// Content-Length, no write deadline), so the connection lives for the consumer's
// lifetime.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	rc := http.NewResponseController(w)
	ch := s.register()
	defer s.unregister(ch)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	if err := rc.Flush(); err != nil { // surface the headers immediately so the consumer knows it is connected
		return
	}

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-ch:
			if !writeEvent(w, ev) || rc.Flush() != nil {
				return // client gone
			}
		}
	}
}

// register adds a fresh consumer channel to the fan-out set and returns it.
func (s *Server) register() chan Event {
	ch := make(chan Event, consumerBuffer)
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	n := len(s.subs)
	s.mu.Unlock()
	if s.logger != nil {
		s.logger.Info(logging.Notify, "consumer.connect", logging.Fields{"consumers": n})
	}
	return ch
}

// unregister removes a consumer channel when its stream ends.
func (s *Server) unregister(ch chan Event) {
	s.mu.Lock()
	delete(s.subs, ch)
	n := len(s.subs)
	s.mu.Unlock()
	if s.logger != nil {
		s.logger.Info(logging.Notify, "consumer.disconnect", logging.Fields{"consumers": n})
	}
}

// writeEvent serializes one Event as a single SSE `data:` record. It returns false
// on any write error (the client has gone).
func writeEvent(w http.ResponseWriter, ev Event) bool {
	body, err := json.Marshal(ev)
	if err != nil {
		return true // a malformed event is dropped, not fatal to the stream
	}
	if _, err := w.Write([]byte("data: ")); err != nil {
		return false
	}
	if _, err := w.Write(body); err != nil {
		return false
	}
	_, err = w.Write([]byte("\n\n"))
	return err == nil
}
