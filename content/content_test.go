package content

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// A checkout with one page that is translated and one that is not.
func root(t *testing.T) Root {
	t.Helper()
	dir := t.TempDir()
	for _, rel := range []string{"doc/faq.md", "doc/devel/weekly.html", "js/site.js"} {
		p := filepath.Join(dir, EnglishDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("<p>hello</p>\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return Root(dir)
}

// Walk and Find have to agree about what is in the corpus. They did not, and
// the disagreement was invisible: Walk stopped offering doc/devel/weekly.html
// and Find carried on handing it out, so the jobs already queued for it went to
// a model as if the page had never been skipped.
func TestWalkAndFindAgreeAboutTheSkipList(t *testing.T) {
	r := root(t)

	pairs, err := r.Pairs()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pairs {
		if Skipped(p.Rel) {
			t.Errorf("Pairs returned %s, which is on the skip list", p.Rel)
		}
		if _, err := r.Find(p.Rel); err != nil {
			t.Errorf("Pairs returned %s but Find will not: %v", p.Rel, err)
		}
	}

	if len(pairs) != 1 || pairs[0].Rel != "doc/faq.md" {
		t.Fatalf("want only doc/faq.md, got %v", pairs)
	}
}

func TestFindRefusesASkippedPage(t *testing.T) {
	r := root(t)

	for _, rel := range []string{
		"doc/devel/weekly.html",
		"_content/doc/devel/weekly.html",
		"./doc/devel/weekly.html",
		"js/site.js",
	} {
		_, err := r.Find(rel)
		if !errors.Is(err, ErrSkipped) {
			t.Errorf("Find(%q) = %v, want ErrSkipped", rel, err)
		}
	}
}

// The sentinel exists so a caller can tell a page it is not meant to have from
// a page that has gone missing, because the first is a job to drop quietly and
// the second is worth an attempt and a line in the log.
func TestFindOnAMissingPageIsNotSkipped(t *testing.T) {
	r := root(t)

	_, err := r.Find("doc/gone.md")
	if err == nil {
		t.Fatal("Find on a missing page returned no error")
	}
	if errors.Is(err, ErrSkipped) {
		t.Errorf("Find on a missing page = %v, want a plain error", err)
	}
}
