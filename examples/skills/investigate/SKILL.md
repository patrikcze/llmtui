---
schema_version: 1
id: investigate
name: Investigate & Fact-Check
version: 1.0.0
description: Structured daily investigation and fact-checking. Use when the user wants to research a topic, verify a claim, compare options, troubleshoot a question, or find out what is true — anything where the answer must rest on evidence rather than memory.
tags:
  - research
  - fact-check
  - investigation
  - daily
triggers:
  - investigate this
  - fact check this
  - is it true that
  - find out whether
  - research this topic
recommended_tools:
  - web_search
  - web_fetch
  - read_file
  - grep
capabilities:
  tool_calling: optional
---

# Investigate & Fact-Check

Work in this exact loop. Do not skip steps and do not answer before step 4.

## The loop

1. **Frame.** Restate the question as one or more checkable claims. Note what
   would count as proof for each. If the question is ambiguous, pick the most
   likely reading, say so in one line, and continue.
2. **Gather.** Collect evidence before forming an opinion:
   - Local material first: `grep` / `glob` to locate, `read_file` to read.
   - Current or external facts: `web_search` to find candidate sources, then
     `web_fetch` the most authoritative results and read them.
   - Prefer primary sources (official docs, standards, the project itself,
     the original announcement) over blogs and forums.
3. **Weigh.** For each claim, line the evidence up for and against. Check
   dates — a source can be genuine but outdated. If two sources disagree,
   say so; do not silently pick one.
4. **Conclude.** Give each claim a verdict: **confirmed**, **refuted**,
   **mixed**, or **unverified** — plus one line of the strongest evidence.
5. **Answer.** Lead with the verdict and the direct answer, then the
   supporting evidence with its sources, then anything left unverified.

## Evidence rules

- Never cite a page you did not fetch in this session. Search snippets are
  leads, not evidence.
- Quote the exact wording for anything contentious; paraphrase everything
  else.
- Separate three things clearly in your answer: what a source says, what you
  infer from it, and what you remember from training. Only the first is a
  verified fact.
- One independent source is a lead; two independent sources are evidence.
  For a surprising or high-stakes claim, do not stop at one.
- Note the publication or last-updated date of key sources when recency
  matters.

## When you cannot verify

- If web tools are unavailable, say so, answer from model knowledge, mark
  the answer as **unverified (from memory, may be outdated)**, and suggest
  the user enable `/web on` if verification matters.
- If the evidence is thin, say "unverified" plainly. A confident-sounding
  guess is a wrong answer here.
- Never invent URLs, titles, quotes, numbers, or dates. A missing fact is
  reported as missing.

## Answer shape

- Verdict first, in the first sentence.
- Then a short evidence list: source → what it establishes.
- Then caveats: what was not checked, where sources disagreed, how fresh
  the evidence is.
- Keep it compact. The user wants the answer and its basis, not a research
  diary.
