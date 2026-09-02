# godev-vn-translator

Translates go.dev into Vietnamese, and refuses most of what comes back.

This is the tooling behind [tamnd/godev-vn](https://github.com/tamnd/godev-vn), which is a fork of [golang/website](https://github.com/golang/website) that serves `_content_vi` over `_content` through an overlay filesystem. A page with no Vietnamese file falls back to the English one, so the site always renders and the gaps are invisible. Finding the gaps is most of what this tool does.

## Why the gates come first

The corpus already had 557 translated files when this repo started, and nothing had ever checked them. Running the thirteen gates in `quality/` over that corpus found real defects on the first pass:

| what | how many |
|---|---|
| files that stop early, with no marker and a plausible closing sentence | 8 |
| links dropped because the sentence carrying them was rewritten | 34 across 17 files |
| files carrying a `template: true` their English does not have | 138 |
| code blocks edited outside their comments | 6 |
| a template function name translated, so the file does not render | 1 |
| files with no translation at all | 26 |
| files that are a byte for byte copy of the English | 97 |

`_content_vi/blog/contributors-summit-2019.md` is the one worth looking at. It is 397 lines of English against 167 of Vietnamese, the prose that is there is good, every heading that survived matches and every link that survived resolves. It just stops in the middle of the section on proposals. A reader without the English open has no way to know.

Every rule in `quality/rules.go` was written against a named file on disk and its comment says which one. A rule with no defect behind it spends its life refusing correct work.

## The rules

| id | name | severity | what it compares |
|---|---|---|---|
| L01 | presence | notice | an English file with no Vietnamese counterpart |
| L02 | untranslated | notice | a Vietnamese file byte for byte identical to the English |
| L03 | truncation | refuse | block counts, so a file that stops early is caught from outside |
| L04 | headings | refuse | heading count, level and order |
| L05 | heading ids | refuse | explicit `{#id}` attributes, and same document anchors that resolve |
| L06 | code | refuse | fenced blocks with their comments masked, so translated comments pass |
| L07 | links | refuse | the multiset of link targets, ignoring link text and titles |
| L08 | actions | refuse | template actions with their string literals stripped |
| L09 | front matter | refuse | the key list, its order, and the values of the verbatim keys |
| L10 | terminology | notice | glossary terms left standing in English in the prose |
| L11 | language | refuse | the proportion of tone marked letters in the prose |
| L12 | commentary | refuse | a covering note or a fence wrapped around the whole answer |
| L13 | stale | refuse | the SHA-256 of the English the translation was made from |

Two severities and not five. A refusal stops a publish and fails CI. A notice never does, because there are hundreds of them and a gate that is always red is a gate that gets a `--no-verify`.

The hard part of every rule is deciding which half of a thing is prose. A first version of L06 demanded that fenced code be identical and flagged 15 files, and almost all of them were correct work: translating the Go comments in `blog/unique.md` is the entire point of translating a tutorial. Masking comments on both sides drops that to 6 real defects out of 601 fenced blocks. The same line runs through L07, where the target is structure and the title after it is prose, and through L08, where the function name is structure and the quoted string is alt text.

What none of this can do is tell a correct translation from a fluent wrong one. Every gate is about the shape of the answer, and a paragraph that says the opposite of the English in good Vietnamese with the links intact passes all thirteen.

## Using it

```
go build ./cmd/godev
./godev -C ../godev-vn audit
```

The checkout defaults to `$GODEV_VN`, then to `../godev-vn` beside this repo. Exit status is the interface: zero when nothing is refused, one when something is.

```
./godev audit -report reports/audit.md    # write the Markdown report
./godev audit -rule L03                   # one rule only, by id or name
./godev audit -all                        # notices as well as refusals
```

## Layout

```
content/     the pairing model: which English file has which Vietnamese one, and a parser for both
glossary/    GLOSSARY.md in the site repo, read as the terminology the site is held to
quality/     the thirteen gates, the report, and translations.json
cmd/godev/   the command line
```

Two files live in the site repo rather than here, on purpose. `GLOSSARY.md` is a fact about that content, it is edited by whoever is translating, and a term added in a pull request should take effect without a release of this tool. `translations.json` records the English each translation was made from, which is the only thing that tells a current page from one the upstream sync moved out from under.

## Status

The audit is done and calibrated. Translation, routing and publication are tracked in the milestone issues.
