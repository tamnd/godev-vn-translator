package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tamnd/godev-vn-translator/content"
)

// repo is a checkout with a history, because both functions under test read git
// and neither can be tested against anything else.
type repo struct {
	t   *testing.T
	dir string
}

func newRepo(t *testing.T) *repo {
	t.Helper()
	r := &repo{t: t, dir: t.TempDir()}
	r.git("init", "-q", "-b", "main")
	r.git("config", "user.email", "test@example.com")
	r.git("config", "user.name", "test")
	return r
}

func (r *repo) git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = r.dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func (r *repo) write(rel, text string) {
	r.t.Helper()
	path := filepath.Join(r.dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

// commit stages everything and commits it on a fixed date, so the date the
// backfill records is a value the test can assert rather than today's.
func (r *repo) commit(message, date string) string {
	r.t.Helper()
	r.git("add", "-A")
	cmd := exec.Command("git", "commit", "-q", "-m", message)
	cmd.Dir = r.dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_DATE="+date+"T12:00:00+07:00",
		"GIT_COMMITTER_DATE="+date+"T12:00:00+07:00")
	if out, err := cmd.CombinedOutput(); err != nil {
		r.t.Fatalf("git commit: %v\n%s", err, out)
	}
	return strings.TrimSpace(r.git("rev-parse", "HEAD"))
}

// The commit a translation was written in is the whole basis of the backfill,
// so the case that matters is a file that was committed once and then left
// alone while everything around it moved.
func TestLastCommitsFindsTheCommitThatTouchedTheFile(t *testing.T) {
	r := newRepo(t)
	r.write("_content/a.md", "first english\n")
	r.write("_content_vi/a.md", "bản dịch\n")
	wrote := r.commit("translate a", "2026-01-05")

	r.write("_content/a.md", "second english\n")
	r.write("_content/b.md", "unrelated\n")
	r.commit("upstream moves on", "2026-02-05")

	got, err := lastCommits(r.dir)
	if err != nil {
		t.Fatal(err)
	}
	c, ok := got["_content_vi/a.md"]
	if !ok {
		t.Fatalf("no commit for _content_vi/a.md, got %v", got)
	}
	if c.hash != wrote {
		t.Errorf("hash = %s, want the commit that wrote the translation %s", c.hash, wrote)
	}
	if c.date != "2026-01-05" {
		t.Errorf("date = %s, want 2026-01-05", c.date)
	}
	// Nothing outside _content_vi belongs in the map. An English path in there
	// would be looked up under a Vietnamese key and never match, which is a
	// silent skip rather than an error.
	for path := range got {
		if !strings.HasPrefix(path, content.VietnameseDir+"/") {
			t.Errorf("%s is not under %s", path, content.VietnameseDir)
		}
	}
}

// The last commit and not the first. A translation corrected later was current
// at the correction, and recording the first commit would call it stale against
// English it was in fact checked over.
func TestLastCommitsIsTheLatest(t *testing.T) {
	r := newRepo(t)
	r.write("_content_vi/a.md", "bản dịch\n")
	first := r.commit("translate a", "2026-01-05")

	r.write("_content_vi/a.md", "bản dịch đã sửa\n")
	second := r.commit("fix a typo in a", "2026-03-05")

	got, err := lastCommits(r.dir)
	if err != nil {
		t.Fatal(err)
	}
	if h := got["_content_vi/a.md"].hash; h != second {
		t.Errorf("hash = %s, want the later commit %s, not the first %s", h, second, first)
	}
}

// A merge reports every file it brings in as touched on the day of the merge,
// which is not the day the work was done. Skipping merges keeps the date the
// one somebody actually wrote the translation on.
func TestLastCommitsSkipsMerges(t *testing.T) {
	r := newRepo(t)
	r.write("README.md", "root\n")
	r.commit("start", "2026-01-01")

	r.git("checkout", "-q", "-b", "work")
	r.write("_content_vi/a.md", "bản dịch\n")
	wrote := r.commit("translate a", "2026-01-05")

	r.git("checkout", "-q", "main")
	r.write("README.md", "root, moved\n")
	r.commit("something else", "2026-01-06")
	r.git("merge", "-q", "--no-ff", "-m", "merge work", "work")

	got, err := lastCommits(r.dir)
	if err != nil {
		t.Fatal(err)
	}
	if h := got["_content_vi/a.md"].hash; h != wrote {
		t.Errorf("hash = %s, want the commit that did the work %s", h, wrote)
	}
}

// The batch protocol is a header line, the object, then a newline. Getting the
// framing wrong reads the next object's header as content, so the test asks for
// several at once and checks the bytes, not just the count.
func TestCatFileReadsEveryBlob(t *testing.T) {
	r := newRepo(t)
	r.write("_content/a.md", "english a\n")
	r.write("_content/b.md", "english b, which is longer than a\n")
	r.write("_content/c.md", "")
	head := r.commit("start", "2026-01-01")

	refs := []string{
		head + ":_content/a.md",
		head + ":_content/b.md",
		head + ":_content/c.md",
	}
	got, err := catFile(r.dir, refs)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		refs[0]: "english a\n",
		refs[1]: "english b, which is longer than a\n",
		refs[2]: "",
	}
	for ref, text := range want {
		if got[ref] != text {
			t.Errorf("%s = %q, want %q", ref, got[ref], text)
		}
	}
}

// A file that did not exist at that commit is the upstream rename case, and it
// has to come back as absent rather than as an error or as the next blob.
func TestCatFileReportsAMissingObject(t *testing.T) {
	r := newRepo(t)
	r.write("_content/a.md", "english a\n")
	head := r.commit("start", "2026-01-01")

	refs := []string{head + ":_content/gone.md", head + ":_content/a.md"}
	got, err := catFile(r.dir, refs)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got[refs[0]]; ok {
		t.Errorf("a missing object came back as %q", got[refs[0]])
	}
	if got[refs[1]] != "english a\n" {
		t.Errorf("the blob after a missing one = %q, want %q", got[refs[1]], "english a\n")
	}
}

func TestCatFileWithNothingToRead(t *testing.T) {
	got, err := catFile(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("got %d blobs, want none", len(got))
	}
}
