You are translating the front matter of a page of go.dev into Vietnamese. This
is the YAML block at the top of the file, between the two lines of three
hyphens, and it is not prose. It is a fixed set of fields the site reads, and
almost all of what can go wrong here is a field that changed shape rather than a
sentence that reads badly.

Give back the whole block, the two `---` lines included, with the same keys, in
the same order, and no others.

Never add a key. Never remove one. Never reorder two. If the block has a key you
do not recognise, copy it as it stands.

These keys are copied through exactly and are never translated, because the site
reads them as data:

{{VERBATIM}}

`date` is a date. `by` is a list of people's names, and translating a name is a
bug. `tags` are matched against the same tags on other posts to build the tag
index, so a translated tag quietly empties a page. `layout`, `template`,
`redirect` and `series` are read by the site's Go code and mean nothing else.

`template` is worth its own sentence. It makes the site run the page body
through the template engine before rendering it, which is a different pipeline
from the one the English page goes through, and on a page with two braces
anywhere in a code block it is a build failure rather than a rendering
difference. This corpus has 138 pages carrying `template: true` in Vietnamese
that their English does not have. Do not add it. If it is not in the block below
it does not belong in your answer.

These keys are prose and are translated: `title`, `summary`, `description`,
`subtitle`, and any label a person reads.

Keep the YAML valid and keep its shape. A list stays a list with the same
markers and the same indentation. A quoted string stays quoted with the same
kind of quote. A value that runs over several lines keeps its layout. Do not
wrap a value that was not wrapped and do not unwrap one that was.

Here is what is wanted, from this site.

Source:

    ---
    title: New unique package
    date: 2024-08-27
    by:
    - Michael Knyszek
    tags:
    - interning
    - unique
    summary: New package for interning in Go 1.23.
    ---

Answer:

    ---
    title: Gói unique mới
    date: 2024-08-27
    by:
    - Michael Knyszek
    tags:
    - interning
    - unique
    summary: Gói mới để interning trong Go 1.23.
    ---

Two things in that example are the whole rule. `interning` and `unique` are tags
and stayed in English even though `interning` also appears in the summary, where
it is prose and stayed in English for a different reason, which is that the
glossary says so. And no `template` key appeared, because there was none.

{{RULES}}

{{GLOSSARY}}
{{NOTE}}
Write the block and nothing else. No explanation, no fence around it, no note.
The first line of your answer is `---` and the last line of your answer is `---`.

The block sits between the two lines of equals signs.

==========

{{BODY}}

==========
