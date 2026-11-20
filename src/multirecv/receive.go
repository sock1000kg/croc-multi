// Package multirecv is the RECEIVER side of croc's multi-send mode (owner: L,
// milestone M1d).
//
//   - client.go:  Receive, the full flow behind `croc --multi CODE`: dial the
//     relay, authenticate (multisession.JoinSession), then ReceiveStream.
//   - stream.go:  ReceiveStream, the loop that reads DATA frames and reports
//     ready / progress / done / fail to the sender.
//   - receive.go: Receiver, which decrypts each DATA payload and writes it to
//     disk safely (temp file, hash check, then publish under the final name).
//
// File names come only from the authenticated manifest and only go through
// src/receivefs, which rejects "..", absolute paths and symlinks.
package multirecv

import (
	"bytes"
	"crypto/cipher"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path"

	"github.com/schollz/croc/v11/src/crypt"
	"github.com/schollz/croc/v11/src/multisession"
	"github.com/schollz/croc/v11/src/multiwire"
	"github.com/schollz/croc/v11/src/receivefs"
)

// ErrHashMismatch indicates that received data does not match the sender's digest.
var ErrHashMismatch = errors.New("hash-mismatch")

// Session and FileEntry are the manifest multisession.JoinSession returns.
type (
	Session   = multisession.Session
	FileEntry = multisession.FileEntry
)

// fileState tracks one file of the manifest while it is being received.
type fileState struct {
	entry   FileEntry
	file    *os.File // open temp file; nil before the first chunk and after file-end
	temp    string   // temp file name inside the output root; "" once published or removed
	written int64    // bytes written so far; chunks must arrive in order
	done    bool     // hash matched and file published under its final name
}

// Receiver processes DATA payloads after the session has been authenticated.
// It is not safe for concurrent use.
type Receiver struct {
	root            *receivefs.Root
	aead            cipher.AEAD
	files           map[uint16]*fileState
	total, received int64
	progress        func(int64, int64)
	ended           bool
}

// New validates the manifest, checks that no destination file exists yet,
// creates the empty directories, and returns a Receiver writing under outDir.
func New(outDir string, session Session, progress func(int64, int64)) (*Receiver, error) {
	if len(session.DataKey) != 32 || session.ChunkSize != multiwire.ChunkSize || session.TotalSize < 0 {
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

// Accept decrypts and applies one DATA payload. It returns true only for a
// valid all-end record, i.e. every file arrived and matched its hash.
func (r *Receiver) Accept(payload []byte) (bool, error) {
	if r.ended {
		return false, errors.New("session already ended")
	}
	// Decryption also authenticates: a tampered or foreign payload fails here.
	b, err := crypt.DecryptAEAD(payload, r.aead)
	if err != nil {
		return false, fmt.Errorf("decrypt data: %w", err)
	}
	kind, chunk, fileEnd, err := multiwire.DecodeRecord(b)
	if err != nil {
		return false, err
	}
	switch kind {
	case multiwire.RecordKindChunk:
		return false, r.writeChunk(chunk)
	case multiwire.RecordKindFileEnd:
		return false, r.finishFile(fileEnd)
	default: // multiwire.RecordKindAllEnd
		for _, f := range r.files {
			if !f.done {
				return false, errors.New("incomplete session")
			}
		}
		r.ended = true
		return true, nil
	}
}

// writeChunk writes one chunk at its offset. Chunks must arrive in order:
// an offset past what is written is a gap and an error. An offset before it
// is a replay and is accepted only if the bytes are identical.
func (r *Receiver) writeChunk(c *multiwire.ChunkRecord) error {
	f := r.files[c.FileIdx]
	if f == nil || f.done {
		return errors.New("unknown or completed file")
	}
	offset, data := c.Offset, c.Data
	if offset > uint64(f.written) || offset > uint64(f.entry.Size) || uint64(len(data)) > uint64(f.entry.Size)-offset {
		return errors.New("invalid chunk offset or size")
	}
	if err := r.open(f); err != nil {
		return err
	}
	if offset < uint64(f.written) {
		if uint64(len(data)) > uint64(f.written)-offset {
			return errors.New("overlapping chunk")
		}
		previous := make([]byte, len(data))
		if _, err := f.file.ReadAt(previous, int64(offset)); err != nil {
			return err
		}
		if !bytes.Equal(previous, data) {
			return errors.New("conflicting chunk")
		}
		return nil
	}
	n, err := f.file.WriteAt(data, int64(offset))
	if err != nil {
		return err
	}
	if n != len(data) {
		return io.ErrShortWrite
	}
	f.written += int64(n)
	r.received += int64(n)
	if r.progress != nil {
		r.progress(r.received, r.total)
	}
	return nil
}

// finishFile re-hashes the temp file from disk and compares it with the
// sender's SHA-256. On a match the temp file is published under its final
// name; on a mismatch it is deleted, so a corrupt file never looks finished.
func (r *Receiver) finishFile(fe *multiwire.FileEndRecord) error {
	f := r.files[fe.FileIdx]
	if f == nil || f.done || f.written != f.entry.Size {
		return errors.New("incomplete or unknown file")
	}
	// open also covers an empty file, which never got a chunk.
	if err := r.open(f); err != nil {
		return err
	}
	if _, err := f.file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	h := sha256.New()
	if _, err := io.Copy(h, f.file); err != nil {
		return err
	}
	if !bytes.Equal(h.Sum(nil), fe.Hash[:]) {
		closeErr := f.file.Close()
		f.file = nil
		removeErr := r.root.Remove(f.temp)
		if removeErr == nil {
			f.temp = ""
		}
		return errors.Join(ErrHashMismatch, closeErr, removeErr)
	}
	if err := f.file.Close(); err != nil {
		return err
	}
	f.file = nil
	if err := r.root.PublishNew(f.temp, f.entry.Path); err != nil {
		return err
	}
	f.temp = ""
	f.done = true
	return nil
}

// open creates f's temp file next to its final location on first use.
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
