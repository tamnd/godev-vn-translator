package publish

import (
	"reflect"
	"testing"
)

// The rewrite is the part of the export that can be wrong quietly. A crawl that
// misses a page shows up as a 404 the first time anybody clicks it, but a
// canonical link left pointing at go.dev looks right in a browser and tells
// every search engine that this site is a duplicate.
func TestRewrite(t *testing.T) {
	const host = "godev-vn.tamnd.com"
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			"canonical link",
			`<link rel="canonical" href="https://go.dev/doc/">`,
			`<link rel="canonical" href="https://godev-vn.tamnd.com/doc/">`,
		},
		{
			"scheme relative",
			`<img src="//go.dev/images/gophers.svg">`,
			`<img src="//godev-vn.tamnd.com/images/gophers.svg">`,
		},
		{
			"bare host with no path",
			`<a href="https://go.dev">go.dev</a>`,
			`<a href="https://godev-vn.tamnd.com">go.dev</a>`,
		},
		{
			// The link text is not a URL and must not become one. Only the href
			// changed above, and the visible go.dev in it stayed.
			"prose naming the site",
			`Trang go.dev nói vậy.`,
			`Trang go.dev nói vậy.`,
		},
		{
			// This is the whole reason the prefix list is in this file.
			// pkg.go.dev is a different service and /pkg is not in the export.
			"package link is left alone",
			`<a href="https://go.dev/pkg/net/http">net/http</a>`,
			`<a href="https://go.dev/pkg/net/http">net/http</a>`,
		},
		{
			"playground link is left alone",
			`<a href="https://go.dev/play/p/abc">chạy thử</a>`,
			`<a href="https://go.dev/play/p/abc">chạy thử</a>`,
		},
		{
			// /doc is not a prefix, and a prefix match on the string rather
			// than on the path segment would catch /dl here.
			"a path that only starts like a prefix",
			`<a href="https://go.dev/dlopen">x</a>`,
			`<a href="https://godev-vn.tamnd.com/dlopen">x</a>`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Rewrite(c.in, host); got != c.want {
				t.Errorf("Rewrite(%q)\n got %q\nwant %q", c.in, got, c.want)
			}
		})
	}
}

func TestProxied(t *testing.T) {
	yes := []string{"/pkg", "/pkg/", "/pkg/net/http", "/play/p/x", "/cl/12345", "/dl", "/wiki/Home"}
	no := []string{"/", "/doc/", "/dlopen", "/blog/go1.23", "/packages", "/security/policy", "/tour/welcome/1"}
	for _, p := range yes {
		if !Proxied(p) {
			t.Errorf("Proxied(%q) = false, want true", p)
		}
	}
	for _, p := range no {
		if Proxied(p) {
			t.Errorf("Proxied(%q) = true, want false", p)
		}
	}
}

func TestLinks(t *testing.T) {
	const host = "godev-vn.tamnd.com"
	html := `
		<a href="/doc/">tài liệu</a>
		<a href="tutorial/">tương đối</a>
		<a href="https://godev-vn.tamnd.com/blog/">tuyệt đối</a>
		<a href="https://go.dev/pkg/fmt">gói</a>
		<a href="https://example.com/x">ngoài</a>
		<a href="#top">neo</a>
		<a href="mailto:a@b.c">thư</a>
		<link href="/css/styles.css">
		<script src="/js/site.js"></script>
		<a href="/blog/?page=2">có truy vấn</a>
	`
	got := Links(html, "/doc/faq/", host)
	want := []string{
		"/doc/",
		"/doc/faq/tutorial/",
		"/blog/",
		"/css/styles.css",
		"/js/site.js",
		"/blog/",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Links\n got %q\nwant %q", got, want)
	}
}

// A trailing slash is the difference between a directory with an index.html in
// it and a file, so resolve has to keep it and path.Clean has to not eat it.
func TestLinksKeepsTrailingSlash(t *testing.T) {
	got := Links(`<a href="/doc/">x</a><a href="/doc/faq">y</a>`, "/", "h")
	want := []string{"/doc/", "/doc/faq"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Links\n got %q\nwant %q", got, want)
	}
}

func TestCSSRefs(t *testing.T) {
	css := `
		@font-face { src: url("/fonts/inter.woff2") format("woff2"); }
		.hero { background: url(../images/hero.svg) no-repeat; }
		.icon { background-image: url('data:image/svg+xml;base64,AAA'); }
	`
	got := CSSRefs(css, "/css/styles.css")
	want := []string{"/fonts/inter.woff2", "/images/hero.svg"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("CSSRefs\n got %q\nwant %q", got, want)
	}
}

// The output path is where a static host will look, and the two hosts look in
// different places for the same request unless the file is an index.html in a
// directory named for the path.
func TestPagePath(t *testing.T) {
	cases := map[string]string{
		"/":                      "index.html",
		"/doc/":                  "doc/index.html",
		"/doc/faq":               "doc/faq/index.html",
		"/doc/effective_go.html": "doc/effective_go.html",
		"/blog/go1.23":           "blog/go1.23/index.html",
	}
	for in, want := range cases {
		if got := pagePath(in); got != want {
			t.Errorf("pagePath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAssetPath(t *testing.T) {
	if got := assetPath("/css/styles.css", "text/css"); got != "css/styles.css" {
		t.Errorf("assetPath = %q", got)
	}
	if got := assetPath("/tour/lesson/", "application/json"); got != "tour/lesson/index.json" {
		t.Errorf("assetPath = %q", got)
	}
}
