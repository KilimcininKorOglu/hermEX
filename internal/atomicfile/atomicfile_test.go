package atomicfile

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// stepLog records the durability steps in the order they run, and fails the one
// named in failAt.
type stepLog struct {
	steps  []string
	failAt string
}

var errInjected = errors.New("injected failure")

// install replaces the durability steps for one test and restores them after.
func (l *stepLog) install(t *testing.T) {
	t.Helper()
	origSync, origRename, origDir := syncFile, renameFile, syncDir
	t.Cleanup(func() { syncFile, renameFile, syncDir = origSync, origRename, origDir })
	step := func(name string) error {
		l.steps = append(l.steps, name)
		if l.failAt == name {
			return errInjected
		}
		return nil
	}
	syncFile = func(f *os.File) error {
		if err := step("sync-file"); err != nil {
			return err
		}
		return origSync(f)
	}
	renameFile = func(a, b string) error {
		if err := step("rename"); err != nil {
			return err
		}
		return origRename(a, b)
	}
	syncDir = func(d string) error {
		if err := step("sync-dir"); err != nil {
			return err
		}
		return origDir(d)
	}
}

// seed writes the old content a replacement must not lose.
func seed(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "target")
	if err := os.WriteFile(path, []byte("old content"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir, path
}

// read returns a file's content.
func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// leftovers lists the temporary files left in dir.
func leftovers(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if IsTemp(e.Name()) {
			out = append(out, e.Name())
		}
	}
	return out
}

// TestWriteFileSyncsBeforeAndAfterTheRename proves the durability order: the data
// reaches the disk before the rename publishes it, and the directory is synced
// after, so neither a crash nor a power loss can leave a published name pointing
// at unwritten data or lose the rename.
func TestWriteFileSyncsBeforeAndAfterTheRename(t *testing.T) {
	_, path := seed(t)
	var l stepLog
	l.install(t)
	if err := WriteFile(path, []byte("new content"), 0o600); err != nil {
		t.Fatal(err)
	}
	want := []string{"sync-file", "rename", "sync-dir"}
	if len(l.steps) != len(want) {
		t.Fatalf("steps = %v, want %v", l.steps, want)
	}
	for i := range want {
		if l.steps[i] != want[i] {
			t.Fatalf("steps = %v, want %v", l.steps, want)
		}
	}
	if got := read(t, path); got != "new content" {
		t.Errorf("content = %q, want the new content", got)
	}
}

// TestFailedReplacementKeepsTheOldContent fails each step before the rename and
// the write itself, and proves the target still holds its old content in full and
// no temporary file is left behind.
func TestFailedReplacementKeepsTheOldContent(t *testing.T) {
	for _, step := range []string{"sync-file", "rename"} {
		t.Run(step, func(t *testing.T) {
			dir, path := seed(t)
			l := stepLog{failAt: step}
			l.install(t)
			if err := WriteFile(path, []byte("new content"), 0o600); !errors.Is(err, errInjected) {
				t.Fatalf("err = %v, want the injected failure", err)
			}
			if got := read(t, path); got != "old content" {
				t.Errorf("content after a failed %s = %q, want the old content", step, got)
			}
			if left := leftovers(t, dir); len(left) != 0 {
				t.Errorf("temporary files left: %v", left)
			}
		})
	}
	t.Run("write", func(t *testing.T) {
		dir, path := seed(t)
		err := WriteFunc(path, 0o600, func(w io.Writer) error {
			if _, err := w.Write([]byte("half")); err != nil {
				return err
			}
			return errInjected
		})
		if !errors.Is(err, errInjected) {
			t.Fatalf("err = %v, want the writer's failure", err)
		}
		if got := read(t, path); got != "old content" {
			t.Errorf("content after a failed write = %q, want the old content", got)
		}
		if left := leftovers(t, dir); len(left) != 0 {
			t.Errorf("temporary files left: %v", left)
		}
	})
}

// TestTempLeftBehindIsReported: a failed write must remove its temporary file,
// and when that removal fails too the caller is told, beside the write failure,
// so a stray file is never left without a word. After a rename the temp name is
// already gone; that absence is not a second failure.
func TestTempLeftBehindIsReported(t *testing.T) {
	removeErr := errors.New("remove refused")
	orig := removeFile
	t.Cleanup(func() { removeFile = orig })
	removeFile = func(string) error { return removeErr }

	_, path := seed(t)
	err := WriteFunc(path, 0o600, func(io.Writer) error { return errInjected })
	if !errors.Is(err, errInjected) || !errors.Is(err, removeErr) {
		t.Errorf("err = %v, want both the write failure and the refused removal", err)
	}

	removeFile = orig
	_, path = seed(t)
	l := stepLog{failAt: "sync-dir"}
	l.install(t)
	err = WriteFile(path, []byte("new content"), 0o600)
	if !errors.Is(err, errInjected) || errors.Is(err, fs.ErrNotExist) {
		t.Errorf("err = %v, want the directory sync failure alone", err)
	}
}

// TestFailedDirectorySyncIsReported proves a failed directory sync is an error:
// the rename may not survive a power loss, so the caller must not read success.
func TestFailedDirectorySyncIsReported(t *testing.T) {
	_, path := seed(t)
	l := stepLog{failAt: "sync-dir"}
	l.install(t)
	if err := WriteFile(path, []byte("new content"), 0o600); !errors.Is(err, errInjected) {
		t.Fatalf("err = %v, want the injected failure", err)
	}
}

// TestWriteFileUsesTheModeAsked proves the file carries the caller's mode, not
// the temporary file's 0600 default.
func TestWriteFileUsesTheModeAsked(t *testing.T) {
	_, path := seed(t)
	for _, mode := range []os.FileMode{0o600, 0o644} {
		if err := WriteFile(path, []byte("x"), mode); err != nil {
			t.Fatal(err)
		}
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != mode {
			t.Errorf("mode = %v, want %v", fi.Mode().Perm(), mode)
		}
	}
}

// TestConcurrentWritersNeverMix proves two writers replacing one path at once
// each succeed and the result is one writer's content whole, never a mix or an
// error from a shared temporary name.
func TestConcurrentWritersNeverMix(t *testing.T) {
	_, path := seed(t)
	a := bytes.Repeat([]byte("a"), 1<<16)
	b := bytes.Repeat([]byte("b"), 1<<16)
	for range 50 {
		var wg sync.WaitGroup
		errs := make([]error, 2)
		wg.Go(func() { errs[0] = WriteFile(path, a, 0o600) })
		wg.Go(func() { errs[1] = WriteFile(path, b, 0o600) })
		wg.Wait()
		for _, err := range errs {
			if err != nil {
				t.Fatalf("concurrent write failed: %v", err)
			}
		}
		got := read(t, path)
		if got != string(a) && got != string(b) {
			t.Fatalf("content is neither writer's whole payload (%d bytes)", len(got))
		}
	}
}

// TestSyncRenameReplacesWithTheGivenFile proves SyncRename publishes a file the
// caller built and leaves nothing at the source name.
func TestSyncRenameReplacesWithTheGivenFile(t *testing.T) {
	dir, path := seed(t)
	src := filepath.Join(dir, "rebuilt")
	if err := os.WriteFile(src, []byte("rebuilt"), 0o600); err != nil {
		t.Fatal(err)
	}
	var l stepLog
	l.install(t)
	if err := SyncRename(src, path); err != nil {
		t.Fatal(err)
	}
	if got := read(t, path); got != "rebuilt" {
		t.Errorf("content = %q, want the rebuilt file", got)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Errorf("source still present: %v", err)
	}
	if len(l.steps) != 3 || l.steps[0] != "sync-file" {
		t.Errorf("steps = %v, want the file synced before the rename", l.steps)
	}
}

// TestIsTempNamesOnlyTemporaryFiles proves IsTemp tells a temporary file from the
// content files the stores keep.
func TestIsTempNamesOnlyTemporaryFiles(t *testing.T) {
	for name, want := range map[string]bool{
		".abc.zst.tmp-123": true,
		".model.tmp-9":     true,
		"abc.zst":          false,
		"12.eml":           false,
		".lock":            false,
	} {
		if got := IsTemp(name); got != want {
			t.Errorf("IsTemp(%q) = %v, want %v", name, got, want)
		}
	}
}
