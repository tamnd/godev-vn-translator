package translate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Store is where a chunk's answer waits for its neighbours.
//
// A file is only worth writing when every piece of it is back, and the pieces
// of ref/mod.md come back over sixty calls spread across four routes and very
// probably several days. So an answer has to survive the process that got it.
// The queue already knows a job is done; what it does not hold is the text,
// because a queue that carried its own payloads would be a queue where a
// retention policy and a lease policy are the same policy.
//
// Two things are kept per chunk and they are kept apart on purpose. An accepted
// answer is what assembly reads. A rejected answer is what the next attempt
// reads, so that the second attempt at a chunk goes out as a repair carrying
// the findings rather than as the same question that already failed once. That
// is the whole of the re-ask loop's memory, and it is the reason a job can fail
// on one machine and be repaired on another.
type Store struct {
	// Root is the working directory, outside both repos. Nothing in here is
	// content and nothing in here is worth committing: it is rebuildable from
	// the English and the prompt, and it holds several megabytes of half
	// finished translation that would otherwise land in a diff.
	Root string
}

// Answer is an accepted piece.
type Answer struct {
	Text string `json:"text"`
	// Route and Model are carried up into the manifest record for the file, so
	// a page can say what answered it. A page whose pieces came from three
	// routes names the one that did most of it.
	Route string    `json:"route,omitempty"`
	Model string    `json:"model,omitempty"`
	At    time.Time `json:"at"`
	// English says this piece was given up on and is the source text. It is
	// counted into the manifest and reported at the end of a run.
	English bool `json:"english,omitempty"`
}

// Rejected is a piece that came back and did not pass.
type Rejected struct {
	Text string `json:"text"`
	// Findings are the gate messages, as a report writes them, which is exactly
	// what the repair prompt wants. They are strings and not quality.Findings
	// because what goes back to the model is the sentence, and keeping the
	// struct would invite somebody to re-derive the sentence differently here
	// than the report does.
	Findings []string  `json:"findings"`
	Route    string    `json:"route,omitempty"`
	At       time.Time `json:"at"`
}

func (s Store) dir(kind string) string { return filepath.Join(s.Root, kind) }

func (s Store) path(kind, id string) string { return filepath.Join(s.dir(kind), id+".json") }

func (s Store) read(kind, id string, into any) (bool, error) {
	raw, err := os.ReadFile(s.path(kind, id))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(raw, into); err != nil {
		return false, fmt.Errorf("%s %s: %w", kind, id, err)
	}
	return true, nil
}

// write puts a file down by temp file and rename, because a worker killed
// halfway through writing an answer must leave either the old answer or the new
// one. A truncated JSON file here is a chunk that can never be read again and
// whose job is already marked done.
func (s Store) write(kind, id string, value any) error {
	if err := os.MkdirAll(s.dir(kind), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	path := s.path(kind, id)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Answer returns an accepted piece.
func (s Store) Answer(id string) (Answer, bool, error) {
	var a Answer
	ok, err := s.read("answers", id, &a)
	return a, ok, err
}

// PutAnswer records an accepted piece and drops the rejection it replaces.
func (s Store) PutAnswer(id string, a Answer) error {
	if a.At.IsZero() {
		a.At = time.Now().UTC()
	}
	if err := s.write("answers", id, a); err != nil {
		return err
	}
	return s.DropRejected(id)
}

// DropAnswer forgets an accepted piece, which is what assembly does to the
// chunks it blames for a refusal on the finished file.
func (s Store) DropAnswer(id string) error {
	err := os.Remove(s.path("answers", id))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// Rejected returns the last answer that failed the gates, if there is one.
func (s Store) Rejected(id string) (Rejected, bool, error) {
	var r Rejected
	ok, err := s.read("rejected", id, &r)
	return r, ok, err
}

// PutRejected records an answer that failed, so the next attempt is a repair.
func (s Store) PutRejected(id string, r Rejected) error {
	if r.At.IsZero() {
		r.At = time.Now().UTC()
	}
	return s.write("rejected", id, r)
}

// DropRejected forgets a rejection.
func (s Store) DropRejected(id string) error {
	err := os.Remove(s.path("rejected", id))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
