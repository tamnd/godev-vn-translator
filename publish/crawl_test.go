package publish

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// crawlFixture stands in for golangorg. It is not a copy of the site, it is the
// four shapes the export has to get right: a page, an asset a page links to, a
// redirect to another path here, and a redirect off the site.
func crawlFixture(t *testing.T) (*crawl, func()) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(`<a href="/doc/faq">FAQ</a>
			<a href="https://go.dev/pkg/fmt">fmt</a>
			<link href="/css/site.css">`))
	})
	mux.HandleFunc("/css/site.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css")
		w.Write([]byte(`@font-face { src: url(/fonts/x.woff2); }`))
	})
	mux.HandleFunc("/doc/faq", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(`<h1>Câu hỏi thường gặp</h1>`))
	})
	mux.HandleFunc("/doc/faq/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/doc/faq", http.StatusMovedPermanently)
	})
	mux.HandleFunc("/doc/articles/image_draw.html", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/blog/image-draw", http.StatusMovedPermanently)
	})
	mux.HandleFunc("/design/go2draft", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://github.com/golang/proposal", http.StatusFound)
	})
	srv := httptest.NewServer(mux)

	dir := t.TempDir()
	c := &crawl{
		opts: Options{Host: "godev-vn.tamnd.com", Log: func(string, ...any) {}},
		out:  dir,
		site: srv.URL,
		seen: map[string]bool{},
	}
	return c, srv.Close
}

func read(t *testing.T, dir, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("reading %s: %v", rel, err)
	}
	return string(b)
}

func TestCrawlPage(t *testing.T) {
	c, done := crawlFixture(t)
	defer done()
	if err := c.fetch(context.Background(), "/"); err != nil {
		t.Fatal(err)
	}
	got := read(t, c.out, "index.html")
	if !strings.Contains(got, `href="https://go.dev/pkg/fmt"`) {
		t.Errorf("the link to /pkg was rewritten, and there is no /pkg in the export:\n%s", got)
	}
	// The page links to two things this export can hold and one it cannot, and
	// the one it cannot must not be queued.
	want := []string{"/doc/faq", "/css/site.css"}
	if len(c.queue) != len(want) {
		t.Fatalf("queued %q, want %q", c.queue, want)
	}
	for i, w := range want {
		if c.queue[i] != w {
			t.Errorf("queued %q at %d, want %q", c.queue[i], i, w)
		}
	}
}

// A stylesheet is the only place a font is named, so a crawl that reads HTML
// and nothing else publishes a site with no fonts on it.
func TestCrawlStylesheet(t *testing.T) {
	c, done := crawlFixture(t)
	defer done()
	if err := c.fetch(context.Background(), "/css/site.css"); err != nil {
		t.Fatal(err)
	}
	if len(c.queue) != 1 || c.queue[0] != "/fonts/x.woff2" {
		t.Errorf("queued %q, want the font", c.queue)
	}
	if got := read(t, c.out, "css/site.css"); !strings.Contains(got, "font-face") {
		t.Errorf("the stylesheet was not written whole: %q", got)
	}
}

// This is the case the first run of the export got wrong. Following the
// redirect wrote the article at the old path, where its relative links to the
// figures pointed at a directory that does not exist.
func TestCrawlRedirectStaysARedirect(t *testing.T) {
	c, done := crawlFixture(t)
	defer done()
	if err := c.fetch(context.Background(), "/doc/articles/image_draw.html"); err != nil {
		t.Fatal(err)
	}
	got := read(t, c.out, "doc/articles/image_draw.html")
	if !strings.Contains(got, `href="/blog/image-draw"`) {
		t.Errorf("the stub does not point at the target:\n%s", got)
	}
	if !strings.Contains(got, `rel="canonical" href="/blog/image-draw"`) {
		t.Errorf("the stub has no canonical link, so the old path gets indexed:\n%s", got)
	}
	if len(c.queue) != 1 || c.queue[0] != "/blog/image-draw" {
		t.Errorf("queued %q, want the redirect target", c.queue)
	}
	if len(c.redirects) != 1 || c.redirects[0].status != 301 {
		t.Errorf("recorded %+v, want one 301", c.redirects)
	}
}

// /doc/faq/ and /doc/faq are one file on a static host, so the redirect between
// them has nowhere to go and must not be written over the page.
func TestCrawlRedirectToTheSameFile(t *testing.T) {
	c, done := crawlFixture(t)
	defer done()
	if err := c.fetch(context.Background(), "/doc/faq"); err != nil {
		t.Fatal(err)
	}
	if err := c.fetch(context.Background(), "/doc/faq/"); err != nil {
		t.Fatal(err)
	}
	if got := read(t, c.out, "doc/faq/index.html"); !strings.Contains(got, "Câu hỏi thường gặp") {
		t.Errorf("the redirect overwrote the page it points at:\n%s", got)
	}
	if len(c.redirects) != 0 {
		t.Errorf("recorded %+v, want nothing", c.redirects)
	}
}

// go.dev serves the design documents by redirecting to GitHub. The export
// cannot hold them, and a reader following that link should still get there.
func TestCrawlRedirectOffSite(t *testing.T) {
	c, done := crawlFixture(t)
	defer done()
	if err := c.fetch(context.Background(), "/design/go2draft"); err != nil {
		t.Fatal(err)
	}
	got := read(t, c.out, "design/go2draft/index.html")
	if !strings.Contains(got, "https://github.com/golang/proposal") {
		t.Errorf("the stub lost the target:\n%s", got)
	}
	if len(c.queue) != 0 {
		t.Errorf("queued %q, and none of it is on this site", c.queue)
	}
}

func TestWriteRedirects(t *testing.T) {
	c, done := crawlFixture(t)
	defer done()
	c.redirects = []rule{{from: "/b", to: "/two", status: 301}, {from: "/a", to: "/one", status: 302}}
	if err := c.writeRedirects(); err != nil {
		t.Fatal(err)
	}
	got := read(t, c.out, "_redirects")
	for _, want := range []string{
		"/pkg/* https://go.dev/pkg/:splat 302",
		"/tour/lesson/ /tour/lessons.json 200",
		"/a /one 302",
		"/b /two 301",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("_redirects has no line %q:\n%s", want, got)
		}
	}
	if strings.Index(got, "/a /one") > strings.Index(got, "/b /two") {
		t.Error("the recorded redirects are not sorted, so the file churns between runs")
	}
}

// TestWriteRedirectsHosts is the domain move, seen from the redirect table. The
// host rules have to come out above the path rules, because Pages takes the
// first line that matches and a bare /tour/lesson/ would otherwise answer for a
// request to a host that is only meant to redirect.
func TestWriteRedirectsHosts(t *testing.T) {
	c, done := crawlFixture(t)
	defer done()
	c.opts.Redirecting = []string{"godev-vn.pages.dev"}
	c.opts.Waiting = []string{"godev.vn"}
	if err := c.writeRedirects(); err != nil {
		t.Fatal(err)
	}
	got := read(t, c.out, "_redirects")
	wait := "https://godev.vn/* /placeholder.html 200"
	send := "https://godev-vn.pages.dev/* https://godev-vn.tamnd.com/:splat 301"
	for _, want := range []string{wait, send} {
		if !strings.Contains(got, want) {
			t.Fatalf("_redirects has no line %q:\n%s", want, got)
		}
	}
	if strings.Index(got, send) > strings.Index(got, "/tour/lesson/") {
		t.Error("a host rule is below a path rule, so the path rule answers first")
	}
}

// A checkout that names one address writes no host rules at all, which is the
// state a site with one address should be in.
func TestWriteRedirectsOneHost(t *testing.T) {
	c, done := crawlFixture(t)
	defer done()
	if err := c.writeRedirects(); err != nil {
		t.Fatal(err)
	}
	if got := read(t, c.out, "_redirects"); strings.Contains(got, "https://godev-vn.tamnd.com") {
		t.Errorf("_redirects sends the only host to itself:\n%s", got)
	}
}

func TestWritePlaceholder(t *testing.T) {
	c, done := crawlFixture(t)
	defer done()
	if err := c.writePlaceholder(); err != nil {
		t.Fatal(err)
	}
	got := read(t, c.out, placeholderFile)
	// It points at the site rather than being one, it says so in Vietnamese,
	// and it keeps itself out of a search index so that the domain's first
	// impression in a search result is not a holding page.
	for _, want := range []string{
		`href="https://godev-vn.tamnd.com/"`,
		`<html lang="vi">`,
		`name="robots" content="noindex"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the placeholder page has no %q:\n%s", want, got)
		}
	}
}

func TestWriteHeaders(t *testing.T) {
	c, done := crawlFixture(t)
	defer done()
	if err := c.writeHeaders(); err != nil {
		t.Fatal(err)
	}
	got := read(t, c.out, "_headers")
	// HTML revalidates every time and assets do not, which is the whole
	// decision in the file.
	for _, want := range []string{
		"Cache-Control: public, max-age=0, must-revalidate",
		"/css/*",
		"X-Content-Type-Options: nosniff",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("_headers has no %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Content-Security-Policy") {
		t.Error("a policy written blind breaks the tour and the playground")
	}
}

// TestWriteCNAME is the GitHub Pages half of SITE.md.
//
// No mirror means no file, and no file is what the deploy workflow reads as
// there being nothing to publish to Pages. That is the state today: a project
// page at tamnd.github.io/godev-vn would serve a site whose every absolute
// reference starts at the root of a domain it is not at.
func TestWriteCNAME(t *testing.T) {
	c, done := crawlFixture(t)
	defer done()

	if err := c.writeCNAME(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(c.out, "CNAME")); !os.IsNotExist(err) {
		t.Errorf("a CNAME was written with no mirror in SITE.md: %v", err)
	}

	c.opts.Mirrors = []string{"godev-vn-mirror.tamnd.com"}
	if err := c.writeCNAME(); err != nil {
		t.Fatal(err)
	}
	// One hostname and a newline. That is the whole of the interface, and a
	// CNAME with anything else in it is a custom domain GitHub does not accept.
	if got := read(t, c.out, "CNAME"); got != "godev-vn-mirror.tamnd.com\n" {
		t.Errorf("CNAME is %q", got)
	}

	// GitHub Pages takes one custom domain per repository, so a second mirror
	// is a setting nobody can act on from here and refusing the export over it
	// would be worse than saying which one was taken.
	c.opts.Mirrors = []string{"first.example", "second.example"}
	if err := c.writeCNAME(); err != nil {
		t.Fatal(err)
	}
	if got := read(t, c.out, "CNAME"); got != "first.example\n" {
		t.Errorf("CNAME is %q, want the first mirror", got)
	}
}
