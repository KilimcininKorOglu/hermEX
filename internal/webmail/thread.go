package webmail

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"hermex/internal/objectstore"
)

// thread is one conversation: the folder messages that belong together by RFC
// 5256 References/In-Reply-To linkage. Messages are ordered oldest-first;
// Latest is the newest member's date, used to order threads against each other.
type thread struct {
	Messages []objectstore.MessageInfo
	Latest   time.Time
}

// buildThreads groups a folder's messages into RFC 5256 conversation threads by
// References/In-Reply-To linkage. It uses union-find over Message-IDs: each
// message is unioned with every ancestor id it cites, so two messages that both
// reference a common ancestor land in one thread even when that ancestor is not
// itself in the folder (deleted, or in another folder). Threads are returned
// newest-activity-first; within a thread, messages are oldest-first.
//
// Subject-based merging (RFC 5256 step 5) is deliberately omitted: it merges
// unrelated mail that happens to share a Subject, which is a common source of
// wrong groupings. Pure References/In-Reply-To linkage satisfies the REFERENCES
// threading contract.
func buildThreads(msgs []objectstore.MessageInfo, headers map[int64]objectstore.ThreadHeaders) []thread {
	uf := &unionFind{parent: make(map[string]string)}
	keyOf := make(map[int64]string, len(msgs))
	for _, m := range msgs {
		h := headers[m.ID]
		key := strings.TrimSpace(h.MessageID)
		if key == "" {
			// No Message-ID: a singleton key so the message is its own thread.
			key = fmt.Sprintf("mid:%d", m.ID)
		}
		keyOf[m.ID] = key
		uf.add(key)
		for anc := range strings.FieldsSeq(h.References + " " + h.InReplyTo) {
			uf.union(key, anc)
		}
	}

	groups := make(map[string][]objectstore.MessageInfo)
	for _, m := range msgs {
		root := uf.find(keyOf[m.ID])
		groups[root] = append(groups[root], m)
	}

	threads := make([]thread, 0, len(groups))
	for _, members := range groups {
		sort.Slice(members, func(i, j int) bool {
			if !members[i].InternalDate.Equal(members[j].InternalDate) {
				return members[i].InternalDate.Before(members[j].InternalDate)
			}
			return members[i].UID < members[j].UID // deterministic tie-break
		})
		threads = append(threads, thread{Messages: members, Latest: members[len(members)-1].InternalDate})
	}

	sort.Slice(threads, func(i, j int) bool {
		if !threads[i].Latest.Equal(threads[j].Latest) {
			return threads[i].Latest.After(threads[j].Latest) // newest activity first
		}
		return lastUID(threads[i]) > lastUID(threads[j]) // deterministic tie-break
	})
	return threads
}

// lastUID is the UID of a thread's newest member (its last, since members are
// oldest-first), used as a deterministic tie-break when two threads share a
// latest-activity time.
func lastUID(t thread) uint32 {
	return t.Messages[len(t.Messages)-1].UID
}

// unionFind is a string-keyed disjoint-set with path compression, used to group
// messages by shared Message-ID linkage.
type unionFind struct{ parent map[string]string }

// add registers a key as its own singleton set if not already present.
func (u *unionFind) add(x string) {
	if _, ok := u.parent[x]; !ok {
		u.parent[x] = x
	}
}

// find returns the set representative of x, registering x if new and compressing
// the path to the root.
func (u *unionFind) find(x string) string {
	if _, ok := u.parent[x]; !ok {
		u.parent[x] = x
		return x
	}
	root := x
	for u.parent[root] != root {
		root = u.parent[root]
	}
	for u.parent[x] != root {
		u.parent[x], x = root, u.parent[x]
	}
	return root
}

// union merges the sets containing a and b.
func (u *unionFind) union(a, b string) {
	ra, rb := u.find(a), u.find(b)
	if ra != rb {
		u.parent[ra] = rb
	}
}
