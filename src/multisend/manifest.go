package multisend

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"

	"github.com/schollz/croc/v11/src/croc"
	"github.com/schollz/croc/v11/src/multisession"
)

// maxFiles is the most files one session can carry: a DATA record stores the
// file index as a u16, so Idx must stay within 0..65535.
const maxFiles = 65536

// buildManifest expands the files and folders given on the command line into
// the manifest the receivers get, plus the local path of every entry.
//
// sources[i] is the file on disk for files[i], and files[i].Idx == i, because
// StreamFiles uses the position in sources as the fileIdx of each record.
//
// The folder walk itself is croc's own (the one 1-1 `croc send` uses), so a
// folder arrives with the same layout as in 1-1 mode. On top of it this adds
// the checks multi-send needs: only regular files, no two entries with the
// same path, and at most maxFiles files. Skipped entries are reported on warn.
func buildManifest(ctx context.Context, paths []string, warn io.Writer) (files []multisession.FileEntry, sources []string, emptyDirs []string, total int64, err error) {
	// zipfolder=false: send the tree as it is (no temp zip file).
	// ignoreGit=false, no exclusions: multi-send has no such flags yet.
	infos, emptyFolders, _, err := croc.GetFilesInfoContext(ctx, paths, false, false, nil)
	if err != nil {
		return nil, nil, nil, 0, err
	}

	seen := map[string]bool{} // manifest paths so far, to catch duplicates
	for _, fi := range infos {
		// FolderRemote is the folder relative to what the user gave, always
		// with "/" and a trailing "/" ("./" for a file given directly), so
		// path (not filepath) builds the manifest path on every OS.
		rel := path.Join(fi.FolderRemote, fi.Name)
		src := filepath.Join(fi.FolderSource, fi.Name)

		size, ok := regularSize(fi, src)
		if !ok {
			// Symlinks inside a folder, sockets, devices...: StreamFiles
			// could not send them as plain bytes (a symlink's Lstat size is
			// the length of the link, not of its target). Skip, don't fail
			// the whole send over one odd entry.
			fmt.Fprintf(warn, "skipped (not a regular file): %s\n", src)
			continue
		}

		// Two inputs can map to the same path, e.g. `a/x.txt b/x.txt` both
		// become "x.txt". The receiver would reject the manifest only after
		// PAKE; fail here, before connecting.
		if seen[rel] {
			return nil, nil, nil, 0, fmt.Errorf("two files would be saved as %q; send them from different folders", rel)
		}
		seen[rel] = true

		files = append(files, multisession.FileEntry{Idx: len(files), Path: rel, Size: size})
		sources = append(sources, src)
		total += size
	}

	// Checked after the expansion: one folder can hold far more files than
	// there were arguments.
	if len(files) > maxFiles {
		return nil, nil, nil, 0, fmt.Errorf("too many files: %d (max %d)", len(files), maxFiles)
	}

	for _, ef := range emptyFolders {
		// "photos/empty/" -> "photos/empty"; the receiver creates it.
		emptyDirs = append(emptyDirs, path.Clean(ef.FolderRemote))
	}

	if len(files) == 0 && len(emptyDirs) == 0 {
		return nil, nil, nil, 0, errors.New("nothing to send")
	}
	return files, sources, emptyDirs, total, nil
}

// regularSize returns the number of bytes StreamFiles will read from src, and
// false if the entry cannot be sent as a plain file.
//
// croc's walk uses Lstat, so a symlink shows up with ModeSymlink. A symlink
// the user named directly on the command line (FolderRemote "./") is followed,
// as before folders were supported: it is sent as the file it points to,
// under the link's name. A symlink found inside a folder is skipped, so a walk
// can never escape the folder or loop.
func regularSize(fi croc.FileInfo, src string) (int64, bool) {
	if fi.Mode.IsRegular() {
		return fi.Size, true
	}
	if fi.Mode&os.ModeSymlink != 0 && fi.FolderRemote == "./" {
		if st, err := os.Stat(src); err == nil && st.Mode().IsRegular() {
			return st.Size(), true
		}
	}
	return 0, false
}
