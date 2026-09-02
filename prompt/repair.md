A Vietnamese translation of a piece of go.dev was checked against its English
and it failed. Below is the English, then the Vietnamese as it stands, then what
the check found. Give back the whole Vietnamese piece with those findings fixed.

The prose is probably fine. Every finding below is about the shape of the file
and not about the quality of the sentences, because that is the only thing the
check can see. So change what the findings name and leave the rest alone. Do not
rewrite a sentence that nothing complained about, do not improve the phrasing,
do not re-translate the piece from the English. A repair that comes back reading
differently everywhere is a repair nobody can review, and it is how a second
defect gets in while the first one is being taken out.

Give back the whole piece and not a patch. The answer replaces the file, so an
answer that is only the fixed paragraph loses everything around it.

Look for a backslash first. When the finding is that headings or links have gone
missing and you can see them in the Vietnamese, they are almost certainly there
with a backslash in front of them, and `\#` is not a heading and `\(` is not the
start of a link target. Take the backslashes out. That one cause is behind more
of these findings than everything else together, and taking it out is the whole
repair: change nothing else.

If a finding is wrong, fix nothing for it and leave that part as it is. Say
nothing about it: your answer is the file, and there is nowhere in it to put a
note. A finding that keeps coming back on work that is right is a bug in the
check, and it gets found by the file failing three times and being put in front
of a person, which is what the third attempt is for.

{{RULES}}

%%%%%

{{WHERE}}

{{GLOSSARY}}
{{NOTE}}
Write the corrected Vietnamese and nothing else. No preamble, no list of what
you changed, no fence around the answer.

The English sits between the first pair of lines of equals signs.

==========

{{BODY}}

==========

The Vietnamese as it stands sits between the second pair.

==========

{{PREVIOUS}}

==========

What the check found:

{{FINDINGS}}
