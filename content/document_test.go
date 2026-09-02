package content

import "testing"

// The link extractor is what L07 is built on, so a link it cannot see is a
// dropped link as far as the gate is concerned, and a link it sees twice is a
// finding on a page that is right.
func TestLinksOf(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []Link
	}{
		{
			name: "a plain link",
			body: "See the [standard library](/pkg) for more.",
			want: []Link{{Text: "standard library", Target: "/pkg", Line: 1}},
		},
		{
			// The one that started this. Markdown lets the text wrap and the
			// renderer joins it, so this is one link and not none.
			name: "the text wraps",
			body: "Go CNA bao gồm ([thư viện\nchuẩn](/pkg) và\nnhững thứ khác).",
			want: []Link{{Text: "thư viện\nchuẩn", Target: "/pkg", Line: 1}},
		},
		{
			// The line is the line the link opens on, which is where a person
			// looking for it will start reading.
			name: "the line is where it opens",
			body: "one\ntwo\nthree [a](/b) four",
			want: []Link{{Text: "a", Target: "/b", Line: 3}},
		},
		{
			name: "an image",
			body: "![MVS upgrade](/doc/mvs/upgrade.svg)",
			want: []Link{{Image: true, Text: "MVS upgrade", Target: "/doc/mvs/upgrade.svg", Line: 1}},
		},
		{
			name: "a title after the target",
			body: `[a](/b "the title")`,
			want: []Link{{Text: "a", Target: "/b", Title: "the title", Line: 1}},
		},
		{
			// Code is not prose. A fenced block full of Markdown examples would
			// otherwise put links into the count that no reader can click.
			name: "a link inside a fence does not count",
			body: "before [a](/b)\n```\n[c](/d)\n```\nafter [e](/f)",
			want: []Link{
				{Text: "a", Target: "/b", Line: 1},
				{Text: "e", Target: "/f", Line: 5},
			},
		},
		{
			// Blanking the fence has to keep byte offsets true or every line
			// number after a fence is wrong, and Vietnamese is three bytes a
			// letter in places where English is one.
			name: "the line survives a fence of multibyte text",
			body: "```\ntiếng Việt trong khối mã\n```\n[a](/b)",
			want: []Link{{Text: "a", Target: "/b", Line: 4}},
		},
		{
			name: "two on one line",
			body: "[a](/b) and [c](/d)",
			want: []Link{
				{Text: "a", Target: "/b", Line: 1},
				{Text: "c", Target: "/d", Line: 1},
			},
		},
		{
			// A target may not contain a space, in Markdown or here, so this is
			// bracket text next to parenthesis text and not a link.
			name: "not a link",
			body: "[a] (b c)",
			want: nil,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := linksOf(c.body)
			if len(got) != len(c.want) {
				t.Fatalf("got %d links, want %d: %+v", len(got), len(c.want), got)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("link %d = %+v, want %+v", i, got[i], c.want[i])
				}
			}
		})
	}
}

// The comment extractor has two jobs beyond finding <!-- -->: it must not count
// the site's own page metadata, which is written as a comment and is the one
// comment on the site that gets translated, and it must not count a comment
// written inside an example.
func TestComments(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []string
	}{
		{
			name: "a plain comment",
			body: "<!-- CL 29072 -->\ntext\n",
			want: []string{" CL 29072 "},
		},
		{
			name: "html page metadata is not a comment",
			body: "<!--{\n\t\"Title\": \"Tải xuống\"\n}-->\n\n<p>text</p>\n<!-- for consistent spacing -->\n",
			want: []string{" for consistent spacing "},
		},
		{
			name: "it spans lines",
			body: "<!-- one\ntwo -->\n",
			want: []string{" one\ntwo "},
		},
		{
			name: "a comment inside a fence does not count",
			body: "<!-- a -->\n```html\n<!-- b -->\n```\n<!-- c -->\n",
			want: []string{" a ", " c "},
		},
		{
			name: "two on one line",
			body: "<b>a</b><!-- x --><b>b</b><!-- y -->",
			want: []string{" x ", " y "},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := comments(c.body)
			if len(got) != len(c.want) {
				t.Fatalf("got %d comments, want %d: %q", len(got), len(c.want), got)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("comment %d = %q, want %q", i, got[i], c.want[i])
				}
			}
		})
	}
}
