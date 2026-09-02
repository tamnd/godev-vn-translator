package quality

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// ManifestFile is where the record lives, at the root of the site repo.
//
// At the root rather than inside _content_vi, because everything under
// _content_vi is embedded into the binary by content.go and a manifest is not
// content. At the root of the site repo rather than in this one, because the
// fact it records is a fact about those files and it has to move with them:
// a checkout of godev-vn at any commit can say what its own translations were
// made from, with no second repo to consult.
const ManifestFile = "translations.json"

// Record is what one translation was made from, and by what.
type Record struct {
	// EnglishSHA256 is the digest of the English file at the moment the
	// translation was written. It is the whole point of the manifest: without
	// it, an upstream sync that changes 41 files leaves no way to tell those
	// 41 from the 400 that are still current, and the only safe reading is
	// that everything is stale.
	EnglishSHA256 string `json:"english_sha256"`
	// PromptSHA256 is the instructions the translation was asked for under, as
	// prompt.Hash reports them. It answers the other stale question: not "has
	// the English moved" but "were these rules the current rules". Tightening a
	// gate usually means tightening the sentence in the prompt that goes with
	// it, and after that the pages written under the old sentence are worth
	// asking for again. Without this there is no way to tell them apart, and
	// the only safe reading is again that everything is stale.
	PromptSHA256 string `json:"prompt_sha256,omitempty"`
	// Route and Model name what answered, because a page translated by a cut
	// down model is a page worth reading again, and a report that cannot say
	// which pages those are is a report that cannot suggest anything.
	Route string `json:"route,omitempty"`
	Model string `json:"model,omitempty"`
	// At is when, to the day. Not to the second: the value of the field is
	// ordering a re-read, and a timestamp precise enough to churn the diff on
	// every run costs more than it gives.
	At string `json:"at,omitempty"`
	// Chunks is how many pieces the file went over in, which is the number to
	// look at first when a long page comes back inconsistent with itself.
	Chunks int `json:"chunks,omitempty"`
	// English is how many of those pieces were given up on and written in
	// English so that the rest of the page could ship.
	//
	// It is here because it is the one thing about a finished file that no gate
	// will ever report. The page is whole, every link resolves, every heading is
	// in place, and three paragraphs in the middle of it are in the wrong
	// language. A reader sees that immediately and an audit does not, so the
	// number has to be written down at the moment it is decided.
	English int `json:"english,omitempty"`
}

// Manifest is the whole record, keyed by path under _content.
type Manifest struct {
	Records map[string]Record `json:"records"`
}

// LoadManifest reads the manifest from a checkout. A checkout without one gets
// an empty manifest and no error, because the corpus predates the file and an
// audit that refuses to run without it would be an audit nobody could run.
func LoadManifest(root string) (*Manifest, error) {
	raw, err := os.ReadFile(filepath.Join(root, ManifestFile))
	if os.IsNotExist(err) {
		return &Manifest{Records: map[string]Record{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	if m.Records == nil {
		m.Records = map[string]Record{}
	}
	return &m, nil
}

// Get returns the record for one path.
func (m *Manifest) Get(rel string) (Record, bool) {
	if m == nil || m.Records == nil {
		return Record{}, false
	}
	r, ok := m.Records[rel]
	return r, ok
}

// Set records a translation.
func (m *Manifest) Set(rel string, r Record) {
	if m.Records == nil {
		m.Records = map[string]Record{}
	}
	if r.At == "" {
		r.At = time.Now().UTC().Format(time.DateOnly)
	}
	m.Records[rel] = r
}

// Write saves the manifest, with the keys sorted so that two runs that
// translated the same files produce the same bytes and the diff is the work
// rather than the map iteration order.
func (m *Manifest) Write(root string) error {
	keys := make([]string, 0, len(m.Records))
	for k := range m.Records {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	ordered := make(map[string]Record, len(keys))
	for _, k := range keys {
		ordered[k] = m.Records[k]
	}
	raw, err := json.MarshalIndent(Manifest{Records: ordered}, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(root, ManifestFile)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
