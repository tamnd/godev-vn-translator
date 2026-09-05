package site

import (
	"strings"
	"testing"
)

const example = `# The addresses this site answers on

Canonical: godev-vn.tamnd.com

| host | role | note |
| ---- | ---- | ---- |
| godev-vn.tamnd.com | canonical | The address today. |
| godev-vn.pages.dev | redirect | What Cloudflare Pages names the project. |
| godev.vn | placeholder | Bought and not moved to yet. |
| godev-vn-mirror.tamnd.com | mirror | GitHub Pages, so one vendor is not the whole site. |
`

func TestParse(t *testing.T) {
	c := Parse(example)
	if c.Host() != "godev-vn.tamnd.com" {
		t.Fatalf("canonical is %q", c.Host())
	}
	if len(c.Hosts) != 4 {
		t.Fatalf("got %d hosts, want 4: %+v", len(c.Hosts), c.Hosts)
	}
	if got, want := c.Redirecting(), []string{"godev-vn.pages.dev"}; !equal(got, want) {
		t.Errorf("redirecting is %q, want %q", got, want)
	}
	if got, want := c.Waiting(), []string{"godev.vn"}; !equal(got, want) {
		t.Errorf("waiting is %q, want %q", got, want)
	}
	// A mirror is not a redirect. It serves the same files, and the canonical
	// link tag in each of them is what keeps it out of a search result.
	if got, want := c.Mirrors(), []string{"godev-vn-mirror.tamnd.com"}; !equal(got, want) {
		t.Errorf("mirrors is %q, want %q", got, want)
	}
	if c.Hosts[2].Note != "Bought and not moved to yet." {
		t.Errorf("the note is %q", c.Hosts[2].Note)
	}
}

// TestUnclassifiedRedirects is the safe reading. A host in the table with a role
// nobody recognised sends readers to the canonical address rather than quietly
// serving a second copy of the site under another name.
func TestUnclassifiedRedirects(t *testing.T) {
	c := Parse("Canonical: a.example\n\n| host | role |\n| b.example | |\n| c.example | someday |\n")
	if got, want := c.Redirecting(), []string{"b.example", "c.example"}; !equal(got, want) {
		t.Errorf("redirecting is %q, want %q", got, want)
	}
	if got := c.Mirrors(); len(got) != 0 {
		t.Errorf("mirrors is %q, want none", got)
	}
}

// TestTheMove is the whole design in one test. Changing the canonical line and
// nothing else has to move the site, turn the old address into a redirect and
// stop the placeholder.
func TestTheMove(t *testing.T) {
	moved := Parse(replace(example, "Canonical: godev-vn.tamnd.com", "Canonical: godev.vn"))
	if moved.Host() != "godev.vn" {
		t.Fatalf("canonical is %q", moved.Host())
	}
	if got, want := moved.Redirecting(), []string{"godev-vn.tamnd.com", "godev-vn.pages.dev"}; !equal(got, want) {
		t.Errorf("redirecting is %q, want %q", got, want)
	}
	// The placeholder row is still in the file and now says nothing, which is
	// what stops the move being two edits.
	if got := moved.Waiting(); len(got) != 0 {
		t.Errorf("waiting is %q, want none", got)
	}
}

func TestNoFile(t *testing.T) {
	var c *Config
	if c.Host() != DefaultHost {
		t.Errorf("a checkout with no SITE.md gets %q, want %q", c.Host(), DefaultHost)
	}
	if got := c.Redirecting(); got != nil {
		t.Errorf("redirecting is %q, want none", got)
	}
	if got := c.Waiting(); got != nil {
		t.Errorf("waiting is %q, want none", got)
	}
}

func TestParseIsForgiving(t *testing.T) {
	// A file with a table and no canonical line still names its hosts, and the
	// address falls back rather than becoming empty.
	c := Parse("| host | role |\n| ---- | ---- |\n| a.example | redirect |\n")
	if c.Host() != DefaultHost {
		t.Errorf("canonical is %q, want the default", c.Host())
	}
	if len(c.Hosts) != 1 || c.Hosts[0].Name != "a.example" {
		t.Errorf("hosts are %+v", c.Hosts)
	}
	// The separator row under the header is a table row and is not a host, and
	// neither is a header cell. A name has a dot in it.
	if got := Parse("| host | role |\n| ---- | ---- |\n"); len(got.Hosts) != 0 {
		t.Errorf("got %+v, want no hosts", got.Hosts)
	}
	// Backticks around a name are how a person writes one in Markdown.
	if got := Parse("Canonical: `godev.vn`\n"); got.Host() != "godev.vn" {
		t.Errorf("canonical is %q", got.Host())
	}
	// The first canonical line wins, so a document that quotes the syntax
	// while explaining it does not change the setting.
	two := "Canonical: a.example\n\nWrite it as `Canonical: b.example`.\n"
	if got := Parse(two); got.Host() != "a.example" {
		t.Errorf("canonical is %q, want the first", got.Host())
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func replace(text, from, to string) string {
	out := strings.Replace(text, from, to, 1)
	if out == text {
		panic("replace: " + from + " is not in the example")
	}
	return out
}
