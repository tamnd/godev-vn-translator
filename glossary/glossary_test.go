package glossary

import "strings"

import "testing"

const twoTables = `# Translation Glossary

## Thuật ngữ đã chốt

| Thuật ngữ gốc | Bản dịch ưu tiên | Ghi chú |
| --- | --- | --- |
| commit | commit | Keep the Git term unchanged. |
| vulnerability | lỗ hổng bảo mật | Security context. |

## Thuật ngữ tùy nghĩa

| Thuật ngữ tùy nghĩa | Bản dịch khi là thuật ngữ Go | Ghi chú |
| --- | --- | --- |
| interface | interface | Chỉ giữ nguyên khi là kiểu Go. |
| map | map | Chỉ giữ nguyên khi là kiểu Go. |
`

func TestParseTwoTables(t *testing.T) {
	g := Parse(twoTables)
	if len(g.Terms) != 4 {
		t.Fatalf("read %d terms, want 4: %+v", len(g.Terms), g.Terms)
	}
	want := map[string]bool{"commit": false, "vulnerability": false, "interface": true, "map": true}
	for _, term := range g.Terms {
		w, ok := want[term.EN]
		if !ok {
			t.Errorf("read a term that is not in the file: %q", term.EN)
			continue
		}
		if term.Contextual != w {
			t.Errorf("%q is contextual %v, want %v", term.EN, term.Contextual, w)
		}
	}
}

// The header of the second table is a row like any other, and a parser that did
// not know it would read it as a term whose English is the heading.
func TestParseSkipsBothHeaders(t *testing.T) {
	g := Parse(twoTables)
	for _, term := range g.Terms {
		if strings.HasPrefix(term.EN, "Thuật ngữ") {
			t.Errorf("a header row was read as a term: %q", term.EN)
		}
	}
}

// A file with one table is the shape this was before, and it has to keep
// working: the tool reads whatever GLOSSARY.md the checkout has, and a checkout
// from before the second table exists is a normal thing to run against.
func TestParseOneTable(t *testing.T) {
	g := Parse(`| Thuật ngữ gốc | Bản dịch ưu tiên | Ghi chú |
| --- | --- | --- |
| commit | commit | Keep the Git term unchanged. |
`)
	if len(g.Terms) != 1 {
		t.Fatalf("read %d terms, want 1", len(g.Terms))
	}
	if g.Terms[0].Contextual {
		t.Error("a term from the only table came back contextual")
	}
}

// The condition is the whole content of a context dependent row. A line reading
// "map -> map" on its own tells the model to leave "map each goroutine" in
// English, which is the mistake the row exists to prevent.
func TestPromptCarriesTheCondition(t *testing.T) {
	got := Parse(twoTables).Prompt()
	if !strings.Contains(got, "map  ->  map  (Chỉ giữ nguyên khi là kiểu Go.)") {
		t.Errorf("the context dependent row lost its note:\n%s", got)
	}
	if strings.Contains(got, "commit  ->  commit  (") {
		t.Errorf("an ordinary row carried its note, which is prompt spent on nothing:\n%s", got)
	}
}
