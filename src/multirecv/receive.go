// Package multirecv receives the encrypted data records of a multi-send session.
package multirecv

import (
	"bytes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path"

	"github.com/schollz/croc/v11/src/crypt"
	"github.com/schollz/croc/v11/src/receivefs"
)

// ErrHashMismatch indicates that received data does not match the sender's digest.
var ErrHashMismatch = errors.New("hash-mismatch")

type FileEntry struct {
	Idx  int
	Path string
	Size int64
}
type Session struct {
	DataKey   []byte
	ChunkSize int
	Files     []FileEntry
	EmptyDirs []string
	TotalSize int64
}
type fileState struct {
	entry   FileEntry
	file    *os.File
	temp    string
	written int64
	done    bool
}

// Receiver processes DATA payloads after the session has been authenticated.
type Receiver struct {
	root            *receivefs.Root
	aead            cipher.AEAD
	files           map[uint16]*fileState
	total, received int64
	progress        func(int64, int64)
	ended           bool
}

func New(outDir string, session Session, progress func(int64, int64)) (*Receiver, error) {
	if len(session.DataKey) != 32 || session.ChunkSize != 32768 || session.TotalSize < 0 {
		return nil, errors.New("invalid session parameters")
	}
	entries := make([]receivefs.Entry, 0, len(session.Files)+len(session.EmptyDirs))
	ids := map[int]bool{}
	var total int64
	for _, f := range session.Files {
		if f.Idx < 0 || f.Idx > 65535 || ids[f.Idx] || f.Size < 0 || f.Size > session.TotalSize-total {
			return nil, errors.New("invalid file manifest")
		}
		ids[f.Idx] = true
		total += f.Size
		entries = append(entries, receivefs.Entry{Path: f.Path, Kind: receivefs.KindFile})
	}
	if total != session.TotalSize {
		return nil, errors.New("invalid total size")
	}
	for _, d := range session.EmptyDirs {
		entries = append(entries, receivefs.Entry{Path: d, Kind: receivefs.KindDirectory})
	}
	clean, err := receivefs.ValidateEntries(entries)
	if err != nil {
		return nil, err
	}
	aead, err := crypt.NewAESGCM(session.DataKey)
	if err != nil {
		return nil, err
	}
	root, err := receivefs.OpenRoot(outDir)
	if err != nil {
		return nil, err
	}
	r := &Receiver{root: root, aead: aead, files: make(map[uint16]*fileState), total: total, progress: progress}
	for i, f := range session.Files {
		f.Path = clean[i].Path
		if err = root.RejectSymlinkPath(f.Path); err != nil {
			r.Close()
			return nil, err
		}
		if _, err = root.Lstat(f.Path); err == nil {
			r.Close()
			return nil, fmt.Errorf("destination already exists: %s", f.Path)
		} else if !errors.Is(err, os.ErrNotExist) {
			r.Close()
			return nil, err
		}
		r.files[uint16(f.Idx)] = &fileState{entry: f}
	}
	for _, d := range clean[len(session.Files):] {
		if err = root.MkdirAll(d.Path, 0700); err != nil {
			r.Close()
			return nil, err
		}
	}
	return r, nil
}

// Accept authenticates one encrypted DATA payload. It returns true only on a valid all-end.
func (r *Receiver) Accept(payload []byte) (bool, error) {
	if r.ended {
		return false, errors.New("session already ended")
	}
	b, err := crypt.DecryptAEAD(payload, r.aead)
	if err != nil {
		return false, fmt.Errorf("decrypt data: %w", err)
	}
	if len(b) == 0 {
		return false, errors.New("empty record")
	}
	switch b[0] {
	case 1:
		if len(b) < 11 || len(b) > 11+32768 {
			return false, errors.New("invalid chunk")
		}
		f := r.files[binary.LittleEndian.Uint16(b[1:3])]
		if f == nil || f.done {
			return false, errors.New("unknown or completed file")
		}
		offset := binary.LittleEndian.Uint64(b[3:11])
		data := b[11:]
		if offset > uint64(f.written) || len(data) == 0 || offset > uint64(f.entry.Size) || uint64(len(data)) > uint64(f.entry.Size)-offset {
			return false, errors.New("invalid chunk offset or size")
		}
		if err = r.open(f); err != nil {
			return false, err
		}
		if offset < uint64(f.written) {
			if uint64(len(data)) > uint64(f.written)-offset {
				return false, errors.New("overlapping chunk")
			}
			previous := make([]byte, len(data))
			if _, err = f.file.ReadAt(previous, int64(offset)); err != nil {
				return false, err
			}
			if !bytes.Equal(previous, data) {
				return false, errors.New("conflicting chunk")
			}
			return false, nil
		}
		n, err := f.file.WriteAt(data, int64(offset))
		if err != nil {
			return false, err
		}
		if n != len(data) {
			return false, io.ErrShortWrite
		}
		f.written += int64(n)
		r.received += int64(n)
		if r.progress != nil {
			r.progress(r.received, r.total)
		}
	case 2:
		if len(b) != 35 {
			return false, errors.New("invalid file-end")
		}
		f := r.files[binary.LittleEndian.Uint16(b[1:3])]
		if f == nil || f.done || f.written != f.entry.Size {
			return false, errors.New("incomplete or unknown file")
		}
		if err = r.open(f); err != nil {
			return false, err
		}
		if _, err = f.file.Seek(0, io.SeekStart); err != nil {
			return false, err
		}
		h := sha256.New()
		if _, err = io.Copy(h, f.file); err != nil {
			return false, err
		}
		if !bytes.Equal(h.Sum(nil), b[3:]) {
			closeErr := f.file.Close()
			f.file = nil
			removeErr := r.root.Remove(f.temp)
			if removeErr == nil {
				f.temp = ""
			}
			return false, errors.Join(ErrHashMismatch, closeErr, removeErr)
		}
		if err = f.file.Close(); err != nil {
			return false, err
		}
		f.file = nil
		if err = r.root.PublishNew(f.temp, f.entry.Path); err != nil {
			return false, err
		}
		f.temp = ""
		f.done = true
	case 3:
		if len(b) != 1 {
			return false, errors.New("invalid all-end")
		}
		for _, f := range r.files {
			if !f.done {
				return false, errors.New("incomplete session")
			}
		}
		r.ended = true
		return true, nil
	default:
		return false, errors.New("unknown data record")
	}
	return false, nil
}
func (r *Receiver) open(f *fileState) error {
	if f.file != nil {
		return nil
	}
	file, name, err := r.root.CreateTemp(path.Dir(f.entry.Path), ".croc-multi-", 0600)
	if err != nil {
		return err
	}
	f.file = file
	f.temp = name
	return nil
}

// Close removes unfinished files and closes the destination root.
func (r *Receiver) Close() error {
	if r.root == nil {
		return nil
	}
	var result error
	for _, f := range r.files {
		if f.file != nil {
			result = errors.Join(result, f.file.Close())
			f.file = nil
		}
		if f.temp != "" {
			result = errors.Join(result, r.root.Remove(f.temp))
			f.temp = ""
		}
	}
	result = errors.Join(result, r.root.Close())
	r.root = nil
	return result
}
