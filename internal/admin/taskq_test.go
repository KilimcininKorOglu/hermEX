package admin

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"hermex/internal/directory"
)

// TestTaskqPageAndStatus proves the Task queue page lists tasks with their result
// and the status endpoint counts pending work.
func TestTaskqPageAndStatus(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		tasks: []directory.TaskInfo{
			{ID: 1, Type: "ldapsync", Status: directory.TaskDone, CreatedBy: "admin@test", Message: "Synced 3 directory entries: 1 created, 2 updated.", CreatedAt: 1000, UpdatedAt: 1005},
			{ID: 2, Type: "ldapsync", Status: directory.TaskPending, CreatedBy: "admin@test"},
		},
	}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)

	resp := authedGET(t, ts, "/admin/ui/taskq", session)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("taskq page = %d, want 200", resp.StatusCode)
	}
	page := string(body)
	if !strings.Contains(page, "ldapsync") || !strings.Contains(page, "1 created, 2 updated") {
		t.Errorf("taskq page missing task rows:\n%s", page)
	}

	resp = authedGET(t, ts, "/admin/tasq/status", session)
	var st struct {
		Running bool
		Active  int
		Pending int
	}
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	resp.Body.Close()
	if st.Pending != 1 {
		t.Errorf("status pending = %d, want 1", st.Pending)
	}
}

// TestTaskqRequiresSystem proves an org admin cannot reach the Task queue page.
func TestTaskqRequiresSystem(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminOrg, ScopeID: 1}}}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)
	resp := authedGET(t, ts, "/admin/ui/taskq", session)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("org-admin taskq page = %d, want 403", resp.StatusCode)
	}
}
