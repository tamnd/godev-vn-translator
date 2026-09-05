// Package site is the list of hostnames the published site answers on, and
// which one of them is the real address.
//
// It is read from SITE.md in the site repo rather than kept here, for the same
// reason the glossary and the manifest are: it is a fact about that site, it is
// edited by whoever runs it, and moving to a new domain should be a pull request
// against the site and not a release of this tool.
//
// The file is Markdown with a line and a table in it, which is the shape
// GLOSSARY.md already uses and for the same reason. A settings file that is only
// machine readable stops being maintained, and this one has to explain itself:
// the whole point of it is that somebody who has not thought about DNS in a year
// can open it, read why godev.vn is listed and not serving, change one line, and
// be done.
//
// That one line is the design. Everything else about a domain move is derived
// from which host is named canonical, so the move is one edit and not a
// migration:
//
//	Canonical: godev-vn.tamnd.com
//
//	| host | role | note |
//	| ---- | ---- | ---- |
//	| godev-vn.tamnd.com | canonical | ... |
//	| godev.vn | placeholder | ... |
//
// The canonical host is what canonical link tags, the feed and any absolute
// reference in the HTML are rewritten to. Every other host does what its role
// says: redirect to the canonical one, serve the placeholder page, or serve the
// same files as a mirror. A role on the canonical host is ignored, so naming
// godev.vn canonical is the whole of the move.
package site

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// File is the settings file in the site repo.
const File = "SITE.md"

// DefaultHost is the address used when a checkout has no SITE.md.
//
// A checkout without the file is not an error. The file arrived after the
// exporter did, an older commit does not have it, and a publish that refused to
// run without it would be a publish nobody could run against history.
const DefaultHost = "godev-vn.tamnd.com"

// The roles a host can have in the table. The canonical host's role is not one
// of these and is not read: which host is canonical is the line above the table,
// and that is the whole point of the file.
const (
	// RoleRedirect sends a reader to the canonical host, permanently. It is
	// what an old address and a hosting provider's own name should do, so that
	// the site has one address in a search result and in a bookmark.
	RoleRedirect = "redirect"
	// RolePlaceholder serves the placeholder page. A domain that is bought and
	// not moved to yet should say so to whoever types it.
	RolePlaceholder = "placeholder"
	// RoleMirror serves the same files with no redirect, for the deploy that
	// exists so the site survives one vendor having a bad day. It does not
	// compete with the canonical host in a search index, because the canonical
	// link tag in every page it serves names the canonical host.
	RoleMirror = "mirror"
)

// Host is one name the site answers on.
type Host struct {
	Name string
	// Role is one of the constants above. An unrecognised role is treated as a
	// redirect, which is the safe reading: a host somebody added to the table
	// and did not classify should send readers home rather than quietly serve a
	// second copy of the site.
	Role string
	// Note is the third column, which says why. Nothing reads it and it is the
	// reason the file is Markdown.
	Note string
}

// Config is the whole file.
type Config struct {
	// Canonical is the one real address. Empty when the file names none, which
	// the caller resolves to DefaultHost.
	Canonical string
	Hosts     []Host
	// Path is where it was read from, so a report can cite it.
	Path string
}

var (
	canonicalRE = regexp.MustCompile(`(?im)^\s*canonical\s*:\s*(\S+)\s*$`)
	rowRE       = regexp.MustCompile(`^\s*\|(.+)\|\s*$`)
)

// Load reads the settings from a checkout. A checkout without SITE.md gets nil
// and no error.
func Load(root string) (*Config, error) {
	path := filepath.Join(root, File)
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	c := Parse(string(raw))
	c.Path = path
	return c, nil
}

// Parse reads the canonical line and every table row.
//
// The first canonical line wins. There should only ever be one, and taking the
// first means a document that quotes the syntax further down while explaining it
// does not silently change the setting.
func Parse(text string) *Config {
	var c Config
	if m := canonicalRE.FindStringSubmatch(text); m != nil {
		c.Canonical = strings.ToLower(strings.Trim(m[1], "`"))
	}
	seen := map[string]bool{}
	for line := range strings.SplitSeq(text, "\n") {
		m := rowRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		cells := strings.Split(m[1], "|")
		if len(cells) < 2 {
			continue
		}
		name := strings.ToLower(strings.Trim(strings.TrimSpace(cells[0]), "`"))
		role := strings.ToLower(strings.TrimSpace(cells[1]))
		note := ""
		if len(cells) > 2 {
			note = strings.TrimSpace(cells[2])
		}
		// The header row and the --- separator under it are rows too, and so is
		// anything that does not look like a name with a dot in it.
		if name == "" || !strings.Contains(name, ".") || seen[name] {
			continue
		}
		seen[name] = true
		c.Hosts = append(c.Hosts, Host{Name: name, Role: role, Note: note})
	}
	return &c
}

// Host is the canonical address, or DefaultHost when the file names none.
func (c *Config) Host() string {
	if c == nil || c.Canonical == "" {
		return DefaultHost
	}
	return c.Canonical
}

// Redirecting returns the hosts that send a reader to the canonical one, in the
// order the file lists them.
//
// A file that lists only the canonical host produces nothing, which is the state
// a site with one address should be in.
func (c *Config) Redirecting() []string {
	return c.withRole(RoleRedirect)
}

// Waiting returns the hosts that serve the placeholder page.
func (c *Config) Waiting() []string {
	return c.withRole(RolePlaceholder)
}

// Mirrors returns the hosts that serve the same files with no redirect.
func (c *Config) Mirrors() []string {
	return c.withRole(RoleMirror)
}

// withRole is where the one edit rule lives.
//
// The canonical host is never returned, whatever its row says, and that is what
// makes the move a single edit. godev.vn is listed as a placeholder from the day
// it is bought, and the day the canonical line names it, its row stops meaning
// anything without anybody having to remember to change it.
//
// A role the file does not recognise counts as a redirect, so a host somebody
// added and did not classify sends readers home rather than serving a second
// copy of the site under another name.
func (c *Config) withRole(role string) []string {
	if c == nil {
		return nil
	}
	canonical := c.Host()
	var out []string
	for _, h := range c.Hosts {
		if h.Name == canonical {
			continue
		}
		switch h.Role {
		case RolePlaceholder, RoleMirror:
			if h.Role != role {
				continue
			}
		default:
			if role != RoleRedirect {
				continue
			}
		}
		out = append(out, h.Name)
	}
	return out
}
