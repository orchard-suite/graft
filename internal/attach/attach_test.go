package attach

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKindIsReadFromTheBytes(t *testing.T) {
	cases := map[string]struct {
		data []byte
		want string
	}{
		"png":                     {[]byte("\x89PNG\r\n\x1a\nrest"), PNG},
		"jpeg":                    {[]byte{0xFF, 0xD8, 0xFF, 0xE0}, JPEG},
		"gif":                     {[]byte("GIF89a..."), GIF},
		"webp":                    {[]byte("RIFF\x00\x00\x00\x00WEBPVP8 "), WebP},
		"pdf":                     {[]byte("%PDF-1.7\n"), PDF},
		"html":                    {[]byte("<!doctype html><script>"), ""},
		"svg":                     {[]byte("<svg xmlns="), ""},
		"a riff that is not webp": {[]byte("RIFF\x00\x00\x00\x00WAVEfmt "), ""},
	}
	for name, c := range cases {
		got, ok := Kind(c.data)
		if got != c.want || ok != (c.want != "") {
			t.Errorf("%s: %q %v", name, got, ok)
		}
	}
}

func TestCheckHoldsFilesToTheLimits(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n")
	files, err := Check([]Upload{{Name: "../../etc/shot.png", Data: png}, {Name: "", Data: []byte("%PDF-1.4")}})
	if err != nil || len(files) != 2 || files[0].Name != "shot.png" || files[1].Name != "file 2" || files[1].ContentType != PDF || files[0].ID == files[1].ID {
		t.Fatalf("files %+v %v", files, err)
	}
	refused := func(name string, uploads []Upload, want string) {
		t.Helper()
		_, err := Check(uploads)
		var r Refused
		if !errors.As(err, &r) || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	six := make([]Upload, MaxFiles+1)
	for i := range six {
		six[i] = Upload{Name: "a.png", Data: png}
	}
	refused("six files", six, "at most 5 files")
	refused("an empty file", []Upload{{Name: "a.png"}}, "a.png is empty")
	refused("a big file", []Upload{{Name: "big.pdf", Data: append([]byte("%PDF-"), make([]byte, MaxFile)...)}}, "big.pdf is larger than 10 MB")
	nine := append([]byte("%PDF-"), make([]byte, 9<<20)...)
	refused("too much together", []Upload{{Name: "a.pdf", Data: nine}, {Name: "b.pdf", Data: nine}, {Name: "c.pdf", Data: nine}}, "larger than 25 MB")
	refused("a page called a picture", []Upload{{Name: "page.png", Data: []byte("<html>")}}, "page.png is not a picture")
}

func TestSlugIsSafeInAPath(t *testing.T) {
	if got := Slug(File{Name: "Ada's Delta Portal · markup (2).PDF"}, 2); got != "2-ada-s-delta-portal-markup-2" {
		t.Errorf("slug %q", got)
	}
	if got := Slug(File{Name: "..."}, 1); got != "1-file" {
		t.Errorf("slug %q", got)
	}
}

func TestFilesKeepOneFolderARequestAndReachNothingElse(t *testing.T) {
	root := t.TempDir()
	f := Files{Root: root}
	if err := f.Put("0123456789abcdef", "fedcba9876543210", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if b, err := f.Read("0123456789abcdef", "fedcba9876543210"); err != nil || !bytes.Equal(b, []byte("x")) {
		t.Fatalf("read %q %v", b, err)
	}
	if err := os.WriteFile(filepath.Join(root, "graft.db"), []byte("db"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"../graft.db", "..", "FEDCBA9876543210", ""} {
		if _, err := f.Read("0123456789abcdef", id); !errors.Is(err, ErrNotFound) {
			t.Errorf("id %q: %v", id, err)
		}
	}
	if err := f.Drop(".."); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "graft.db")); err != nil {
		t.Error("a drop of a request that is no id reached outside its folder")
	}
	if err := f.Drop("0123456789abcdef"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Read("0123456789abcdef", "fedcba9876543210"); !errors.Is(err, ErrNotFound) {
		t.Errorf("after the drop: %v", err)
	}
}

func TestTakeHoldsOneMoreFileToTheRequestsLimits(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n")
	f, err := Take([]File{{Size: 10}}, Upload{Name: "logo.png", Data: png})
	if err != nil || f.Name != "logo.png" || f.ContentType != PNG || f.From != FromPerson || f.ID == "" {
		t.Fatalf("file %+v %v", f, err)
	}
	five := make([]File, MaxFiles)
	if _, err := Take(five, Upload{Name: "logo.png", Data: png}); err == nil || !strings.Contains(err.Error(), "logo.png would be one file too many") {
		t.Errorf("a sixth file: %v", err)
	}
	if _, err := Take([]File{{Size: 20 << 20}}, Upload{Name: "spec.pdf", Data: append([]byte("%PDF-"), make([]byte, 6<<20)...)}); err == nil || !strings.Contains(err.Error(), "with spec.pdf, the files together are larger than 25 MB") {
		t.Errorf("past the total: %v", err)
	}
}
