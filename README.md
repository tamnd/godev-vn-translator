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

## The routes

There are three ways to reach a model here and one wire between them. A command route runs the `codex` CLI against the subscription this machine is signed in to. A box route calls `chatgpt-tool serve` on server1, server2 or server3 through an ssh tunnel to the loopback. A gateway route calls a plain OpenAI compatible endpoint. All three answer `POST /v1/chat/completions`, streaming, so nothing above `route/` knows which one it got.

Streaming is not decoration. A six thousand character chunk takes minutes to come back through a browser session, and a non streaming request holds a connection open with nothing on it for that whole time, which is the shape an idle timeout somewhere in the middle kills. The stream also carries the token usage on its last chunk, which is how a truncated answer gets noticed before it is written to a file.

The pool tries routes in rank order, gives each one as many lanes as it can carry, and cools a route down when it fails. The cooldown depends on the cause, because the causes are not alike. A rejected key is not going to fix itself in thirty seconds. A daily limit that names its own reset instant is honoured to the minute rather than rounded up to the default.

That last part is worth an example. A deep probe of the fleet on 2026-09-02 came back like this:

```
route    state     model                detail
codex    quota     gpt-5.4              hit your usage limit, resets 2026-09-07 02:52 UTC
server3  unknown   gpt-5                context canceled
server2  live      gpt-5 -> gpt-5-mini  answered in 3m20s on gpt-5-mini, not gpt-5
server1  quota     gpt-5                could not acquire a verified slot after 60s
```

Two things in there are the reason the deep probe exists. The subscription is spent for five days, and it says so in a sentence written for a person rather than in an error code, so it has to be read as prose. And server2 was asked for `gpt-5` and answered on `gpt-5-mini`. Neither the route file nor the model catalogue can see that, because both describe what is on offer and only the answer says what arrived. `gpt-5-mini` is the model that does not know the terminology, which is a large part of why the gates exist.

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

```
./godev routes                            # what would be tried, in what order
./godev routes -write ~/.config/godev/routes.json
./godev doctor                            # is each route up, in milliseconds
./godev doctor -deep                      # ask each route a real question
./godev doctor -route server3             # one route only
```

With no route file the built in registry is used, which is the fleet as measured. Write it out with `-write` to edit it. The file is not in this repo and never will be: it names hosts, and in its literal key form it carries a credential. The key itself is read from `GODEV_PROXY_KEY`, then from `BOURBAKI_PROXY_KEY` for a machine already set up for the fleet, then from `~/.config/godev/env` or `~/.config/bourbaki/env`, because that last file is where the key already lives and a shell that has not sourced it otherwise gets a 401 that reads like a rejected key rather than a missing one.

`doctor` is shallow by default. A box answers `GET /v1/health` in milliseconds with the size of its session pool, which is the thing that actually goes wrong: the sessions log themselves out, the host stays up, and every call it takes comes back with a refusal that looks like a model failure. Asking a real question costs two to ten minutes per box, which is fine once and useless as a guard.

## Layout

```
api/         the OpenAI chat completions wire, streaming, with usage and a prompt cache key
route/       the registry, the health prober, and the pool that fails over between them
codex/       the local subscription, reached by running the CLI and reading what it prints
content/     the pairing model: which English file has which Vietnamese one, and a parser for both
glossary/    GLOSSARY.md in the site repo, read as the terminology the site is held to
quality/     the thirteen gates, the report, and translations.json
cmd/godev/   the command line
```

Two files live in the site repo rather than here, on purpose. `GLOSSARY.md` is a fact about that content, it is edited by whoever is translating, and a term added in a pull request should take effect without a release of this tool. `translations.json` records the English each translation was made from, which is the only thing that tells a current page from one the upstream sync moved out from under.

## Status

The audit is done and calibrated, and the transport underneath it works against the real fleet. The queue, the prompt, the translation loop and publication are tracked in the milestone issues.
