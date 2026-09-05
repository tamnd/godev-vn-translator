// Package translate is the loop that spends the fleet.
//
// Everything under it already exists on its own. content walks the corpus,
// chunk cuts a page into pieces that fit a call, prompt turns a piece into a
// request, route picks something alive to send it to, api sends it, queue
// remembers what has been sent, and quality says whether what came back is fit
// to publish. This package is where those meet, and the only ideas in it are
// about what happens when an answer is wrong.
//
// The shape is: plan, then run, then assemble.
//
// Plan walks the files, cuts each one up and adds one job per piece. It is
// idempotent because the job id is a hash of the piece and the prompt, so
// planning twice adds nothing the second time, and an upstream sync that edits
// a file makes new jobs for exactly the pieces that moved.
//
// Run is workers. Each one picks a live route, leases the next piece, asks,
// checks the answer against the gates that hold on a fragment, and if the
// answer fails, asks once more as a repair carrying the findings. That second
// ask is the whole reason the gates run per piece rather than per file: a
// refusal that arrives while the route is warm and the piece is in hand can be
// acted on, and a refusal that arrives an hour later when the file is finally
// whole cannot.
//
// Assemble puts the pieces of a finished file back together, audits the whole
// thing, and writes it only if nothing refuses. The per piece gates are the
// fast loop and this is the backstop, because eight of the seventeen rules are
// about a sequence that runs across a cut and there is no honest way to check
// those until the file exists.
//
// The one decision here that will surprise somebody reading a diff is that a
// piece which fails its last attempt is written in English. The alternative is
// leaving the file unassembled, and the overlay filesystem then serves the
// whole page in English anyway, so refusing to give up on one piece of
// ref/mod.md costs the other fifty nine pieces and buys nothing a reader can
// see. The count goes in the manifest and is printed at the end of a run,
// because it is the one defect no gate will ever report: the page is whole,
// every link resolves, and three paragraphs of it are in the wrong language.
package translate

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/tamnd/godev-vn-translator/api"
	"github.com/tamnd/godev-vn-translator/chunk"
	"github.com/tamnd/godev-vn-translator/content"
	"github.com/tamnd/godev-vn-translator/glossary"
	"github.com/tamnd/godev-vn-translator/prompt"
	"github.com/tamnd/godev-vn-translator/quality"
	"github.com/tamnd/godev-vn-translator/queue"
	"github.com/tamnd/godev-vn-translator/route"
)

// DefaultExpected is how long one piece is assumed to take, which is what the
// lease deadline is built from.
//
// Six minutes because that is the middle of what this fleet does: the local
// subscription answers a piece in two to four, the boxes in four to ten under
// load. queue.LeaseSlack adds five more on top for the gates and the write, and
// the deadline is deliberately generous because a lease that expires under a
// worker still waiting on an answer hands the piece to a second worker and pays
// for it twice.
const DefaultExpected = 6 * time.Minute

// Engine is one run.
type Engine struct {
	// Root is the checkout of tamnd/godev-vn that is read and written.
	Root content.Root
	// Work is where answers wait between processes.
	Work Store
	// Queue is the job list. Stage is always StageTranslate: a repair here is
	// a second ask inside one attempt at a piece, not a job of its own, because
	// a repair that is its own job is a repair that can be leased by a worker
	// that never saw the answer it is repairing.
	Queue *queue.Queue
	Pool  *route.Pool
	// Glossary is the whole table. Each ask gets only the rows its own piece
	// mentions.
	Glossary *glossary.Glossary
	// Budget is the chunk size in bytes. Zero is chunk.DefaultBudget. It must be
	// the same in Plan, Run and Assemble or the pieces will not line up, which
	// is why it lives on the engine and not on a method.
	Budget int
	// Expected is the assumed duration of one piece. Zero is DefaultExpected.
	Expected time.Duration
	// Log is where progress goes. Nil is silence.
	Log func(format string, args ...any)

	mu    sync.Mutex
	split map[string][]chunk.Chunk
}

func (e *Engine) budget() int {
	if e.Budget > 0 {
		return e.Budget
	}
	return chunk.DefaultBudget
}

func (e *Engine) expected() time.Duration {
	if e.Expected > 0 {
		return e.Expected
	}
	return DefaultExpected
}

func (e *Engine) logf(format string, args ...any) {
	if e.Log != nil {
		e.Log(format, args...)
	}
}

// Target names one piece of one file.
//
// The number is zero padded to four places because the queue leases in target
// order and target order is string order. Without the padding the tenth piece
// of a page sorts before the second, and the queue's one real scheduling
// promise, that the pieces of a page are asked for together so the page can be
// finished and shipped while the rest of the run is still going, quietly stops
// holding.
func Target(rel string, index int) string { return fmt.Sprintf("%s#%04d", rel, index) }

// ParseTarget is Target backwards.
func ParseTarget(target string) (rel string, index int, err error) {
	rel, num, ok := strings.Cut(target, "#")
	if !ok {
		return "", 0, fmt.Errorf("%q is not a chunk target", target)
	}
	index, err = strconv.Atoi(num)
	if err != nil {
		return "", 0, fmt.Errorf("%q is not a chunk target: %w", target, err)
	}
	return rel, index, nil
}

// chunks cuts a file up, once per run.
//
// Every piece that comes back has to be matched against the piece that was
// asked for, and a worker holding a target has only the path and a number. Two
// hundred pieces of ref/ would otherwise mean two hundred reads and two hundred
// splits of the same eight files.
func (e *Engine) chunks(rel string) ([]chunk.Chunk, error) {
	e.mu.Lock()
	if got, ok := e.split[rel]; ok {
		e.mu.Unlock()
		return got, nil
	}
	e.mu.Unlock()

	pair, err := e.Root.Find(rel)
	if err != nil {
		return nil, err
	}
	english, err := pair.English()
	if err != nil {
		return nil, err
	}
	cut := chunk.Split(rel, pair.Kind, english, e.budget())

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.split == nil {
		e.split = map[string][]chunk.Chunk{}
	}
	e.split[rel] = cut
	return cut, nil
}

func (e *Engine) chunkAt(rel string, index int) (chunk.Chunk, error) {
	cut, err := e.chunks(rel)
	if err != nil {
		return chunk.Chunk{}, err
	}
	for _, c := range cut {
		if c.Index == index {
			return c, nil
		}
	}
	return chunk.Chunk{}, fmt.Errorf("%s has no piece %d, only %d", rel, index, len(cut))
}

// ask builds the request for one piece, with the glossary cut down to the terms
// the piece contains and any previous rejection folded in as a repair.
func (e *Engine) ask(c chunk.Chunk, id string) (prompt.Ask, error) {
	a := prompt.Ask{Chunk: c, Glossary: e.Glossary.Relevant(c.Text).Prompt()}
	last, ok, err := e.Work.Rejected(id)
	if err != nil {
		return prompt.Ask{}, err
	}
	if ok {
		a.Previous, a.Findings = last.Text, last.Findings
	}
	return a, nil
}

// Plan is what a run intends to do.
type Plan struct {
	Files int
	// Asked is how many pieces need a model call.
	Asked int
	// Copied is how many are carried through without one, which today is the
	// inline SVG charts.
	Copied int
	// Added is how many of the asked pieces were not already in the queue. A
	// second plan over the same corpus adds nothing. On a dry plan it is how
	// many would have been added, and nothing was.
	Added int
}

// Plan cuts every file up and puts one job in the queue per piece that needs a
// call.
//
// With dry set it counts and inserts nothing. That is what `godev translate
// -plan` runs, and it has to be a separate path rather than a plan followed by a
// drain: draining removes every pending job, including ones a previous run put
// there on purpose, so a dry run that queued first would destroy work by asking
// a question. The count of new pieces is the same either way, because Add's
// answer is Has's answer.
func (e *Engine) Plan(pairs []content.Pair, dry bool) (Plan, error) {
	var p Plan
	for _, pair := range pairs {
		cut, err := e.chunks(pair.Rel)
		if err != nil {
			return p, err
		}
		if len(cut) == 0 {
			continue
		}
		p.Files++
		for _, c := range cut {
			if c.Verbatim {
				p.Copied++
				continue
			}
			p.Asked++
			hash, err := prompt.Hash(prompt.Ask{Chunk: c})
			if err != nil {
				return p, err
			}
			job := queue.New(queue.StageTranslate,
				Target(pair.Rel, c.Index), content.SHA256(c.Text), hash)
			if dry {
				if !e.Queue.Has(job.Stage, job.ID) {
					p.Added++
				}
				continue
			}
			added, err := e.Queue.Add(job)
			if err != nil {
				return p, err
			}
			if added {
				p.Added++
			}
		}
	}
	return p, nil
}

// Result is what a run did.
type Result struct {
	Done int
	// Repaired is how many pieces failed the gates on the first ask and passed
	// on the repair. It is the number that says whether the re-ask loop is
	// earning its keep.
	Repaired int
	// Failed is attempts that ended badly, which is not the same as pieces
	// given up on: a piece has three attempts.
	Failed int
	// English is pieces written in the source language because their last
	// attempt failed.
	English int
	Usage   api.Usage
}

func (r *Result) add(other Result) {
	r.Done += other.Done
	r.Repaired += other.Repaired
	r.Failed += other.Failed
	r.English += other.English
	r.Usage = r.Usage.Add(other.Usage)
}

// Run works the queue until it is empty or the context is cancelled.
//
// group is the section of the site, which is what the queue leases by. Empty
// means every section.
//
// Workers is how many pieces are in flight at once. Zero is the pool's own lane
// count, which is the sum of what each live route says it will carry, because
// the pool is the thing that knows and a number chosen here would either
// under-use a healthy fleet or queue up behind a box that is already full.
func (e *Engine) Run(ctx context.Context, group string, workers int) (Result, error) {
	if _, err := e.Queue.Reap(queue.StageTranslate); err != nil {
		return Result{}, err
	}
	if workers <= 0 {
		workers = e.Pool.Lanes()
	}
	if workers <= 0 {
		return Result{}, errors.New("no route is live, so there is nothing to run on")
	}

	var (
		mu      sync.Mutex
		total   Result
		failure error
		wg      sync.WaitGroup
	)
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := e.worker(ctx, group, i+1)
			mu.Lock()
			defer mu.Unlock()
			total.add(got)
			// The first real error is the one reported. A run that loses its
			// last live route produces one of these per worker and they all say
			// the same thing.
			if err != nil && failure == nil {
				failure = err
			}
		}()
	}
	wg.Wait()
	return total, failure
}

// worker is one lane: pick a route, lease a piece, do it, repeat.
//
// The route is picked before the lease and released after the piece, so a route
// that is full or cold is never leased against. Doing it the other way round
// leases a piece, finds no route, and has to put the piece back, which spends
// an attempt on a chunk no model ever read.
func (e *Engine) worker(ctx context.Context, group string, lane int) (Result, error) {
	var out Result
	for ctx.Err() == nil {
		value, client, release, err := e.Pool.Pick(ctx)
		if err != nil {
			return out, err
		}
		job, err := e.Queue.Lease(queue.StageTranslate, value.Name, group, e.expected())
		if err != nil {
			release()
			if errors.Is(err, queue.ErrEmpty) {
				return out, nil
			}
			return out, err
		}
		got, err := e.do(ctx, job, value, client)
		release()
		out.add(got)
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

// do is one attempt at one piece.
//
// The two kinds of failure in here are kept strictly apart, and the queue's own
// documentation is what draws the line. A transport failure, meaning the tunnel
// dropped or the box logged itself out or the answer never arrived, releases
// the piece and gives the attempt back, because the model never read it and
// spending an attempt on that would kill a good chunk after three bad
// afternoons. A gate refusal fails the piece and spends the attempt, because
// the model did read it and got it wrong, and the answer is kept so that the
// next attempt goes out as a repair.
func (e *Engine) do(ctx context.Context, job queue.Job, value route.Route, client api.Completer) (Result, error) {
	var out Result

	rel, index, err := ParseTarget(job.Target)
	if err != nil {
		_, ferr := e.Queue.Fail(job, err.Error())
		return out, ferr
	}
	c, err := e.chunkAt(rel, index)
	if err != nil {
		// The English moved under a job that was planned against the old cut.
		// That is not the route's fault and not worth retrying: the next plan
		// will make the right jobs.
		out.Failed++
		_, ferr := e.Queue.Fail(job, err.Error())
		return out, ferr
	}

	ask, err := e.ask(c, job.ID)
	if err != nil {
		return out, err
	}
	repair := ask.Repair()

	answer, usage, err := e.send(ctx, client, value, ask)
	out.Usage = out.Usage.Add(usage)
	if err != nil {
		e.Pool.Fail(value.Name, err)
		e.logf("lane %s  %s  route failed: %v", value.Name, job.Target, err)
		return out, e.Queue.Release(job, err.Error())
	}
	e.Pool.Succeed(value.Name)

	findings := e.check(c, answer)
	// One repair inside this attempt, and only when the ask was not already a
	// repair. Two repairs in a row on one attempt is the same model reading the
	// same findings twice, and the thing that fixes a piece a route keeps
	// getting wrong is a different route, which is what the next attempt gets.
	if len(findings) > 0 && !repair {
		ask.Previous, ask.Findings = answer, findings
		second, usage, err := e.send(ctx, client, value, ask)
		out.Usage = out.Usage.Add(usage)
		if err != nil {
			e.Pool.Fail(value.Name, err)
			// The first answer is worth keeping even though the repair never
			// arrived: it is what the next attempt repairs from.
			if perr := e.Work.PutRejected(job.ID, Rejected{
				Text: answer, Findings: findings, Route: value.Name,
			}); perr != nil {
				return out, perr
			}
			return out, e.Queue.Release(job, err.Error())
		}
		if again := e.check(c, second); len(again) == 0 {
			answer, findings = second, nil
			out.Repaired++
		} else {
			answer, findings = second, again
		}
	}

	if len(findings) > 0 {
		if err := e.Work.PutRejected(job.ID, Rejected{
			Text: answer, Findings: findings, Route: value.Name,
		}); err != nil {
			return out, err
		}
		reason := fmt.Sprintf("%d refused: %s", len(findings), findings[0])
		// The last attempt writes the English rather than leaving the file
		// unassembled. See the package comment for why that is the better of
		// two bad answers.
		if e.Queue.LastAttempt(job) {
			out.English++
			e.logf("lane %s  %s  giving up after %d attempts, keeping the English",
				value.Name, job.Target, job.Attempts)
			if err := e.Work.PutAnswer(job.ID, Answer{
				Text: c.Text, Route: value.Name, Model: value.Model, English: true,
			}); err != nil {
				return out, err
			}
			// PutAnswer drops the rejection, and the rejection is the record of
			// why this piece is in English. Put it back.
			if err := e.Work.PutRejected(job.ID, Rejected{
				Text: answer, Findings: findings, Route: value.Name,
			}); err != nil {
				return out, err
			}
		}
		out.Failed++
		e.logf("lane %s  %s  %s", value.Name, job.Target, reason)
		_, err := e.Queue.Fail(job, reason)
		return out, err
	}

	if err := e.Work.PutAnswer(job.ID, Answer{
		Text: answer, Route: value.Name, Model: value.Model,
	}); err != nil {
		return out, err
	}
	out.Done++
	e.logf("lane %s  %s  ok", value.Name, job.Target)
	_, err = e.Queue.Finish(job, true, "")
	return out, err
}

// send makes the call and cleans up the answer.
func (e *Engine) send(ctx context.Context, client api.Completer, value route.Route, ask prompt.Ask) (string, api.Usage, error) {
	instructions, input, err := ask.Messages()
	if err != nil {
		return "", api.Usage{}, err
	}
	response, err := client.Complete(ctx, api.Request{
		Model: value.Model, Instructions: instructions, Input: input,
	})
	if err != nil {
		return "", api.Usage{}, err
	}
	text := unmangle(clean(response.Text), ask.Chunk.Text)
	if strings.TrimSpace(text) == "" {
		return "", response.Usage, errors.New("the route answered with nothing")
	}
	// A route that has fallen over answers with its own error page, and the
	// scraper brings it back looking like a translation. That is the route
	// being down and not the answer being bad, so it fails the route in the
	// pool and releases the job, and the next attempt goes to a lane that is up.
	// See quality.TransportError for the page that shipped before this existed.
	if banner := quality.TransportError(text, ask.Chunk.Text); banner != "" {
		return "", response.Usage, fmt.Errorf("the route answered with its own error page: %q", banner)
	}
	// The blank lines at either end go back on here rather than at assembly,
	// so that the gates see the bytes that will land in the file. The front
	// matter is the case that forces it: a block with no newline after its
	// closing three hyphens does not parse as front matter at all, and L09
	// refuses a perfectly good answer for want of a character no model was
	// asked to write.
	return fit(text, ask.Chunk.Text), response.Usage, nil
}

// check runs the gates that hold on a fragment and returns the refusals as the
// sentences a report would print.
//
// Only the refusals. A Notice is worth a human's attention on the finished file
// and is not worth another call: L10 says a glossary term was rendered some
// other way, which is often right, and re-asking a piece until every notice
// clears would spend the fleet arguing about word choice.
func (e *Engine) check(c chunk.Chunk, answer string) []string {
	findings := quality.AuditChunk(quality.Input{
		Pair:     content.Pair{Rel: c.Rel, Kind: c.Kind},
		EN:       c.Text,
		VI:       answer,
		Glossary: e.Glossary,
	})
	var out []string
	for _, f := range findings {
		if f.Severity != quality.Refuse {
			continue
		}
		f.Path = Target(c.Rel, c.Index)
		out = append(out, f.String())
	}
	return out
}

// clean takes off what a model wraps an answer in when it ignores the
// instruction not to.
//
// Only the one wrapper that is unambiguous: a fence around the whole answer with
// nothing outside it. A piece of a Markdown page cannot itself be one
// unterminated fenced block, so that is never the translation. Everything else,
// a preamble sentence, a heading of its own, a closing remark, is left alone and
// refused by the gates, because a cleaner that guesses is a cleaner that
// eventually eats the first line of somebody's page.
//
// What it trims is deliberately lopsided. Trailing whitespace of any kind goes,
// because it means nothing and every model adds some. Leading newlines go,
// because a blank line at the top of an answer is padding. Leading spaces stay,
// because four of them is a Markdown code block, and a chunk that begins inside
// one begins indented.
func clean(text string) string {
	text = strings.TrimLeft(strings.TrimRight(text, " \t\r\n"), "\r\n")
	lines := strings.Split(text, "\n")
	if len(lines) < 2 {
		return text
	}
	first, last := strings.TrimSpace(lines[0]), strings.TrimSpace(lines[len(lines)-1])
	if strings.HasPrefix(first, "```") && last == "```" &&
		!strings.Contains(strings.Join(lines[1:len(lines)-1], "\n"), "```") {
		inner := strings.Join(lines[1:len(lines)-1], "\n")
		text = strings.TrimLeft(strings.TrimRight(inner, " \t\r\n"), "\r\n")
	}
	return text
}

// selfLinkRE matches a link target that came back as a Markdown link to itself,
// `]([url](url))` where the page wrote `](url)`. The two urls are captured
// separately and compared in Go, because RE2 has no backreference.
var selfLinkRE = regexp.MustCompile(`\]\(\[([^\[\]()\s]+)\]\(([^\[\]()\s]+)\)\)`)

// attrSelfLinkRE is the same defect inside an HTML attribute,
// `href="[url](url)"` where the page wrote `href="url"`.
//
// The `.html` pages under _content are hand written HTML rather than Markdown,
// so their links are attributes, and the converter autolinks the url in an
// attribute as readily as one in prose. Double quotes only, because that is
// what every anchor in the corpus uses and what the evidence shows.
var attrSelfLinkRE = regexp.MustCompile(`(href|src)="\[([^\[\]"]+)\]\(([^()"]+)\)"`)

// entityRE matches a numeric character reference, decimal or hexadecimal.
var entityRE = regexp.MustCompile(`&#(x[0-9a-fA-F]+|[0-9]+);`)

// unmangle undoes what the transport does to an answer on the way back.
//
// This is not the model getting it wrong. Three of the four routes are a
// headless browser driving chatgpt.com, so an answer is rendered to HTML by a
// web application and converted back to Markdown by a scraper, and that round
// trip is lossy in ways that are always the same. Every rejected answer in the
// work directory when this was written had at least one of them.
//
// A bare url in a link target comes back autolinked inside its own link, so
// `](https://example.com)` arrives as `]([https://example.com](https://example.com))`.
// Nobody writes that and no page contains it, the correct repair is the only
// repair, and L07 was already printing it back with the fix in the message.
//
// It happens in an HTML attribute too, and that one shipped. Nine anchors in
// doc/contribute.html and one in doc/diagnostics.html reached _content_vi as
// `href="[url](url)"`, which is not a url, so all ten were dead links on a
// merged page. L07 did not object because it reads Markdown links and an .html
// page has none. The two urls were identical in all ten, which is the same
// thing that makes the Markdown repair safe, and the replacement only runs when
// they match.
//
// The other is backslashes. The converter escapes punctuation on the way out,
// so `x` arrives as `\`x\“ and `**bold**` as `\*\*bold\*\*`. Across the eight
// rejected answers on disk when this was written it was 170 backticks, 36 open
// parentheses, 30 angle brackets and 24 asterisks, and not one of them was
// asked for.
//
// The English the piece was made from is the ground truth for how much escaping
// the passage wants, because it is the same passage. Where the English never
// writes \x, an answer that writes it wrote it in transit, and it comes off.
// Where the English does write it, this leaves the answer alone and L14
// decides, because choosing which of six backslashes to keep is a guess and the
// point of clean below is that this code does not guess.
//
// Punctuation only, one character at a time. \n and \t inside a fenced Go
// string are not escapes of anything and the letter test is what keeps them.
//
// The third is a character that came back as a numeric HTML entity. A tab
// arrives as `&#x9;`, and 72 of them were sitting in five stored answers when
// this was written, 38 of which reached doc/contribute.html and are on the page
// as that literal text. The arithmetic on that file is what makes it certain
// rather than likely: the English has 84 tab indented lines and the Vietnamese
// had 46 tabs and 38 entities.
//
// The test is the same one the backslashes get, and it has to be, because the
// English does write entities. There are `&#39;`, `&#160;`, `&#xa0;`, `&#xb6;`,
// `&#x60;`, `&#x261e;` and `&#x26;` under _content, every one of them written
// on purpose. So an entity comes apart only where the English writes the
// character it stands for and does not write the entity, which is the passage
// saying it wants the character raw. Where the English writes the entity, this
// leaves the answer alone.
//
// The backslashes come off first. The converter escapes the brackets in the
// wrapped link as readily as anything else, so a run that repaired the links
// first would leave `\]\(\[url\]\(url\)\)` standing and then reveal it, and on
// the eight answers on disk that is exactly what happened: thirty self linked
// targets that the first ordering did not see.
func unmangle(text, english string) string {
	var b strings.Builder
	runes := []rune(text)
	for i := 0; i < len(runes); i++ {
		if runes[i] == '\\' && i+1 < len(runes) && addedInTransit(runes[i+1], english) {
			continue
		}
		b.WriteRune(runes[i])
	}
	text = selfLinkRE.ReplaceAllStringFunc(b.String(), func(m string) string {
		g := selfLinkRE.FindStringSubmatch(m)
		if g[1] != g[2] {
			return m
		}
		return "](" + g[1] + ")"
	})
	text = attrSelfLinkRE.ReplaceAllStringFunc(text, func(m string) string {
		g := attrSelfLinkRE.FindStringSubmatch(m)
		if g[2] != g[3] {
			return m
		}
		return g[1] + `="` + g[3] + `"`
	})
	return entityRE.ReplaceAllStringFunc(text, func(m string) string {
		r, ok := entityRune(m)
		if !ok || strings.Contains(english, m) || !strings.ContainsRune(english, r) {
			return m
		}
		return string(r)
	})
}

// entityRune decodes a numeric character reference.
func entityRune(entity string) (rune, bool) {
	digits := entity[2 : len(entity)-1]
	base := 10
	if digits[0] == 'x' || digits[0] == 'X' {
		digits, base = digits[1:], 16
	}
	n, err := strconv.ParseInt(digits, base, 32)
	if err != nil || n <= 0 || n > unicode.MaxRune {
		return 0, false
	}
	return rune(n), true
}

// addedInTransit says whether a backslash in front of this character can only
// have come from the converter, which is true when the character is punctuation
// the English never escapes.
func addedInTransit(r rune, english string) bool {
	if r > unicode.MaxASCII || (!unicode.IsPunct(r) && !unicode.IsSymbol(r)) {
		return false
	}
	return !strings.Contains(english, `\`+string(r))
}
