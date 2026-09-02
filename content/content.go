// Package content is the model of the go.dev site as a thing to translate.
//
// The site is a fork of golang/website. English lives in _content and
// Vietnamese in _content_vi, and the Go code overlays the second on the first
// so a file missing from _content_vi is served in English rather than as a 404.
// That overlay is why the corpus has no manifest of what exists: the answer is
// whatever is on disk, and a file that was never translated is invisible until
// somebody opens the page.
//
// So the first thing this package does is make the pairing explicit. Every
// English file that carries prose gets a Pair, whether or not the Vietnamese
// side is there, and the gates in quality/ read pairs rather than walking the
// tree themselves.
package content

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Directory names in the site repo. They are constants rather than options
// because the overlay in content.go on the other side names them too, and two
// places that must agree are better as one fact repeated than as two settings.
const (
	EnglishDir    = "_content"
	VietnameseDir = "_content_vi"
)

// Kind is what a file is, which decides how it is parsed and which gates apply.
type Kind string

const (
	// KindMarkdown is a .md page: front matter, then Markdown.
	KindMarkdown Kind = "markdown"
	// KindHTML is a .html page. It carries front matter too, and its body is
	// HTML with template actions in it.
	KindHTML Kind = "html"
	// KindArticle is a present(1) .article, the format the tour and the talks
	// are written in.
	KindArticle Kind = "article"
	// KindSlide is a present(1) .slide.
	KindSlide Kind = "slide"
	// KindTemplate is a .tmpl, which is the site's layout. Its prose is a
	// handful of strings inside template actions and its structure is load
	// bearing: a mangled action is a build failure, not a bad sentence.
	KindTemplate Kind = "template"
	// KindYAML is a data file like menus.yaml or resources.yaml, where some
	// values are prose and the keys are not.
	KindYAML Kind = "yaml"
	// KindText is a .txt, most of which are fixtures and robots.txt rather
	// than prose.
	KindText Kind = "text"
	// KindOther is everything else: images, Go source, CSS. Not translated.
	KindOther Kind = "other"
)

// KindOf reads the kind off the extension.
func KindOf(name string) Kind {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".md":
		return KindMarkdown
	case ".html":
		return KindHTML
	case ".article":
		return KindArticle
	case ".slide":
		return KindSlide
	case ".tmpl":
		return KindTemplate
	case ".yaml", ".yml":
		return KindYAML
	case ".txt":
		return KindText
	}
	return KindOther
}

// Translatable says whether a kind carries prose worth sending to a model.
//
// Text is out. Of the 26 .txt files under _content, all but robots.txt are
// test fixtures for cmd/golangorg, and translating a fixture breaks the test
// that reads it.
func (k Kind) Translatable() bool {
	switch k {
	case KindMarkdown, KindHTML, KindArticle, KindSlide, KindTemplate, KindYAML:
		return true
	}
	return false
}

// Skip is the set of paths under _content that are never translated, by prefix.
//
// _content/tour/static is the Angular app that drives the tour. Its Vietnamese
// is in the templates and the lesson articles, and its JavaScript is code.
// cmd fixtures are read back by tests that compare bytes.
var Skip = []string{
	"tour/static/js",
	"tour/static/lib",
	"js/",
	"css/",
	"images/",
	"favicon.ico",
}

func skipped(rel string) bool {
	rel = filepath.ToSlash(rel)
	for _, prefix := range Skip {
		if strings.HasPrefix(rel, prefix) {
			return true
		}
	}
	return false
}

// Pair is one English file and the Vietnamese that stands for it.
//
// VietnamesePath is set whether or not the file is there, because the gate that
// reports a missing translation needs to name where it should be.
type Pair struct {
	// Rel is the path under _content, slash separated, and is the identity of
	// the pair everywhere else: in the queue, in the manifest and in a report.
	Rel  string
	Kind Kind

	EnglishPath    string
	VietnamesePath string
	// Exists is whether the Vietnamese file is on disk.
	Exists bool
}

// Root is a checkout of tamnd/godev-vn.
type Root string

// Pairs walks the English tree and returns every translatable file, in path
// order so that a report and a run are both reproducible.
func (r Root) Pairs() ([]Pair, error) {
	base := filepath.Join(string(r), EnglishDir)
	info, err := os.Stat(base)
	if err != nil {
		return nil, fmt.Errorf("%s is not a checkout of godev-vn: %w", string(r), err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", base)
	}
	var out []Pair
	err = filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(base, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if skipped(rel) {
			return nil
		}
		kind := KindOf(rel)
		if !kind.Translatable() {
			return nil
		}
		vi := filepath.Join(string(r), VietnameseDir, filepath.FromSlash(rel))
		_, statErr := os.Stat(vi)
		out = append(out, Pair{
			Rel: rel, Kind: kind,
			EnglishPath: p, VietnamesePath: vi, Exists: statErr == nil,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Rel < out[j].Rel })
	return out, nil
}

// Find returns the pair for one path under _content.
func (r Root) Find(rel string) (Pair, error) {
	rel = filepath.ToSlash(path.Clean(rel))
	rel = strings.TrimPrefix(rel, EnglishDir+"/")
	en := filepath.Join(string(r), EnglishDir, filepath.FromSlash(rel))
	if _, err := os.Stat(en); err != nil {
		return Pair{}, fmt.Errorf("no English file at %s", rel)
	}
	vi := filepath.Join(string(r), VietnameseDir, filepath.FromSlash(rel))
	_, statErr := os.Stat(vi)
	return Pair{
		Rel: rel, Kind: KindOf(rel),
		EnglishPath: en, VietnamesePath: vi, Exists: statErr == nil,
	}, nil
}

// English reads the source.
func (p Pair) English() (string, error) {
	raw, err := os.ReadFile(p.EnglishPath)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// Vietnamese reads the translation, and reports the empty string when there is
// none rather than an error, because "not translated yet" is the ordinary state
// of this corpus and not a failure of the caller.
func (p Pair) Vietnamese() (string, error) {
	raw, err := os.ReadFile(p.VietnamesePath)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// SHA256 is the digest of a file's bytes, which is what the manifest records so
// a later run can tell a translation that is current from one whose English has
// moved under it.
func SHA256(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}
