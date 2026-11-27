package multisend

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/schollz/croc/v11/src/multihub"
	"github.com/schollz/croc/v11/src/multirecv"
	"github.com/schollz/croc/v11/src/multisession"
	"github.com/schollz/croc/v11/src/multiwire"
)

// makeTree writes files (relative path → content) and empty dirs under root.
func makeTree(t *testing.T, root string, files map[string]string, emptyDirs ...string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range emptyDirs {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(d)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// A file and a folder together: relative paths keep the folder's own name,
// sources line up with Idx, empty folders are listed, symlinks inside the
// folder are skipped with a warning.
func TestBuildManifestFolder(t *testing.T) {
	root := t.TempDir()
	makeTree(t, root, map[string]string{
		"top.txt":           "top",
		"photos/a.jpg":      "aaaa",
		"photos/2024/b.jpg": "bb",
	}, "photos/empty")
	if runtime.GOOS != "windows" {
		if err := os.Symlink(filepath.Join(root, "top.txt"), filepath.Join(root, "photos", "link.txt")); err != nil {
			t.Fatal(err)
		}
	}

	var warn bytes.Buffer
	files, sources, emptyDirs, total, err := buildManifest(context.Background(),
		[]string{filepath.Join(root, "top.txt"), filepath.Join(root, "photos")}, &warn)
	if err != nil {
		t.Fatal(err)
	}

	got := map[string]int64{}
	for i, f := range files {
		if f.Idx != i {
			t.Fatalf("files[%d].Idx = %d, want %d", i, f.Idx, i)
		}
		// sources[i] must be the file for files[i].
		if !strings.HasSuffix(filepath.ToSlash(sources[i]), f.Path) {
			t.Fatalf("sources[%d] = %q does not match path %q", i, sources[i], f.Path)
		}
		got[f.Path] = f.Size
	}
	want := map[string]int64{"top.txt": 3, "photos/a.jpg": 4, "photos/2024/b.jpg": 2}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("manifest = %v, want %v", got, want)
	}
	if total != 9 {
		t.Fatalf("total = %d, want 9", total)
	}
	if !reflect.DeepEqual(emptyDirs, []string{"photos/empty"}) {
		t.Fatalf("emptyDirs = %q, want [photos/empty]", emptyDirs)
	}
	if runtime.GOOS != "windows" && !strings.Contains(warn.String(), "link.txt") {
		t.Fatalf("expected a warning about the skipped symlink, got %q", warn.String())
	}
}

// A symlink named directly on the command line is still sent, as the file
// it points to (the behaviour before folders were supported).
func TestBuildManifestTopLevelSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need extra privileges on Windows")
	}
	root := t.TempDir()
	makeTree(t, root, map[string]string{"real.txt": "12345"})
	link := filepath.Join(root, "link.txt")
	if err := os.Symlink(filepath.Join(root, "real.txt"), link); err != nil {
		t.Fatal(err)
	}
	files, _, _, _, err := buildManifest(context.Background(), []string{link}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Path != "link.txt" || files[0].Size != 5 {
		t.Fatalf("got %+v, want link.txt with the target's 5 bytes", files)
	}
}

func TestBuildManifestErrors(t *testing.T) {
	root := t.TempDir()
	makeTree(t, root, map[string]string{"a/x.txt": "1", "b/x.txt": "2"})

	// Two inputs that would land on the same path.
	_, _, _, _, err := buildManifest(context.Background(),
		[]string{filepath.Join(root, "a", "x.txt"), filepath.Join(root, "b", "x.txt")}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), `"x.txt"`) {
		t.Fatalf("duplicate path: got %v", err)
	}

	// A path that does not exist.
	if _, _, _, _, err = buildManifest(context.Background(), []string{filepath.Join(root, "missing")}, &bytes.Buffer{}); err == nil {
		t.Fatal("missing path: want error")
	}

	// Only things that are skipped: nothing left to send.
	if runtime.GOOS != "windows" {
		dir := filepath.Join(root, "links")
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(root, "a", "x.txt"), filepath.Join(dir, "l")); err != nil {
			t.Fatal(err)
		}
		if _, _, _, _, err = buildManifest(context.Background(), []string{dir}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "nothing to send") {
			t.Fatalf("only symlinks: got %v", err)
		}
	}
}

// sendTree runs a full in-memory session: buildManifest on paths, then
// sendSession to n receivers. It returns each receiver's output folder.
func sendTree(t *testing.T, room string, paths []string, n int) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	hub := multihub.New(multihub.Config{MaxReceivers: n})
	connect := func(role multiwire.Role) *multisession.Link {
		t.Helper()
		c, s := multihub.Pipe()
		go hub.Serve(room, s)
		l, e := multisession.NewLink(ctx, c, role, n)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { l.Close() })
		return l
	}

	files, sources, emptyDirs, total, err := buildManifest(ctx, paths, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	session := multisession.Session{DataKey: bytes.Repeat([]byte{5}, 32), ChunkSize: multiwire.ChunkSize,
		Files: files, EmptyDirs: emptyDirs, TotalSize: total}

	sender := connect(multiwire.RoleSender)
	const code = "room-correct-words"
	sent := make(chan error, 1)
	go func() {
		sent <- sendSession(ctx, sender, code, session, SendOptions{Files: sources, Receivers: n})
	}()

	var outs []string
	results := make(chan error, n)
	for range n {
		l := connect(multiwire.RoleReceiver)
		out := t.TempDir()
		outs = append(outs, out)
		go func() {
			s, e := multisession.JoinSession(ctx, l, code, "")
			if e != nil {
				results <- e
				return
			}
			var rf []multirecv.FileEntry
			for _, f := range s.Files {
				rf = append(rf, multirecv.FileEntry{Idx: f.Idx, Path: f.Path, Size: f.Size})
			}
			results <- multirecv.ReceiveStream(ctx, receiverStream{l}, out, multirecv.Session{
				DataKey: s.DataKey, ChunkSize: s.ChunkSize, Files: rf, EmptyDirs: s.EmptyDirs, TotalSize: s.TotalSize}, nil)
		}()
	}
	for range n {
		if e := <-results; e != nil {
			t.Fatalf("receiver: %v", e)
		}
	}
	if e := <-sent; e != nil {
		t.Fatalf("sender: %v", e)
	}
	return outs
}

// listTree returns every file (with content) and every dir under root.
func listTree(t *testing.T, root string) (files map[string]string, dirs []string) {
	t.Helper()
	files = map[string]string{}
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || p == root {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if info.IsDir() {
			dirs = append(dirs, rel)
			return nil
		}
		b, err := os.ReadFile(p)
		files[rel] = string(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(dirs)
	return files, dirs
}

// End to end: a folder (with a sub-folder, an empty folder and a file large
// enough for several DATA records) plus a single file reach 2 receivers with
// the same tree.
func TestSendFolderInMemory(t *testing.T) {
	root := t.TempDir()
	big := strings.Repeat("folder transfer\n", 10000) // ~160 KB, several chunks
	tree := map[string]string{
		"photos/a.jpg":      "aaaa",
		"photos/2024/b.jpg": big,
		"photos/2024/c.txt": "",
	}
	makeTree(t, root, tree, "photos/empty")
	makeTree(t, root, map[string]string{"note.txt": "hi"})

	outs := sendTree(t, "multi:folder", []string{filepath.Join(root, "photos"), filepath.Join(root, "note.txt")}, 2)

	wantFiles := map[string]string{"note.txt": "hi"}
	for k, v := range tree {
		wantFiles[k] = v
	}
	wantDirs := []string{"photos", "photos/2024", "photos/empty"}
	for i, out := range outs {
		files, dirs := listTree(t, out)
		if !reflect.DeepEqual(files, wantFiles) {
			t.Fatalf("receiver %d files differ: got %d files", i, len(files))
		}
		if !reflect.DeepEqual(dirs, wantDirs) {
			t.Fatalf("receiver %d dirs = %q, want %q", i, dirs, wantDirs)
		}
	}
}

// A folder that holds only empty folders: no file, so no DATA at all. The
// receivers must still create the folders and finish.
func TestSendOnlyEmptyFoldersInMemory(t *testing.T) {
	root := t.TempDir()
	makeTree(t, root, nil, "empty/one", "empty/two")

	outs := sendTree(t, "multi:empty-folders", []string{filepath.Join(root, "empty")}, 1)

	_, dirs := listTree(t, outs[0])
	if want := []string{"empty", "empty/one", "empty/two"}; !reflect.DeepEqual(dirs, want) {
		t.Fatalf("dirs = %q, want %q", dirs, want)
	}
}
