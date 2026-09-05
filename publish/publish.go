// Package publish turns the running site into a directory of files.
//
// go.dev is a Go program, not a directory. It routes on the request host, it
// renders Markdown and present files at request time, and a few of its paths
// are served by talking to another service entirely. None of that survives on
// Cloudflare Pages or GitHub Pages, which serve bytes off a disk and nothing
// else, so something has to run the program once and write down what it said.
//
// That is all this package is. It builds cmd/golangorg out of the checkout,
// starts it on a loopback port, walks it from the front page following every
// link it finds, and writes each answer where a static host will look for it.
// The pages that cannot be flattened are named in ProxyPrefixes and are left
// pointing at go.dev.
package publish

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Options is what a publish run needs to know. Everything has a default that
// works from a checkout of the site, because the common case is a CI job that
// passes an output directory and nothing else.
type Options struct {
	// Root is the godev-vn checkout to build and serve.
	Root string
	// Out is the directory to write, relative to Root when not absolute. It is
	// removed first, so it must not be anything but an export directory.
	Out string
	// Host is the hostname the site will be served under. Absolute go.dev
	// references in the HTML are rewritten to it.
	Host string
	// Redirecting are the other hostnames the same deploy answers on, each of
	// which sends a reader to Host permanently. One deploy serving several
	// names is how the move to a new domain stays a DNS record and a setting:
	// the names are all pointed at the same project, and which of them is real
	// is decided here rather than by which one has the files.
	Redirecting []string
	// Waiting are hostnames that serve the placeholder page rather than the
	// site or a redirect. A domain that is bought and not moved to yet should
	// say so to anyone who types it, which is not the same as being parked and
	// is not the same as pretending to be the site.
	Waiting []string
	// Mirrors are hostnames that serve these same files with no redirect, which
	// is the second deploy that exists so one vendor having a bad day is not the
	// whole site being down. A mirror does not compete with Host in a search
	// index, because the canonical link tag in every page it serves names Host.
	//
	// GitHub Pages takes exactly one custom domain per repository and reads it
	// out of a CNAME file at the root of what is published, so the first of
	// these is written there. It has to be a custom domain and not the project
	// page: tamnd.github.io/godev-vn puts the whole site under a path, and every
	// absolute reference in the export begins at the root.
	Mirrors []string
	// Addr is the loopback address to run the site on during the export.
	Addr string
	// Log receives one line per notable event. Nil is silence.
	Log func(format string, args ...any)
}

// Result counts what a run wrote, which is the only summary worth printing and
// the only thing a test can assert cheaply.
type Result struct {
	Pages     int
	Assets    int
	Redirects int
	Bytes     int64
	// Skipped holds the paths that answered with something other than a 2xx,
	// sorted. An export with a page missing is a real problem and a count of
	// failures does not say which one, so the paths are carried out whole.
	Skipped []string
	Out     string
}

// originHost is the host header every request carries.
//
// golangorg routes on the host: it serves the tour under one name, tip under
// another, and go.dev itself under this one. A request with a loopback host
// header does not get the site, it gets whatever the default mux does with it,
// so the crawl lies about who it is and asks for go.dev.
const originHost = "go.dev"

// Run exports the site and returns what it wrote.
func Run(ctx context.Context, opts Options) (Result, error) {
	var res Result
	if strings.TrimSpace(opts.Root) == "" {
		return res, errors.New("publish: no checkout to export")
	}
	if strings.TrimSpace(opts.Host) == "" {
		return res, errors.New("publish: no host to rewrite references to")
	}
	if opts.Addr == "" {
		opts.Addr = "127.0.0.1:8099"
	}
	if opts.Log == nil {
		opts.Log = func(string, ...any) {}
	}
	out := opts.Out
	if out == "" {
		out = "dist"
	}
	if !filepath.IsAbs(out) {
		out = filepath.Join(opts.Root, out)
	}
	res.Out = out

	site, stop, err := start(ctx, opts)
	if err != nil {
		return res, err
	}
	defer stop()

	if err := os.RemoveAll(out); err != nil {
		return res, err
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return res, err
	}

	c := &crawl{opts: opts, out: out, site: site, seen: map[string]bool{}}
	for _, seed := range seeds {
		c.enqueue(seed)
	}
	for len(c.queue) > 0 {
		p := c.queue[0]
		c.queue = c.queue[1:]
		if err := c.fetch(ctx, p); err != nil {
			return c.result(), err
		}
	}

	// The tour is an Angular application. It asks for its lessons and its
	// templates from JavaScript, so no href in any page points at them and the
	// crawl above cannot reach them. Without these three files the tour loads
	// its shell and then sits empty.
	if err := c.tour(ctx); err != nil {
		return c.result(), err
	}
	if err := c.writePlaceholder(); err != nil {
		return c.result(), err
	}
	if err := c.writeRedirects(); err != nil {
		return c.result(), err
	}
	if err := c.writeHeaders(); err != nil {
		return c.result(), err
	}
	if err := c.writeCNAME(); err != nil {
		return c.result(), err
	}
	return c.result(), nil
}

// seeds are the paths the crawl starts from.
//
// The front page reaches almost everything by link. The other two are asked for
// by a browser or a robot rather than linked to, so nothing would ever queue
// them.
var seeds = []string{"/", "/robots.txt", "/favicon.ico"}

type crawl struct {
	opts  Options
	out   string
	site  string
	queue []string
	seen  map[string]bool

	pages     int
	assets    int
	bytes     int64
	skipped   []string
	redirects []rule
}

// rule is one line of the redirect table.
type rule struct {
	from   string
	to     string
	status int
}

func (c *crawl) result() Result {
	sort.Strings(c.skipped)
	return Result{
		Pages:     c.pages,
		Assets:    c.assets,
		Redirects: len(c.redirects),
		Bytes:     c.bytes,
		Skipped:   c.skipped,
		Out:       c.out,
	}
}

func (c *crawl) enqueue(p string) {
	if p == "" || c.seen[p] || Proxied(p) {
		return
	}
	c.seen[p] = true
	c.queue = append(c.queue, p)
}

func (c *crawl) fetch(ctx context.Context, p string) error {
	a, err := get(ctx, c.site, p)
	if err != nil {
		return err
	}
	if a.location != nil {
		return c.redirect(p, a)
	}
	if a.status < 200 || a.status >= 300 {
		c.opts.Log("skip %s: %d", p, a.status)
		c.skipped = append(c.skipped, fmt.Sprintf("%s (%d)", p, a.status))
		return nil
	}
	switch {
	case strings.Contains(a.ctype, "text/html"):
		html := Rewrite(string(a.body), c.opts.Host)
		for _, link := range Links(html, p, c.opts.Host) {
			c.enqueue(link)
		}
		c.pages++
		return c.write(pagePath(p), []byte(html))
	case strings.Contains(a.ctype, "text/css"):
		for _, ref := range CSSRefs(string(a.body), p) {
			c.enqueue(ref)
		}
	}
	c.assets++
	return c.write(assetPath(p, a.ctype), a.body)
}

// redirect records where a path goes and leaves something at the old path that
// takes a reader there.
//
// Cloudflare Pages reads _redirects and will answer with a real 301. GitHub
// Pages has nothing of the kind, so the same fact is also written as a page
// with a meta refresh in it. Whichever of the two the host honours, the link
// works, and the canonical link in the stub keeps a search engine from indexing
// the old path as a page of its own.
func (c *crawl) redirect(from string, a answer) error {
	to := a.location
	target := to.String()
	if to.Host == originHost {
		target = to.EscapedPath()
		if target == "" {
			target = "/"
		}
		if to.RawQuery != "" {
			target += "?" + to.RawQuery
		}
		c.enqueue(strings.SplitN(target, "?", 2)[0])
		if Proxied(target) {
			// It leaves the export either way, so send readers to the service
			// that answers rather than to a path with no file behind it.
			target = "https://" + originHost + target
		} else if pagePath(from) == pagePath(strings.SplitN(target, "?", 2)[0]) {
			// /blog/context/ redirects to /blog/context, and a static host
			// serves both of those from blog/context/index.html. Writing a stub
			// would overwrite the page with a redirect to itself.
			return nil
		}
	}
	status := a.status
	if status != 301 && status != 302 && status != 307 && status != 308 {
		status = 302
	}
	c.redirects = append(c.redirects, rule{from: from, to: target, status: status})
	return c.write(pagePath(from), []byte(stub(target)))
}

// stub is the page left at a redirected path for hosts that cannot redirect.
func stub(target string) string {
	esc := html.EscapeString(target)
	return "<!DOCTYPE html>\n" +
		"<html lang=\"vi\">\n<head>\n" +
		"<meta charset=\"utf-8\">\n" +
		"<title>Đã chuyển hướng</title>\n" +
		"<link rel=\"canonical\" href=\"" + esc + "\">\n" +
		"<meta http-equiv=\"refresh\" content=\"0; url=" + esc + "\">\n" +
		"</head>\n<body>\n" +
		"<p>Trang này đã chuyển tới <a href=\"" + esc + "\">" + esc + "</a>.</p>\n" +
		"</body>\n</html>\n"
}

// tour writes the three files the tour fetches from JavaScript.
//
// /tour/lesson/ answers with every lesson in one JSON document. The application
// asks for it under that path, but a static host cannot serve a directory as
// JSON, so it lands as /tour/lessons.json and the deploy layer maps the one to
// the other. This is the arrangement tamnd/godev-worker already runs.
func (c *crawl) tour(ctx context.Context) error {
	a, err := get(ctx, c.site, "/tour/lesson/")
	if err != nil {
		return err
	}
	if a.status < 200 || a.status >= 300 {
		c.opts.Log("skip tour lessons: %d", a.status)
		c.skipped = append(c.skipped, fmt.Sprintf("/tour/lesson/ (%d)", a.status))
	} else {
		c.assets++
		if err := c.write("tour/lessons.json", a.body); err != nil {
			return err
		}
	}
	for _, partial := range []string{"editor.html", "list.html"} {
		p := "/tour/static/partials/" + partial
		if c.seen[p] {
			continue
		}
		c.seen[p] = true
		// Not through fetch: these answer as text/html and are Angular
		// templates rather than pages, so rewriting them and crawling out of
		// them would be wrong on both counts.
		a, err := get(ctx, c.site, p)
		if err != nil {
			return err
		}
		if a.status < 200 || a.status >= 300 {
			c.opts.Log("skip %s: %d", p, a.status)
			c.skipped = append(c.skipped, fmt.Sprintf("%s (%d)", p, a.status))
			continue
		}
		c.assets++
		if err := c.write(assetPath(p, a.ctype), a.body); err != nil {
			return err
		}
	}
	return nil
}

func (c *crawl) write(rel string, body []byte) error {
	dest := filepath.Join(c.out, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(dest, body, 0o644); err != nil {
		return err
	}
	c.bytes += int64(len(body))
	return nil
}

// pagePath is where a static host looks for the HTML of a request path.
//
// A path already ending in .html keeps its name, so /doc/effective_go.html
// lands as one file. Everything else becomes a directory with an index.html in
// it, which is what makes /doc/faq and /doc/faq/ both work on Pages.
//
// The test is for that one suffix rather than for any extension, because the
// blog names posts after releases: path.Ext("/blog/go1.23") is ".23", and a
// rule about extensions would write half the blog to a file with no index.
func pagePath(p string) string {
	clean := strings.Trim(p, "/")
	if clean == "" {
		return "index.html"
	}
	if strings.HasSuffix(clean, ".html") || strings.HasSuffix(clean, ".htm") {
		return clean
	}
	return path.Join(clean, "index.html")
}

func assetPath(p, ctype string) string {
	clean := strings.TrimLeft(p, "/")
	if clean == "" || strings.HasSuffix(clean, "/") {
		// An asset served from a directory path has no filename to take. It
		// happens for at most a handful of paths and a name derived from the
		// type is better than dropping the bytes.
		ext := "bin"
		if i := strings.Index(ctype, "/"); i > 0 {
			ext = strings.TrimSpace(strings.Split(ctype[i+1:], ";")[0])
		}
		return path.Join(clean, "index."+ext)
	}
	return clean
}

// answer is one reply from the site.
type answer struct {
	body   []byte
	ctype  string
	status int
	// location is the resolved Location header, empty unless status is a
	// redirect. It is resolved against go.dev rather than the loopback address,
	// so its host says whether the target is on this site.
	location *url.URL
}

// get asks the local site for one path and does not follow what it says.
//
// Following redirects is the obvious thing and it is wrong here. golangorg
// redirects /doc/articles/image_draw.html to /blog/image-draw, and an export
// that followed it would write the article's HTML at the old path. The article
// loads its figures with relative hrefs, so every one of them would then
// resolve under /doc/articles/ where no figure exists. That was seven broken
// images in the first run of this, and the same shape of breakage under
// /doc/gopher/, /blog/context/ and /blog/pipelines/.
//
// So a redirect stays a redirect and the target gets crawled on its own.
func get(ctx context.Context, site, p string) (answer, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, site+p, nil)
	if err != nil {
		return answer{}, err
	}
	req.Host = originHost
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := noRedirect.Do(req)
	if err != nil {
		return answer{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return answer{}, err
	}
	a := answer{body: body, ctype: resp.Header.Get("Content-Type"), status: resp.StatusCode}
	if loc := resp.Header.Get("Location"); a.status >= 300 && a.status < 400 && loc != "" {
		next, err := url.Parse(loc)
		if err == nil {
			base := &url.URL{Scheme: "https", Host: originHost, Path: p}
			a.location = base.ResolveReference(next)
		}
	}
	return a, nil
}

var noRedirect = &http.Client{
	Timeout: 2 * time.Minute,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// start builds the site and runs it, and returns the origin to ask and the
// function that stops it.
//
// It builds rather than using go run. go run compiles to a temporary file and
// executes it as a child, so killing go run leaves the server holding the port,
// and the next run of this command fails to bind with no clue why.
func start(ctx context.Context, opts Options) (string, func(), error) {
	dir, err := os.MkdirTemp("", "godev-publish-")
	if err != nil {
		return "", nil, err
	}
	bin := filepath.Join(dir, "golangorg")
	build := exec.CommandContext(ctx, "go", "build", "-o", bin, "./cmd/golangorg")
	build.Dir = opts.Root
	build.Stderr = os.Stderr
	opts.Log("building cmd/golangorg from %s", opts.Root)
	if err := build.Run(); err != nil {
		os.RemoveAll(dir)
		return "", nil, fmt.Errorf("publish: building the site: %w", err)
	}

	// -tip, -wiki and -gopls each clone a git repository at startup. They are
	// off by default anywhere but App Engine, and they are named here so that a
	// future default cannot turn a two second export into a two minute one.
	srv := exec.Command(bin, "-http="+opts.Addr, "-tip=false", "-wiki=false", "-gopls=false")
	srv.Dir = opts.Root
	srv.Stderr = os.Stderr
	// The overlay is on unless GODEV_CONTENT says en, and an export that
	// inherited that from a shell would quietly publish the English site.
	srv.Env = append(os.Environ(), "GODEV_CONTENT=vi")
	if err := srv.Start(); err != nil {
		os.RemoveAll(dir)
		return "", nil, err
	}
	stop := func() {
		if srv.Process != nil {
			srv.Process.Kill()
			srv.Wait()
		}
		os.RemoveAll(dir)
	}

	origin := "http://" + opts.Addr
	for i := 0; i < 120; i++ {
		if a, err := get(ctx, origin, "/"); err == nil && a.status > 0 {
			opts.Log("site is up on %s", origin)
			return origin, stop, nil
		}
		select {
		case <-ctx.Done():
			stop()
			return "", nil, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	stop()
	return "", nil, fmt.Errorf("publish: the site never answered on %s", opts.Addr)
}

// writeRedirects writes the table Cloudflare Pages reads.
//
// Three kinds of line go in it. The prefixes go.dev serves from another service
// go back to go.dev, because nothing here can answer them. The tour's lesson
// endpoint is rewritten to the file the export wrote it to. And every redirect
// the site itself issued is repeated, so that Pages answers those with a status
// rather than with the meta refresh stub that is there for GitHub Pages.
func (c *crawl) writeRedirects() error {
	var b strings.Builder
	b.WriteString("# Written by godev publish. Do not edit.\n")
	// The host rules go first. Pages takes the first line that matches, and a
	// path rule below would otherwise answer for a request to a host that is
	// only meant to redirect: somebody typing godev.vn/pkg/fmt while the domain
	// is waiting would be sent to go.dev rather than told where the site is.
	if len(c.opts.Waiting) > 0 {
		b.WriteString("\n# Bought, pointed here, and not the address yet. See SITE.md.\n")
		for _, host := range c.opts.Waiting {
			fmt.Fprintf(&b, "https://%s/* /%s 200\n", host, placeholderFile)
		}
	}
	if len(c.opts.Redirecting) > 0 {
		b.WriteString("\n# Other names for this deploy. The real one is in SITE.md.\n")
		for _, host := range c.opts.Redirecting {
			fmt.Fprintf(&b, "https://%s/* https://%s/:splat 301\n", host, c.opts.Host)
		}
	}
	b.WriteString("\n# Served by a program on go.dev, not by a file here.\n")
	for _, prefix := range ProxyPrefixes {
		fmt.Fprintf(&b, "%s https://go.dev%s 302\n", prefix, prefix)
		fmt.Fprintf(&b, "%s/* https://go.dev%s/:splat 302\n", prefix, prefix)
	}
	// The tour asks for its lessons under a path a static host reads as a
	// directory. This is the one rewrite the site itself needs.
	b.WriteString("\n# The tour loads every lesson from this one path.\n")
	b.WriteString("/tour/lesson/ /tour/lessons.json 200\n")
	b.WriteString("/tour/lesson /tour/lessons.json 200\n")
	if len(c.redirects) > 0 {
		b.WriteString("\n# Redirects the site issued when it was crawled.\n")
		sort.Slice(c.redirects, func(i, j int) bool { return c.redirects[i].from < c.redirects[j].from })
		for _, r := range c.redirects {
			fmt.Fprintf(&b, "%s %s %d\n", r.from, r.to, r.status)
		}
	}
	return c.write("_redirects", []byte(b.String()))
}

// placeholderFile is where the page for a bought and unmoved domain lands.
//
// A real path and not the root, because the root is the site. A host in Waiting
// is rewritten onto it for every path it is asked for, so somebody who follows a
// deep link into the future domain gets the explanation rather than a 404.
const placeholderFile = "placeholder.html"

// writePlaceholder writes the page a domain shows while it is waiting.
//
// It is written whether or not any host is waiting, because it costs 900 bytes
// and because the alternative is that the file appears on the deploy where the
// redirect table first references it, which is the deploy where nobody is
// looking at it yet.
func (c *crawl) writePlaceholder() error {
	host := c.opts.Host
	page := "<!DOCTYPE html>\n" +
		"<html lang=\"vi\">\n<head>\n" +
		"<meta charset=\"utf-8\">\n" +
		"<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n" +
		"<title>go.dev tiếng Việt</title>\n" +
		"<meta name=\"robots\" content=\"noindex\">\n" +
		"<link rel=\"canonical\" href=\"https://" + html.EscapeString(host) + "/\">\n" +
		"<style>\n" +
		"body{font-family:system-ui,sans-serif;line-height:1.6;margin:0 auto;padding:3rem 1.5rem;max-width:34rem;color:#1a1a1a}\n" +
		"a{color:#007d9c}\n" +
		"</style>\n" +
		"</head>\n<body>\n" +
		"<h1>go.dev tiếng Việt</h1>\n" +
		"<p>Tên miền này dành cho bản tiếng Việt của go.dev. Trang web đang chạy tại\n" +
		"<a href=\"https://" + html.EscapeString(host) + "/\">" + html.EscapeString(host) + "</a>\n" +
		"và sẽ chuyển về đây khi sẵn sàng.</p>\n" +
		"<p>Bản dịch là mã nguồn mở tại\n" +
		"<a href=\"https://github.com/tamnd/godev-vn\">github.com/tamnd/godev-vn</a>.</p>\n" +
		"</body>\n</html>\n"
	return c.write(placeholderFile, []byte(page))
}

// writeHeaders writes the table Cloudflare Pages reads for response headers.
//
// Two decisions in it. HTML is revalidated on every request, because a
// documentation site's whole value is that the page you are reading is the
// current one, and a translation that lands in the morning being served from a
// cache until the afternoon is the failure mode nobody notices. The assets get
// an hour, which is long enough to matter on a page that loads a dozen of them
// and short enough that a stylesheet change is not stuck.
//
// The asset paths are not content addressed on this site. `/css/styles.css` is
// the real name of the real file and it keeps that name across deploys, so the
// year-long immutable caching that a hashed asset pipeline earns would be wrong
// here and would strand a reader on last month's layout.
//
// The security headers are the two that cost nothing. There is deliberately no
// Content-Security-Policy: the tour runs a compiler in a web worker and the
// playground posts to another origin, and a policy written without being able
// to load either of those is a policy that breaks them.
func (c *crawl) writeHeaders() error {
	const table = `# Written by godev publish. Do not edit.

/*
  X-Content-Type-Options: nosniff
  Referrer-Policy: strict-origin-when-cross-origin
  Cache-Control: public, max-age=0, must-revalidate

/css/*
  Cache-Control: public, max-age=3600, stale-while-revalidate=86400

/js/*
  Cache-Control: public, max-age=3600, stale-while-revalidate=86400

/fonts/*
  Cache-Control: public, max-age=604800, stale-while-revalidate=604800

/images/*
  Cache-Control: public, max-age=604800, stale-while-revalidate=604800
`
	return c.write("_headers", []byte(table))
}

// writeCNAME names the custom domain for the GitHub Pages mirror.
//
// One file with one hostname in it and nothing else, which is the whole of the
// GitHub Pages custom domain interface. Writing it here rather than committing
// it to the site repo keeps every hostname this project answers on in SITE.md,
// which is the only arrangement where moving to a new domain stays one edit.
//
// No mirror in SITE.md means no file, and no file means the deploy workflow has
// nothing to publish to Pages, which is the correct behaviour today: there is no
// mirror host yet, and publishing to tamnd.github.io/godev-vn would serve a site
// whose every absolute reference starts at the root of a domain it is not at.
//
// The first mirror wins if somebody lists two. GitHub Pages takes one custom
// domain per repository, so the alternative is refusing the export over a
// setting nobody can act on from here, and the log line says which one it took.
func (c *crawl) writeCNAME() error {
	if len(c.opts.Mirrors) == 0 {
		return nil
	}
	host := c.opts.Mirrors[0]
	if len(c.opts.Mirrors) > 1 {
		c.opts.Log("%d mirrors in SITE.md and GitHub Pages takes one, so the CNAME says %s",
			len(c.opts.Mirrors), host)
	}
	return c.write("CNAME", []byte(host+"\n"))
}
