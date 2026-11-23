// Package attach holds what a person sends with a request beside its words:
// pictures of the page and PDFs. It says what is taken, keeps the files on
// Graft's data disk beside the database, and drops them with the
// request's captured context.
//
// A file's kind is read from its first bytes, never from what the sender
// called it. Only pictures and PDFs are taken: nothing a browser or an agent
// would run.
package attach

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The limits on one request's files.
const (
	MaxFiles = 5
	MaxFile  = 10 << 20
	MaxTotal = 25 << 20
)

// The kinds of file taken.
const (
	PNG  = "image/png"
	JPEG = "image/jpeg"
	GIF  = "image/gif"
	WebP = "image/webp"
	PDF  = "application/pdf"
)

// Upload is one file as it came in: the name the sender gave it and its bytes.
type Upload struct {
	Name string
	Data []byte
}

// Who sends a file.
const (
	// FromPerson is the person who asked, with their words.
	FromPerson = "person"
	// FromApprover is one of the plan's approvers, on the request's ticket.
	FromApprover = "approver"
)

// File is one file a request keeps: what it is called, its kind, its size,
// and who sent it.
type File struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	ContentType string `json:"contentType"`
	Size        int    `json:"size"`
	// From is FromPerson or FromApprover. A file kept before it was said is
	// the person's.
	From string `json:"from,omitempty"`
	// By is the approver who sent it, and Round the round of the plan they
	// answered with it.
	By    string `json:"by,omitempty"`
	Round int    `json:"round,omitempty"`
}

// Image says the file is a picture, not a PDF.
func (f File) Image() bool { return f.ContentType != PDF }

// Approvers says one of the plan's approvers sent the file.
func (f File) Approvers() bool { return f.From == FromApprover }

// Refused is a file that is not taken, said so a person can read it.
type Refused string

func (r Refused) Error() string { return string(r) }

// Kind reads a file's kind from its first bytes. ok is false for anything
// that is not a picture or a PDF.
func Kind(data []byte) (contentType string, ok bool) {
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return PNG, true
	case bytes.HasPrefix(data, []byte{0xFF, 0xD8, 0xFF}):
		return JPEG, true
	case bytes.HasPrefix(data, []byte("GIF87a")), bytes.HasPrefix(data, []byte("GIF89a")):
		return GIF, true
	case len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return WebP, true
	case bytes.HasPrefix(data, []byte("%PDF-")):
		return PDF, true
	}
	return "", false
}

// Check takes a request's uploads, as the person's, or says, in plain
// words, why not.
func Check(uploads []Upload) ([]File, error) {
	if len(uploads) > MaxFiles {
		return nil, Refused(fmt.Sprintf("send at most %d files with one request", MaxFiles))
	}
	var out []File
	for _, u := range uploads {
		f, err := Take(out, u)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, nil
}

// Take takes one more file for a request that holds have, held to the
// limits on the request's files together, or says, in plain words, why
// not. The file gets a new id, the name it was sent with made safe to
// show, and the kind its bytes say it is. It is the person's until the
// caller says otherwise.
func Take(have []File, u Upload) (File, error) {
	name := Name(u.Name, len(have)+1)
	if len(have) >= MaxFiles {
		return File{}, Refused(fmt.Sprintf("%s would be one file too many: a request keeps at most %d", name, MaxFiles))
	}
	if len(u.Data) == 0 {
		return File{}, Refused(fmt.Sprintf("%s is empty", name))
	}
	if len(u.Data) > MaxFile {
		return File{}, Refused(fmt.Sprintf("%s is larger than 10 MB; send a smaller picture or PDF", name))
	}
	total := len(u.Data)
	for _, f := range have {
		total += f.Size
	}
	if total > MaxTotal {
		return File{}, Refused(fmt.Sprintf("with %s, the files together are larger than 25 MB; send fewer or smaller ones", name))
	}
	kind, ok := Kind(u.Data)
	if !ok {
		return File{}, Refused(fmt.Sprintf("%s is not a picture (PNG, JPEG, GIF, or WebP) or a PDF; only those can be sent", name))
	}
	return File{ID: newID(), Name: name, ContentType: kind, Size: len(u.Data), From: FromPerson}, nil
}

// Name is the name a file is shown by: the sender's, without a folder or
// anything that is not a printable character, cut to 120 bytes. A file sent
// with no name is named by its place.
func Name(sent string, place int) string {
	sent = strings.ReplaceAll(sent, "\\", "/")
	if i := strings.LastIndexByte(sent, '/'); i >= 0 {
		sent = sent[i+1:]
	}
	sent = strings.Map(func(r rune) rune {
		if r == utf8.RuneError || !unicode.IsPrint(r) {
			return -1
		}
		return r
	}, sent)
	sent = strings.TrimSpace(sent)
	if len(sent) > 120 {
		n := 120
		for n > 0 && sent[n]&0xC0 == 0x80 {
			n--
		}
		sent = sent[:n]
	}
	if sent == "" || sent == "." || sent == ".." {
		return fmt.Sprintf("file %d", place)
	}
	return sent
}

var unsafe = regexp.MustCompile(`[^a-z0-9._-]+`)

// Slug is a file's name as a path in a sandbox: its place, then its name
// in lower-case letters, digits, dots, and dashes, with no extension.
func Slug(f File, place int) string {
	lower := strings.ToLower(f.Name)
	base := strings.TrimSuffix(lower, filepath.Ext(lower))
	base = strings.Trim(unsafe.ReplaceAllString(base, "-"), "-.")
	if len(base) > 40 {
		base = strings.Trim(base[:40], "-.")
	}
	if base == "" {
		base = "file"
	}
	return fmt.Sprintf("%d-%s", place, base)
}

// Ext is the extension a file of this kind is saved with.
func Ext(contentType string) string {
	switch contentType {
	case PNG:
		return ".png"
	case JPEG:
		return ".jpg"
	case GIF:
		return ".gif"
	case WebP:
		return ".webp"
	case PDF:
		return ".pdf"
	}
	return ""
}

func newID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// Files keeps each request's files on disk, one folder a request, one file
// an id. It never reads a file's name to find it.
type Files struct {
	Root string
}

var idShape = regexp.MustCompile(`^[0-9a-f]{16}$`)

// ErrNotFound is a file the disk does not hold, or an id that names none.
var ErrNotFound = errors.New("no such file")

func (f Files) dir(request string) (string, error) {
	if !idShape.MatchString(request) {
		return "", ErrNotFound
	}
	return filepath.Join(f.Root, request), nil
}

func (f Files) path(request, id string) (string, error) {
	dir, err := f.dir(request)
	if err != nil || !idShape.MatchString(id) {
		return "", ErrNotFound
	}
	return filepath.Join(dir, id), nil
}

// Put writes one file of a request.
func (f Files) Put(request, id string, data []byte) error {
	p, err := f.path(request, id)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o600)
}

// Read reads one file of a request.
func (f Files) Read(request, id string) ([]byte, error) {
	p, err := f.path(request, id)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	return b, err
}

// Remove deletes one file of a request.
func (f Files) Remove(request, id string) error {
	p, err := f.path(request, id)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Drop removes every file of a request. A request with none is no error.
func (f Files) Drop(request string) error {
	dir, err := f.dir(request)
	if err != nil {
		return nil
	}
	return os.RemoveAll(dir)
}
