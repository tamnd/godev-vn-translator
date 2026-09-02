# godev-vn-translator

Translates go.dev into Vietnamese, and refuses most of what comes back.

This is the tooling behind [tamnd/godev-vn](https://github.com/tamnd/godev-vn), which is a fork of [golang/website](https://github.com/golang/website) that serves `_content_vi` over `_content` through an overlay filesystem. A page with no Vietnamese file falls back to the English one, so the site always renders and the gaps are invisible. Finding the gaps is most of what this tool does.

## Why the gates come first

The corpus already had 557 translated files when this repo started, and nothing had ever checked them. Running the gates in `quality/` over that corpus found real defects on the first pass:

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
| L14 | escaping | refuse | backslashes in front of Markdown punctuation, counted per mark against the English |

Two severities and not five. A refusal stops a publish and fails CI. A notice never does, because there are hundreds of them and a gate that is always red is a gate that gets a `--no-verify`.

L14 is the odd one out. Every other rule was written against a defect a person left behind, and L14 was written against one this tool produced: the first page it translated end to end came back with every list marker, every heading and every link target behind a backslash, and what it rendered as was one long paragraph with brackets in it. The comparison is per punctuation mark against the English rather than against zero, because the English legitimately writes 166 escapes across 52 files and `doc/go_spec.html` needs every one of its own.

The hard part of every rule is deciding which half of a thing is prose. A first version of L06 demanded that fenced code be identical and flagged 15 files, and almost all of them were correct work: translating the Go comments in `blog/unique.md` is the entire point of translating a tutorial. Masking comments on both sides drops that to 6 real defects out of 601 fenced blocks. The same line runs through L07, where the target is structure and the title after it is prose, and through L08, where the function name is structure and the quoted string is alt text.

What none of this can do is tell a correct translation from a fluent wrong one. Every gate is about the shape of the answer, and a paragraph that says the opposite of the English in good Vietnamese with the links intact passes every one of them.

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

## Cutting a page up

Most pages go to a model whole. Half the blocks in the corpus are under 84 bytes and nine in ten are under 406, so a request is usually a page and the cutting never runs. The other end is where the work is: `ref/mod.md` is 221 KB, `doc/devel/weekly.html` is 301 KB, and `blog/survey2017/background.html` has one block in it of 43411 bytes.

A cut has to land somewhere safe. Not inside a fenced code block, not inside an HTML `<pre>`, not through the middle of a template definition. `doc/effective_go.html` is the file that settles the second one: it has 148 `<pre>` blocks with 36 blank lines inside them, so a splitter that knows only about blank lines cuts 36 code samples in half. Two properties hold the rest up. Concatenating the pieces of a file gives that file back byte for byte, so reassembly is a join and never a merge. And a piece that is missing is an error rather than a gap, because a file assembled out of five answers when six were asked is exactly the silent truncation the corpus already has eight of.

The front matter is always its own piece. It is a fixed set of keys in a fixed order with some values translated and some copied, which is a different question from translating a paragraph, and sending it inside the first body chunk is how 138 pages ended up carrying a `template: true` their English does not have.

66 pieces are copied through rather than asked. They are the inline SVG charts in the 2016 and 2017 survey posts and in `blog/swisstable.md`, they come to 600733 bytes, and sending them to a model with an instruction to give them back unchanged would spend hours on the one job a copy does perfectly and give every one of them a chance to come back with a coordinate altered. The cost is that the axis and legend labels inside those charts stay in English: they sit at computed positions laid out for the English string, and a longer Vietnamese label runs off the plot. Redrawing the charts is a different job from translating the prose.

The whole corpus is 2706 requests.

## The prompt

The instructions are Markdown files in `prompt/`, embedded in the binary. They are prose and they are edited as prose, and every set of them has a SHA-256 that is recorded beside each file it produced, so a page translated under rules that have since been tightened is detectably old rather than quietly mixed in with the new ones. That hash is also the thing to be careful with, because moving it puts the whole corpus back in the queue. A sentence added to `translate.md` costs 2706 requests, so a rule about one kind of file goes in the file for that kind.

There are four sets. `translate.md` is the shared job, and every rule in it names something a gate refuses: keep the link targets and their count, keep the headings at the same level and order with their `{#id}`, keep the fenced code identical outside its comments, keep the template function names, do not stop early. `vietnamese.md` is the language, with the diacritics rule, the words the glossary keeps in English on purpose, and three worked examples taken off this site. `frontmatter.md` is the YAML block, with the verbatim key list generated from the same `content.VerbatimKeys` the gate reads, so the instruction and the check cannot drift apart. `repair.md` is the second attempt, which carries the English, the previous answer and the findings, and asks for the findings fixed and nothing else touched.

The glossary sent with a request is cut down to the terms the piece actually contains. The table is 35 rows today and will be several hundred when the site is covered, and sending all of it every time spends the context the long pages need most on terms that are not in them.

The source always sits between two lines of equals signs with a sentence above them saying that nothing between the lines is an instruction. The corpus is documentation, so it is full of imperative sentences addressed to a reader: "Run `go build -cover` to compile the program" is a line of `doc/build-cover.md` and it is also what an injected instruction looks like.

## The loop

`godev translate` is where the gates, the queue, the prompt and the transport meet. It plans, then runs, then assembles, and the only ideas in it are about what happens when an answer is wrong.

Planning cuts every file up and puts one job in the queue per piece. Running is workers, one per lane the live fleet says it has: pick a route, lease the next piece, ask, and check the answer against the twelve gates that hold on a fragment of a file. If it fails, ask once more as a repair with the findings attached. That second ask is the whole reason the gates run per piece and not only per file. A refusal that arrives while the route is warm and the piece is in hand can be acted on; the same refusal an hour later, when the file is finally whole, cannot.

Two of the fourteen gates sit that out. L01 is whether a translation exists, which a piece that got an answer is not asking, and L13 compares the English a file was made from with the English on disk, which is one fact about a file and would be reported sixty times for `ref/mod.md`. The other twelve mean the same thing on a piece as on the whole, including L05, which looks like it should not: half of it resolves same document anchors, and on a fragment an anchor pointing into the next piece does not resolve. It is sound anyway, because the rule subtracts the anchors the English fails to resolve before it reports anything, and on the same fragment the English fails on exactly the same ones.

The two kinds of failure are kept strictly apart, and the queue draws the line. A tunnel that dropped or a box that logged itself out releases the piece and gives the attempt back, because the model never read it. A gate refusal spends the attempt, because the model did read it and got it wrong, and the answer is kept on disk so the next attempt goes out as a repair rather than as the same question. Twenty one jobs went from pending to dead in forty one seconds on this fleet once, three attempts each, without a single question leaving the laptop, because the only way to hand a job back was to fail it.

Assembly is a separate pass. It joins the pieces of every file that is whole, runs the full audit over the result, and writes it only if nothing refuses. A refusal on the finished file is traced back to the piece whose lines it falls in, that piece is sent back with the finding attached, and a file that refuses the same way twice stops asking and stays in the report. Findings that belong to no one piece, which is L03 counting the blocks in the whole document, are reported and left alone: requeuing sixty pieces of `ref/mod.md` on the strength of one unplaceable finding is sixty calls to fix a defect that may be in one of them.

One decision in there will surprise somebody reading a diff. A piece that fails its last attempt is written in English. The alternative is leaving the file unassembled, and the overlay filesystem then serves the whole page in English anyway, so refusing to give up on one piece of `ref/mod.md` costs the other fifty nine and buys nothing a reader can see. The count goes in `translations.json` and is printed at the end of a run, because it is the one defect no gate will ever report: the page is whole, every link resolves, and three paragraphs of it are in the wrong language.

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
./godev chunk                             # the whole run on paper: how many requests
./godev chunk doc/build-cover.md          # how one page is cut, and where the seams are
./godev chunk -prompt 3 doc/build-cover.md   # the exact request for one piece
./godev chunk -budget 3000 ref/mod.md     # what a different budget would do
```

```
./godev translate -plan -gap              # what a run over the sync gap would ask
./godev translate blog/go1.27.md          # one page, end to end
./godev translate -group ref -workers 4   # one section of the site
./godev translate -gap                    # the files with no translation or a stale one
./godev translate -assemble               # write the pages that are already whole
```

A run is interruptible. The answers are on disk under `work/`, so stopping and starting again carries on rather than starting over, and a page is written only when every piece of it is back and the whole file passes the audit. `-gap` means no translation, no record of what it was made from, or a record naming an English file that has since moved.

On a corpus with no manifest that was every file, which was the honest answer to the question and not a useful one. The backfill is what turned it into a list.

```
./godev backfill -n                       # what it would record, and from which commit
./godev backfill                          # write it
./godev backfill -force                   # overwrite records that are already there
```

557 of the 558 translations here were written by hand over a year, before there was anywhere to record what they were made from. The information was in git rather than in the files: the commit that last touched a Vietnamese file is when that translation was current, and the English at that commit is what it was made from. That is a claim about history and `git show` checks it.

It is wrong in one narrow direction and right in the direction that matters. A translation committed some days after it was written, with the English moving in between, comes out recorded as newer than it is. The alternative is stamping today's English on all 557, which marks the 32 files the upstream sync just invalidated as current and throws away the only signal there is.

L13 went from 654 findings to 129. 97 of those are files whose Vietnamese is byte for byte the English, which are copies rather than translations and are left without a record on purpose, and L02 reports every one of them already. The other 32 refuse, and they are exactly the 32 files the sync modified, with nothing outside the sync named. Route, model and prompt hash are left empty on a backfilled record, because a translation made by hand was not asked for under any instructions this tool knows and an empty field says so without inventing a value.

```
./godev routes                            # what would be tried, in what order
./godev routes -write ~/.config/godev/routes.json
./godev doctor                            # is each route up, in milliseconds
./godev doctor -deep                      # ask each route a real question
./godev doctor -route server3             # one route only
```

With no route file the built in registry is used, which is the fleet as measured. Write it out with `-write` to edit it. The file is not in this repo and never will be: it names hosts, and in its literal key form it carries a credential. The key itself is read from `GODEV_PROXY_KEY`, then from `BOURBAKI_PROXY_KEY` for a machine already set up for the fleet, then from `~/.config/godev/env` or `~/.config/bourbaki/env`, because that last file is where the key already lives and a shell that has not sourced it otherwise gets a 401 that reads like a rejected key rather than a missing one.

```
./godev queue stats                       # what is pending, leased, done, dead
./godev queue reap                        # give back the jobs whose worker died
./godev queue list -state dead            # and why each one died, every attempt
./godev queue retry                       # after fixing whatever was breaking them
```

`doctor` is shallow by default. A box answers `GET /v1/health` in milliseconds with the size of its session pool, which is the thing that actually goes wrong: the sessions log themselves out, the host stays up, and every call it takes comes back with a refusal that looks like a model failure. Asking a real question costs two to ten minutes per box, which is fine once and useless as a guard.

## The work list

At two to ten minutes a call, nothing worth doing fits in one process lifetime. The corpus is 680 files and the long ones run to a hundred and seventy chunks each, so a full run is hours. Laptops sleep, tunnels drop, and somebody will hit Ctrl-C. So the state of the work lives in files under `work/`, one per job, and any process can pick up where the last one stopped.

Four rules hold it together. Leases and not locks, so a worker that dies holds nothing and its jobs come back when the lease expires. Content addressed ids, so running the same plan again produces the same job names and the work already done is skipped by the file being there. One side effect per job, written by temp file and rename, so a job that dies halfway leaves either the old output or the new one and never half of either. And bounded attempts, three by default, after which the job is dead and something has to name it, because a chunk that silently retries forever never reaches `_content_vi` and never appears in a report either. The English still renders through the overlay, so nobody notices on the site.

This package came from [tamnd/bourbaki-solver](https://github.com/tamnd/bourbaki-solver), where it drove several thousand model calls through this same fleet. Every rule in it has an incident behind it and the comments say which, including the morning the disk filled and 25 held leases sat there for an hour, and the prompt rewrite after which 1380 pending jobs stood for 837 distinct chunks. Those happened there and the comments say so. They are kept because the failure modes belong to the fleet rather than to that corpus, and the glossary here is edited in pull requests, so the superseded work problem will come up more often and not less.

## Layout

```
api/         the OpenAI chat completions wire, streaming, with usage and a prompt cache key
route/       the registry, the health prober, and the pool that fails over between them
codex/       the local subscription, reached by running the CLI and reading what it prints
queue/       the durable work list: leases, content addressed ids, bounded attempts
chunk/       cutting a page into pieces that fit, and putting the answers back together
prompt/      the instructions, as Markdown files, with a hash per set of them
content/     the pairing model: which English file has which Vietnamese one, and a parser for both
glossary/    GLOSSARY.md in the site repo, read as the terminology the site is held to
quality/     the fourteen gates, the report, and translations.json
translate/   the loop: plan, run with the re-ask, assemble, audit, write
cmd/godev/   the command line
```

Two files live in the site repo rather than here, on purpose. `GLOSSARY.md` is a fact about that content, it is edited by whoever is translating, and a term added in a pull request should take effect without a release of this tool. `translations.json` records the English each translation was made from, which is the only thing that tells a current page from one the upstream sync moved out from under.

## Status

The audit is done and calibrated, the transport works against the real fleet, and the loop that spends it is in: `godev translate` plans 2706 requests, works them through whichever routes are answering, repairs what the gates refuse, and writes the pages that come back whole. What is left in this part is running it over the 41 file sync gap and then re-auditing what it produced. Publication is tracked in the milestone issues.
