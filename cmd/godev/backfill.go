package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/tamnd/godev-vn-translator/content"
	"github.com/tamnd/godev-vn-translator/quality"
)

// runBackfill writes a manifest record for every translation that predates the
// manifest.
//
// 557 of the 558 translations in the corpus were made by people over a year,
// before there was anywhere to record what they were made from. L13 reports
// every one of them as having no record, which is 654 notices that say nothing
// except that the file is old. Until they have records, the one question the
// manifest exists to answer cannot be asked of them: has the English moved since.
//
// The answer is in git and not in the files. For each Vietnamese file, the
// commit that last touched it is when that translation was current, and the
// English at that commit is what it was made from. That is a claim about
// history rather than a guess about content, and it is checkable: git show is
// the whole of it.
//
// It is honest in the direction that matters. A translation committed some days
// after it was written, with the English moving in between, is recorded as
// newer than it is, and a sync in that window would then read as current. The
// alternative is recording today's English against every one of them, which
// marks the 32 files the sync just invalidated as current and throws away the
// only signal there is. This way those 32 come out stale, which is the answer.
//
// Route, model and prompt hash are left empty on purpose. A human translation
// was not asked for under any instructions this tool knows, and an empty field
// says that without inventing a value.
//
// The 97 files whose Vietnamese is byte for byte the English are skipped. They
// are copies and not translations, and a record against one asserts a
// translation was made from that English when none was. L02 already reports
// them, so skipping them here loses nothing and keeps the manifest a record of
// work that happened. When one of them is really translated it gets a record
// then, from the tool, the ordinary way.
func runBackfill(root string, args []string) error {
	fs := flag.NewFlagSet("backfill", flag.ExitOnError)
	dry := fs.Bool("n", false, "say what would be written and stop")
	force := fs.Bool("force", false, "overwrite records that are already there")
	if err := fs.Parse(args); err != nil {
		return err
	}

	pairs, err := content.Root(root).Pairs()
	if err != nil {
		return err
	}
	manifest, err := quality.LoadManifest(root)
	if err != nil {
		return err
	}

	commits, err := lastCommits(root)
	if err != nil {
		return err
	}

	// Work out who is in before asking git anything, so the counts at the end
	// are over one list rather than two loops that have to agree.
	type candidate struct {
		rel     string
		english string
		commit  commit
	}
	var todo []candidate
	var copies, uncommitted, recorded int
	for _, pair := range pairs {
		if !pair.Exists {
			continue
		}
		if _, ok := manifest.Get(pair.Rel); ok && !*force {
			recorded++
			continue
		}
		en, err := pair.English()
		if err != nil {
			return err
		}
		vi, err := pair.Vietnamese()
		if err != nil {
			return err
		}
		if vi == en {
			copies++
			continue
		}
		commit, ok := commits[content.VietnameseDir+"/"+pair.Rel]
		if !ok {
			// Not committed yet, so there is no history to read. A file in this
			// state is one somebody is working on right now, and it gets its
			// record from the tool when it lands.
			uncommitted++
			continue
		}
		todo = append(todo, candidate{rel: pair.Rel, english: en, commit: commit})
	}

	// Ask git for every English blob in one process. 557 files is 557 calls to
	// git show done the obvious way, and cat-file --batch is one.
	want := make([]string, 0, len(todo))
	for _, c := range todo {
		want = append(want, c.commit.hash+":"+content.EnglishDir+"/"+c.rel)
	}
	sort.Strings(want)
	blobs, err := catFile(root, want)
	if err != nil {
		return err
	}

	var wrote, missing, current int
	for _, c := range todo {
		blob, ok := blobs[c.commit.hash+":"+content.EnglishDir+"/"+c.rel]
		if !ok {
			// The Vietnamese file was committed when the English did not exist
			// under that name, which is a rename upstream. There is nothing to
			// record and saying so is more use than a wrong hash.
			missing++
			fmt.Printf("%-52s no English at %s\n", c.rel, short(c.commit.hash))
			continue
		}
		sum := content.SHA256(blob)
		if sum == content.SHA256(c.english) {
			current++
		}
		if !*dry {
			manifest.Set(c.rel, quality.Record{EnglishSHA256: sum, At: c.commit.date})
		}
		wrote++
	}

	verb := "written to " + quality.ManifestFile
	if *dry {
		verb = "to write"
	}
	fmt.Printf("\n%d records %s, %d of them still current and %d stale\n",
		wrote, verb, current, wrote-current)
	fmt.Printf("skipped %d already recorded, %d copies of the English, %d not committed, %d with no English at their commit\n",
		recorded, copies, uncommitted, missing)
	if *dry || wrote == 0 {
		return nil
	}
	return manifest.Write(root)
}

// commit is when a file was last touched.
type commit struct {
	hash string
	date string
}

// lastCommits is the commit that last touched each file under _content_vi.
//
// One walk of the log rather than a call per file. The log is newest first, so
// the first time a path is seen is the last time it changed.
//
// Merges are skipped. A merge that brings in a translation reports the file as
// touched at a date the work was not done on, and the commit that did the work
// is in the log a few lines further down.
func lastCommits(root string) (map[string]commit, error) {
	cmd := exec.Command("git", "log", "--no-merges", "--name-only",
		"--format=%x00%H %ad", "--date=short", "--", content.VietnameseDir)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("reading the history of %s: %w", content.VietnameseDir, err)
	}

	found := map[string]commit{}
	var current commit
	scanner := bufio.NewScanner(bytes.NewReader(out))
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "\x00") {
			hash, date, _ := strings.Cut(strings.TrimPrefix(line, "\x00"), " ")
			current = commit{hash: hash, date: date}
			continue
		}
		if line == "" || current.hash == "" {
			continue
		}
		if _, seen := found[line]; !seen {
			found[line] = current
		}
	}
	return found, scanner.Err()
}

// catFile reads many blobs out of history in one process.
//
// The --batch protocol answers each request with a header line, then the object,
// then a newline. A missing object gets one line ending in "missing" and no
// body, which is what a file that did not exist at that commit looks like.
func catFile(root string, refs []string) (map[string]string, error) {
	out := map[string]string{}
	if len(refs) == 0 {
		return out, nil
	}
	cmd := exec.Command("git", "cat-file", "--batch")
	cmd.Dir = root
	cmd.Stdin = strings.NewReader(strings.Join(refs, "\n") + "\n")
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	reader := bufio.NewReaderSize(stdout, 1<<20)
	for _, ref := range refs {
		header, err := reader.ReadString('\n')
		if err != nil {
			cmd.Wait()
			return nil, fmt.Errorf("reading %s: %w", ref, err)
		}
		fields := strings.Fields(strings.TrimSpace(header))
		if len(fields) < 3 {
			// "<ref> missing", so there is no body to skip.
			continue
		}
		var size int
		if _, err := fmt.Sscanf(fields[2], "%d", &size); err != nil {
			cmd.Wait()
			return nil, fmt.Errorf("reading the size of %s from %q: %w", ref, header, err)
		}
		body := make([]byte, size+1) // the object, then the newline after it
		if _, err := readFull(reader, body); err != nil {
			cmd.Wait()
			return nil, fmt.Errorf("reading %s: %w", ref, err)
		}
		out[ref] = string(body[:size])
	}
	return out, cmd.Wait()
}

func readFull(r *bufio.Reader, buf []byte) (int, error) {
	n := 0
	for n < len(buf) {
		got, err := r.Read(buf[n:])
		n += got
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

func short(hash string) string {
	if len(hash) > 12 {
		return hash[:12]
	}
	return hash
}
