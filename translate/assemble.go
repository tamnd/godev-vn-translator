package translate

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/tamnd/godev-vn-translator/chunk"
	"github.com/tamnd/godev-vn-translator/content"
	"github.com/tamnd/godev-vn-translator/prompt"
	"github.com/tamnd/godev-vn-translator/quality"
	"github.com/tamnd/godev-vn-translator/queue"
)

// Written is one file that reached _content_vi.
type Written struct {
	Rel    string
	Chunks int
	// English is how many pieces of it were given up on. A file with a number
	// here is a file to read.
	English int
	// Notices is what the full audit said that was not a refusal. It does not
	// stop the write and it is the queue of work for a human.
	Notices int
	Route   string
	Model   string
}

// Refused is a file that came back whole and did not pass the full audit.
type Refused struct {
	Rel      string
	Findings []quality.Finding
	// Requeued is the pieces sent back to be asked again, carrying the findings
	// that were traced to them.
	Requeued []string
	// Unplaced is the findings that could not be traced to any one piece,
	// because they are about the file as a file. L03 counts the blocks in the
	// whole document, and no piece of it owns that number.
	Unplaced []quality.Finding
}

// Result of an assembly pass.
type Assembly struct {
	Written []Written
	Refused []Refused
	// Waiting is files that are not whole yet, with how many pieces are still
	// out. This is the ordinary state of a run in progress and not a problem.
	Waiting map[string]int
}

// Assemble puts back together every file whose pieces are all in, audits each
// one, and writes the ones that pass.
//
// It is a separate pass from Run and not the last thing a worker does, because
// the last piece of a page can be finished by any of eight workers at once and
// two of them assembling the same file would race on the write. It is also
// cheap and it is safe to run again: a file that is already written and still
// passing is written again with the same bytes.
func (e *Engine) Assemble(pairs []content.Pair) (Assembly, error) {
	out := Assembly{Waiting: map[string]int{}}
	manifest, err := quality.LoadManifest(string(e.Root))
	if err != nil {
		return out, err
	}
	glossary := e.Glossary
	changed := false

	for _, pair := range pairs {
		cut, err := e.chunks(pair.Rel)
		if err != nil {
			return out, err
		}
		if len(cut) == 0 {
			continue
		}
		text, spans, missing, made, err := e.gather(pair.Rel, cut)
		if err != nil {
			return out, err
		}
		if missing > 0 {
			out.Waiting[pair.Rel] = missing
			continue
		}

		english, err := pair.English()
		if err != nil {
			return out, err
		}
		findings := quality.Audit(quality.Input{
			Pair: pair, EN: english, VI: text,
			Glossary: glossary, Manifest: manifest,
		})
		var refusals, notices []quality.Finding
		for _, f := range findings {
			if f.Severity == quality.Refuse {
				refusals = append(refusals, f)
			} else {
				notices = append(notices, f)
			}
		}
		if len(refusals) > 0 {
			refused, err := e.blame(pair.Rel, cut, spans, refusals)
			if err != nil {
				return out, err
			}
			out.Refused = append(out.Refused, refused)
			e.logf("%s  %d refused on the whole file, %d pieces sent back",
				pair.Rel, len(refusals), len(refused.Requeued))
			continue
		}

		if err := write(pair.VietnamesePath, text); err != nil {
			return out, err
		}
		hash, err := prompt.Hash(prompt.Ask{Chunk: firstAsked(cut)})
		if err != nil {
			return out, err
		}
		manifest.Set(pair.Rel, quality.Record{
			EnglishSHA256: content.SHA256(english),
			PromptSHA256:  hash,
			Route:         made.Route,
			Model:         made.Model,
			Chunks:        len(cut),
			English:       made.English,
		})
		changed = true
		out.Written = append(out.Written, Written{
			Rel: pair.Rel, Chunks: len(cut), English: made.English,
			Notices: len(notices), Route: made.Route, Model: made.Model,
		})
		e.logf("%s  written, %d pieces, %d notices", pair.Rel, len(cut), len(notices))
	}

	if changed {
		if err := manifest.Write(string(e.Root)); err != nil {
			return out, err
		}
	}
	return out, nil
}

// span is where one piece of the file ended up, in body lines.
//
// Body lines and not file lines, because that is what a finding carries: the
// audit parses the front matter off before it looks at anything, so a heading
// reported at line 12 is the twelfth line after the closing three hyphens.
type span struct {
	index    int
	from, to int
	head     bool
}

// made is what the pieces of a file say about how it was translated.
type made struct {
	Route   string
	Model   string
	English int
}

// gather reads the answers for one file and joins them.
//
// The answer stored for a piece has exactly one trailing newline on it, because
// that is what came back from the model after cleaning, and the piece it stands
// for may have had two or none. The blank line between two paragraphs is the
// paragraph break, so a piece whose trailing blank line was eaten joins its last
// paragraph onto the first one of the next piece and the two become one. That is
// the whole of fit, and it is the only place in this package where a byte of the
// English is put back into the Vietnamese on purpose.
func (e *Engine) gather(rel string, cut []chunk.Chunk) (text string, spans []span, missing int, m made, err error) {
	answers := map[int]string{}
	routes := map[string]int{}
	models := map[string]int{}
	var body strings.Builder

	for _, c := range cut {
		if c.Verbatim {
			if c.Part == chunk.PartBody {
				start := 1 + strings.Count(body.String(), "\n")
				body.WriteString(c.Text)
				spans = append(spans, span{index: c.Index, from: start,
					to: 1 + strings.Count(body.String(), "\n")})
			}
			continue
		}
		hash, err := prompt.Hash(prompt.Ask{Chunk: c})
		if err != nil {
			return "", nil, 0, m, err
		}
		id := queue.NewID(queue.StageTranslate, Target(rel, c.Index),
			content.SHA256(c.Text), hash)
		answer, ok, err := e.Work.Answer(id)
		if err != nil {
			return "", nil, 0, m, err
		}
		if !ok {
			missing++
			continue
		}
		got := fit(answer.Text, c.Text)
		answers[c.Index] = got
		if answer.Route != "" {
			routes[answer.Route]++
		}
		if answer.Model != "" {
			models[answer.Model]++
		}
		if answer.English {
			m.English++
		}
		if c.Part == chunk.PartFrontMatter {
			spans = append(spans, span{index: c.Index, head: true})
			continue
		}
		start := 1 + strings.Count(body.String(), "\n")
		body.WriteString(got)
		spans = append(spans, span{index: c.Index, from: start,
			to: 1 + strings.Count(body.String(), "\n")})
	}
	if missing > 0 {
		return "", nil, missing, m, nil
	}
	text, err = chunk.Assemble(cut, answers)
	if err != nil {
		return "", nil, 0, m, err
	}
	m.Route, m.Model = commonest(routes), commonest(models)
	return text, spans, 0, m, nil
}

// fit gives an answer the blank lines the piece it replaces had at each end.
//
// Both ends, and the leading one is the one that was found the hard way. The
// front matter is cut at its closing three hyphens, so the blank line under it
// belongs to the first body piece, and no model has ever begun an answer with a
// blank line. The first real page translated by this tool came back with its
// title running straight into its first paragraph, and no gate said anything,
// because every heading, link and block on both sides of the join was still
// exactly where it should be.
func fit(answer, source string) string {
	answer = strings.Trim(answer, "\n")
	head := source[:len(source)-len(strings.TrimLeft(source, "\n"))]
	tail := source[len(strings.TrimRight(source, "\n")):]
	return head + answer + tail
}

func commonest(counts map[string]int) string {
	var best string
	for name, n := range counts {
		if n > counts[best] || (n == counts[best] && name < best) {
			best = name
		}
	}
	return best
}

// firstAsked is any piece that goes to a model, for the prompt hash recorded
// against the file.
//
// Any of them, because a file is asked for under one set of instructions and
// the front matter under another, and the hash that says whether the file is
// current is the body one. A file that is nothing but front matter has no body
// piece and gets the front matter hash, which is still the right answer for it.
func firstAsked(cut []chunk.Chunk) chunk.Chunk {
	for _, c := range cut {
		if !c.Verbatim && c.Part == chunk.PartBody {
			return c
		}
	}
	for _, c := range cut {
		if !c.Verbatim {
			return c
		}
	}
	return chunk.Chunk{}
}

// blame traces the refusals on a finished file back to the pieces they are in,
// and sends those pieces back to be asked again with the findings attached.
//
// A finding carries a body line, and a piece owns a range of body lines, so most
// of this is a lookup. What it cannot place is a finding about the file as a
// file: L03 compares the number of blocks in the whole document with the number
// in the English, and no piece owns that number. Those are reported and left
// alone, because requeuing every piece of ref/mod.md on the strength of one
// unplaceable finding is sixty calls to fix a defect that may be in one of them.
//
// The requeue is not unconditional. A piece is only sent back if it is not
// already carrying exactly these findings, which is the fixed point: a file that
// refuses the same way twice stops asking and stays in the report for somebody
// to read. Without that the loop is plan, run, assemble, requeue, run, assemble,
// requeue, forever, and each turn of it is real fleet time.
func (e *Engine) blame(rel string, cut []chunk.Chunk, spans []span, refusals []quality.Finding) (Refused, error) {
	out := Refused{Rel: rel, Findings: refusals}
	byIndex := map[int][]string{}
	frontMatter := -1
	for _, s := range spans {
		if s.head {
			frontMatter = s.index
		}
	}

	for _, f := range refusals {
		index := -1
		switch {
		case f.Rule == ruleFrontMatter && frontMatter >= 0:
			index = frontMatter
		case f.Line > 0:
			for _, s := range spans {
				if !s.head && f.Line >= s.from && f.Line <= s.to {
					index = s.index
					break
				}
			}
		}
		if index < 0 {
			out.Unplaced = append(out.Unplaced, f)
			continue
		}
		byIndex[index] = append(byIndex[index], f.String())
	}

	indexes := make([]int, 0, len(byIndex))
	for i := range byIndex {
		indexes = append(indexes, i)
	}
	sort.Ints(indexes)

	for _, index := range indexes {
		c, err := e.chunkAt(rel, index)
		if err != nil {
			return out, err
		}
		hash, err := prompt.Hash(prompt.Ask{Chunk: c})
		if err != nil {
			return out, err
		}
		target := Target(rel, index)
		id := queue.NewID(queue.StageTranslate, target, content.SHA256(c.Text), hash)
		answer, ok, err := e.Work.Answer(id)
		if err != nil {
			return out, err
		}
		if !ok {
			continue
		}
		findings := byIndex[index]
		last, had, err := e.Work.Rejected(id)
		if err != nil {
			return out, err
		}
		if had && slices.Equal(last.Findings, findings) {
			// Asked once already for exactly this and it came back the same.
			continue
		}
		if err := e.Work.PutRejected(id, Rejected{
			Text: answer.Text, Findings: findings, Route: answer.Route,
		}); err != nil {
			return out, err
		}
		if err := e.Work.DropAnswer(id); err != nil {
			return out, err
		}
		if _, err := e.Queue.Reset(queue.StageTranslate, id); err != nil {
			return out, err
		}
		out.Requeued = append(out.Requeued, target)
	}
	return out, nil
}

// ruleFrontMatter is L09, which reports on the block at the top of the file and
// carries no line number. It is the one file level rule that has an obvious
// owner, so it is placed by name rather than by line.
const ruleFrontMatter = "L09"

// write puts a translation down, making the directory first, by temp file and
// rename so that a run interrupted here leaves the old translation rather than
// half of the new one.
func write(path, text string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(text), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}
