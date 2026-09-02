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

// The heading extractor has to know the format, because present(1) and Markdown
// disagree about what a hash at the start of a line means. In Markdown it opens
// a heading; in a .slide or a .article it opens a comment, and the headings are
// the star lines.
func TestHeadings(t *testing.T) {
	const markdown = "# Một\n\ntext\n\n## Hai\n"
	const present = `Title of the talk
Subtitle

# This is a comment in present(1), and it wraps
# onto a second line.

* Slide one

Some prose.

** A subsection

*** Deeper

# Another comment.

* Slide two
`
	cases := []struct {
		name string
		kind Kind
		body string
		want []string
	}{
		{"markdown", KindMarkdown, markdown, []string{"Một", "Hai"}},
		{
			"a slide counts stars",
			KindSlide, present,
			[]string{"Slide one", "A subsection", "Deeper", "Slide two"},
		},
		{
			"an article counts stars too",
			KindArticle, present,
			[]string{"Slide one", "A subsection", "Deeper", "Slide two"},
		},
		{
			// The bug this was written for. Reading a slide as Markdown counts
			// its comments, and a comment rewrapped by a translator changes the
			// count, so L04 refused a file that had lost nothing.
			"reading a slide as markdown counts the comments instead",
			KindMarkdown, present,
			[]string{
				"This is a comment in present(1), and it wraps",
				"onto a second line.",
				"Another comment.",
			},
		},
		{
			"a star inside a fence is not a heading",
			KindSlide, "* Real\n\n```\n* Not a heading\n```\n\n* Also real\n",
			[]string{"Real", "Also real"},
		},
		{
			"a bare star with no text is not a heading",
			KindSlide, "*\n*bold*\n* Real\n",
			[]string{"Real"},
		},
		{
			"a hash in a slide yields nothing when there are no stars",
			KindSlide, "# Only a comment.\n\ntext\n",
			nil,
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			doc := Parse(tt.kind, tt.body)
			var got []string
			for _, h := range doc.Headings {
				got = append(got, h.Text)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("heading %d is %q, want %q", i+1, got[i], tt.want[i])
				}
			}
		})
	}
}

// Levels come off the marker in both formats, which is what L04 compares after
// it has agreed on the count.
func TestHeadingLevels(t *testing.T) {
	doc := Parse(KindSlide, "* One\n** Two\n*** Three\n**** Four\n")
	want := []int{1, 2, 3, 4}
	if len(doc.Headings) != len(want) {
		t.Fatalf("got %d headings, want %d", len(doc.Headings), len(want))
	}
	for i, h := range doc.Headings {
		if h.Level != want[i] {
			t.Errorf("heading %d is level %d, want %d", i+1, h.Level, want[i])
		}
	}
}
