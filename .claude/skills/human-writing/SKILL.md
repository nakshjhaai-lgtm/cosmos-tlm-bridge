---
name: human-writing
description: "Draft, edit, rewrite, or review professional technical prose for accuracy, clarity, and a natural human voice. Use for READMEs, documentation, specifications, reports, tutorials, release notes, PR descriptions, commit messages, code comments, and technical explanations. Also use when asked to remove AI-sounding language, reduce fluff, preserve an author's voice, or adapt writing to an existing project style."
---

# Human writing

Produce writing that is accurate, direct, and appropriate for its audience. Improve the text rather than optimizing it to evade AI detection.

## Priorities

1. Preserve facts, meaning, constraints, and the author's intended level of certainty.
2. Follow the repository's style guide and nearby writing before applying this skill's defaults.
3. Match the document's purpose, audience, and expected level of detail.
4. Prefer concrete nouns, precise verbs, and evidence over promotional language.
5. Keep necessary terminology and natural constructions even when they appear in a style-warning list.

## Workflow

1. Inspect the request, source material, and nearby documents. Identify the audience, purpose, required format, and house style.
2. For a rewrite, preserve the original claims and voice unless the user requests a change. Do not silently add or remove commitments.
3. Draft or edit using [professional standards](references/standards.md).
4. Review [formulaic writing signals](references/ai-tells.md) when the prose feels generic, promotional, repetitive, or mechanically structured. Treat them as diagnostic clues, not prohibited forms.
5. Run the final checks below and revise only where the change improves accuracy or comprehension.

## Final checks

- Support factual claims with the supplied material or a verifiable source.
- Never invent measurements, benchmarks, versions, citations, quotations, dates, or RFC applicability. Ask for evidence or qualify the statement when verification is unavailable.
- Preserve meaning and uncertainty during edits. Mark unresolved contradictions instead of guessing.
- Confirm the result answers the request and includes all required sections, steps, warnings, and constraints.
- Use terminology consistently. Verify commands, code, links, and examples when tools or source material permit.
- Remove vague attribution, unsupported interpretation, repetition, and promotional filler.
- Keep headings, lists, tables, emphasis, and emojis only when they fit the target format and established style.
- Read the result once for flow. Vary sentence and paragraph shape when the current rhythm is mechanical, not merely for variety.

## References

- [Professional standards](references/standards.md): positive guidance for structure, tone, evidence, and common document types.
- [Formulaic writing signals](references/ai-tells.md): contextual editing signals for generic or AI-sounding prose.
