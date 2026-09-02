package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/tamnd/godev-vn-translator/chunk"
	"github.com/tamnd/godev-vn-translator/content"
	"github.com/tamnd/godev-vn-translator/glossary"
	"github.com/tamnd/godev-vn-translator/prompt"
)

const chunkUsage = `usage: godev chunk [flags] [path]

Show how a page would be cut up and what would be asked about each piece.

With a path it prints one page. With no path it prints the whole corpus as a
table of counts, which is what to look at before starting a run: it says how
many requests the run is and how many of them are copies rather than questions.

flags:
  -budget N     bytes of English per request, default 6000
  -show         print the text of each piece
  -prompt N     print the whole request for piece N of the named page
  -over N       with no path, list the pages that need more than N requests

Nothing here talks to a model. It is the run as it would be, on paper.
`

func runChunk(checkout string, args []string) error {
	fs := flag.NewFlagSet("chunk", flag.ExitOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, chunkUsage) }
	budget := fs.Int("budget", chunk.DefaultBudget, "bytes of English per request")
	show := fs.Bool("show", false, "print the text of each piece")
	which := fs.Int("prompt", 0, "print the request for one piece")
	over := fs.Int("over", 8, "list pages needing more than this many requests")
	if err := fs.Parse(args); err != nil {
		return err
	}
	root := content.Root(checkout)
	terms, err := glossary.Load(checkout)
	if err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return chunkOne(root, terms, fs.Arg(0), *budget, *show, *which)
	}
	return chunkAll(root, *budget, *over)
}

func chunkOne(root content.Root, terms *glossary.Glossary, rel string,
	budget int, show bool, which int) error {
	pair, err := root.Find(rel)
	if err != nil {
		return err
	}
	english, err := pair.English()
	if err != nil {
		return err
	}
	chunks := chunk.Split(pair.Rel, pair.Kind, english, budget)

	if which > 0 {
		if which > len(chunks) {
			return fmt.Errorf("%s has %d pieces, there is no piece %d", pair.Rel, len(chunks), which)
		}
		c := chunks[which-1]
		if c.Verbatim {
			return fmt.Errorf("piece %d of %s is copied through, so nothing is asked about it",
				which, pair.Rel)
		}
		text, err := prompt.Ask{
			Chunk:    c,
			Glossary: terms.Relevant(c.Text).Prompt(),
		}.Text()
		if err != nil {
			return err
		}
		fmt.Print(text)
		return nil
	}

	fmt.Printf("%s  %s  %d bytes  %d pieces\n\n", pair.Rel, pair.Kind, len(english), len(chunks))
	fmt.Printf("%-5s  %-12s  %7s  %-8s  %s\n", "piece", "part", "bytes", "how", "under")
	for _, c := range chunks {
		how := "asked"
		switch {
		case c.Verbatim:
			how = "copied"
		case c.Split:
			how = "cut"
		}
		fmt.Printf("%-5d  %-12s  %7d  %-8s  %s\n", c.Index, c.Part, len(c.Text), how, c.Heading)
		if show {
			fmt.Printf("\n%s\n", strings.TrimRight(c.Text, "\n"))
			fmt.Printf("%s\n", strings.Repeat("-", 60))
		}
	}
	return nil
}

func chunkAll(root content.Root, budget, over int) error {
	pairs, err := root.Pairs()
	if err != nil {
		return err
	}
	type page struct {
		rel    string
		pieces int
	}
	var long []page
	var asked, copied, cut, pages, bytes int
	for _, p := range pairs {
		english, err := p.English()
		if err != nil {
			return err
		}
		chunks := chunk.Split(p.Rel, p.Kind, english, budget)
		pages++
		for _, c := range chunks {
			switch {
			case c.Verbatim:
				copied++
				bytes += len(c.Text)
			case c.Split:
				cut++
				asked++
			default:
				asked++
			}
		}
		if len(chunks) > over {
			long = append(long, page{p.Rel, len(chunks)})
		}
	}
	fmt.Printf("%d pages, %d requests\n", pages, asked)
	fmt.Printf("%d pieces copied through rather than asked, %d bytes of them\n", copied, bytes)
	fmt.Printf("%d pieces are a block cut at a line, which is the seam to watch\n\n", cut)
	sort.Slice(long, func(i, j int) bool { return long[i].pieces > long[j].pieces })
	fmt.Printf("pages needing more than %d requests:\n", over)
	for _, p := range long {
		fmt.Printf("  %-44s %4d\n", p.rel, p.pieces)
	}
	return nil
}
