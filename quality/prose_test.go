package quality

import "testing"

// TestProse is the extractor's doc comment as assertions. Two rules read prose
// and both of them count what is left, so anything this fails to strip is a
// finding on a file that is correct.
func TestProse(t *testing.T) {
	for _, tt := range []struct {
		name string
		in   string
		want string
	}{
		{"plain", "Một câu.", "Một câu."},
		{
			"front matter is not prose",
			"---\ntitle: Cài đặt\n---\nMột câu.",
			"Một câu.",
		},
		{
			"a fence is code",
			"Trước.\n\n```go\nimport \"repository\"\n```\n\nSau.",
			"Trước. Sau.",
		},
		{
			"a link keeps its text and loses its target",
			"Xem [hướng dẫn](/doc/tutorial/generics) ở đây.",
			"Xem hướng dẫn ở đây.",
		},
		{
			"a link title after the target is prose",
			`Xem [MVS](/doc/mvs/upgrade.svg "Nâng cấp MVS").`,
			"Xem MVS Nâng cấp MVS.",
		},
		{"inline code is not prose", "Chạy `go install` trước.", "Chạy trước."},
		{
			"a template action is not prose",
			"Trước {{template \"foo\" .}} sau.",
			"Trước sau.",
		},
		{"an HTML comment is not prose", "Trước <!-- CL 29072 --> sau.", "Trước sau."},
		{"HTML tags go and their text stays", "<p>Một câu.</p>", "Một câu."},

		// The two this file was added for. htmlTagRE takes the tags and leaves
		// the body, and a stylesheet full of untoned Latin letters reads to L11
		// as a page nobody translated.
		{
			"a style body is not prose",
			"<style>\n#wire { background: #F0F0F0; }\n#wire .send { color: #900; }\n</style>\n<p>Một câu.</p>",
			"Một câu.",
		},
		{
			"a script body is not prose",
			"<script>var greeting = \"hello world\";</script>\n<p>Một câu.</p>",
			"Một câu.",
		},
		{
			"a style tag with attributes, and a closing tag with space",
			`<style type="text/css" media="screen">a { color: red }</style >Một câu.`,
			"Một câu.",
		},
		{
			"an uppercase script tag",
			"<SCRIPT>alert(1)</SCRIPT>Một câu.",
			"Một câu.",
		},
		{
			"two style blocks, and the prose between them",
			"<style>a{}</style>Giữa.<style>b{}</style>Cuối.",
			"Giữa. Cuối.",
		},
		{
			"a comment inside a style block",
			"<style><!-- a{} --></style>Một câu.",
			"Một câu.",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := Prose(tt.in); got != tt.want {
				t.Errorf("Prose(%q)\n got %q\nwant %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestProseWire is the real file, which is the whole reason for the change. It
// is thirteen lines of CSS and an empty div, identical in both languages
// because there is nothing in it to translate, and L11 called it untranslated.
func TestProseWire(t *testing.T) {
	const wire = `<style>
#wire {
	border: 1px solid #E0E0E0;
	background: #F0F0F0;
	height: 300px;
	font-family: 'Droid Sans Mono', 'Courier New', monospace;
	font-size: 18px;
	line-height: 24px;
	overflow: auto;
}
#wire div { margin-bottom: 10px; }
#wire .send { color: #900; }
#wire .recv { color: #009; }
</style>
<div id="wire"></div>`

	if got := Prose(wire); got != "" {
		t.Errorf("the page has no sentences in it and Prose found %q", got)
	}
	if f := check(t, ruleLanguage, wire, wire); len(f) != 0 {
		t.Errorf("L11 refuses a page with no prose in it: %v", f)
	}
}
