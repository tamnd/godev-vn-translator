package publish

import (
	"net/url"
	"path"
	"regexp"
	"strings"
)

// ProxyPrefixes are the paths go.dev does not serve from a file.
//
// This list is carried over from tamnd/godev-worker, which arrived at it by
// running the site behind a Worker and watching what broke. /pkg and /mod are
// pkg.go.dev, a different service with a different database. /play is the
// playground, which compiles and runs code. /dl reads the release index. /cl,
// /issue and /change are short links into Gerrit and GitHub. None of them can
// be a file in a directory, so the export does not visit them and the HTML
// keeps pointing at go.dev.
var ProxyPrefixes = []string{
	"/dl",
	"/play",
	"/pkg",
	"/cmd",
	"/mod",
	"/s",
	"/src",
	"/talks",
	"/misc",
	"/gopls",
	"/issue",
	"/issues",
	"/change",
	"/cl",
	"/wiki",
}

// Proxied reports whether a path belongs to another service.
func Proxied(p string) bool {
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p = p[:i]
	}
	for _, prefix := range ProxyPrefixes {
		if p == prefix || strings.HasPrefix(p, prefix+"/") {
			return true
		}
	}
	return false
}

// goDevRE matches a reference to go.dev in any of the three forms the templates
// write: with a scheme, without one, and bare.
var goDevRE = regexp.MustCompile(`(https?:)?//go\.dev(/[^"'\s<>)]*)?`)

// Rewrite points absolute go.dev references at the host being published to.
//
// The site writes its own name into canonical links, into Open Graph tags, into
// the feed, and into a handful of hrefs in the templates. Left alone, every one
// of those sends a reader of the Vietnamese site back to the English one, and
// the canonical link tells a search engine that this site is a copy that should
// not be indexed.
//
// References under ProxyPrefixes are left alone on purpose. Those paths do not
// exist in the export, so rewriting them would turn a working link to
// pkg.go.dev into a 404 on a static host.
func Rewrite(html, host string) string {
	return goDevRE.ReplaceAllStringFunc(html, func(m string) string {
		scheme, rest, ok := strings.Cut(m, "//")
		if !ok {
			return m
		}
		p := strings.TrimPrefix(rest, "go.dev")
		if Proxied(p) {
			return m
		}
		return scheme + "//" + host + p
	})
}

var linkRE = regexp.MustCompile(`\b(?:href|src)=["']([^"']+)["']`)

// Links returns the same site paths an exported page points at.
//
// The page has already been through Rewrite, so a link that survived as go.dev
// is a link to another service and is dropped here by the host check rather
// than by a second copy of the prefix list.
func Links(html, from, host string) []string {
	base := &url.URL{Scheme: "https", Host: host, Path: from}
	var out []string
	for _, m := range linkRE.FindAllStringSubmatch(html, -1) {
		out = append(out, resolve(base, host, m[1])...)
	}
	return out
}

var cssRefRE = regexp.MustCompile(`url\(\s*["']?([^"')]+)["']?\s*\)`)

// CSSRefs returns the paths a stylesheet loads.
//
// Fonts and background images are reached from CSS and from nowhere else, so a
// crawl that only reads HTML publishes a site with no fonts on it.
func CSSRefs(css, from string) []string {
	base := &url.URL{Scheme: "https", Host: "css.invalid", Path: from}
	var out []string
	for _, m := range cssRefRE.FindAllStringSubmatch(css, -1) {
		out = append(out, resolve(base, "css.invalid", m[1])...)
	}
	return out
}

// resolve turns one reference into at most one path on this site.
func resolve(base *url.URL, host, ref string) []string {
	ref = strings.TrimSpace(ref)
	switch {
	case ref == "", strings.HasPrefix(ref, "#"):
		return nil
	case strings.HasPrefix(ref, "mailto:"), strings.HasPrefix(ref, "javascript:"),
		strings.HasPrefix(ref, "data:"), strings.HasPrefix(ref, "tel:"):
		return nil
	}
	u, err := url.Parse(ref)
	if err != nil {
		return nil
	}
	u = base.ResolveReference(u)
	if u.Host != host {
		return nil
	}
	p := u.EscapedPath()
	if p == "" {
		p = "/"
	}
	// The query is dropped rather than kept. A static export has one file per
	// path and no way to vary on a query string, and every query on this site
	// is either a playground share or an analytics tag.
	if Proxied(p) {
		return nil
	}
	return []string{path.Clean(p) + trailingSlash(p)}
}

func trailingSlash(p string) string {
	if strings.HasSuffix(p, "/") && p != "/" {
		return "/"
	}
	return ""
}
