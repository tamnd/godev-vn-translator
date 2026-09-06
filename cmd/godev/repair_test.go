package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tamnd/godev-vn-translator/content"
)

// pairIn writes a pair of files and returns the directory they are in, which
// is all `godev repair` needs. It reads no git and asks no route.
func pairIn(t *testing.T, rel, en, vi string) string {
	t.Helper()
	dir := t.TempDir()
	for base, text := range map[string]string{content.EnglishDir: en, content.VietnameseDir: vi} {
		path := filepath.Join(dir, base, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func read(t *testing.T, dir, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, content.VietnameseDir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The shape the command was written for: a url the English writes plain, on
// disk wrapped in a link to itself. 202 of these were under _content_vi.
func TestRepairWritesTheFix(t *testing.T) {
	en := "---\ntitle: A\n---\n\nXem https://vuln.go.dev de biet them.\n"
	vi := "---\ntitle: A\n---\n\nDoc [https://vuln.go.dev](https://vuln.go.dev) de biet them.\n"
	want := "---\ntitle: A\n---\n\nDoc https://vuln.go.dev de biet them.\n"

	dir := pairIn(t, "blog/x.md", en, vi)
	if err := runRepair(dir, nil); err != nil {
		t.Fatal(err)
	}
	if got := read(t, dir, "blog/x.md"); got != want {
		t.Errorf("repair\n got %q\nwant %q", got, want)
	}
}

func TestRepairWritesNothingUnderDashN(t *testing.T) {
	en := "---\ntitle: A\n---\n\nXem https://vuln.go.dev de biet them.\n"
	vi := "---\ntitle: A\n---\n\nDoc [https://vuln.go.dev](https://vuln.go.dev) de biet them.\n"

	dir := pairIn(t, "blog/x.md", en, vi)
	if err := runRepair(dir, []string{"-n"}); err != nil {
		t.Fatal(err)
	}
	if got := read(t, dir, "blog/x.md"); got != vi {
		t.Errorf("-n wrote the file: %q", got)
	}
}

// A page with nothing wrong with it is left byte for byte alone, which is what
// makes running this over the whole corpus a safe thing to do.
func TestRepairLeavesACleanPageAlone(t *testing.T) {
	en := "---\ntitle: A\n---\n\nSee [the docs](/doc/) for **more**.\n"
	vi := "---\ntitle: A\n---\n\nXem [tài liệu](/doc/) để biết **thêm**.\n"

	dir := pairIn(t, "blog/x.md", en, vi)
	before, err := os.Stat(filepath.Join(dir, content.VietnameseDir, "blog", "x.md"))
	if err != nil {
		t.Fatal(err)
	}
	if err := runRepair(dir, nil); err != nil {
		t.Fatal(err)
	}
	if got := read(t, dir, "blog/x.md"); got != vi {
		t.Errorf("repair changed a clean page\n got %q\nwant %q", got, vi)
	}
	after, err := os.Stat(filepath.Join(dir, content.VietnameseDir, "blog", "x.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("a page with nothing to repair was rewritten")
	}
}
