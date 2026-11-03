# Professional writing standards

Use these defaults when the project has no stronger style convention.

## Accuracy and evidence

- State only what the source material or verified evidence supports.
- Distinguish observed behavior, requirements, assumptions, estimates, and recommendations.
- Use exact numbers, versions, dates, standards, and RFCs only when known and relevant.
- Preserve qualifiers such as `may`, `typically`, and `under these conditions` when certainty is limited.
- Do not turn an example into a general claim.

Prefer evidence-backed specificity:

```text
Vague: The new pipeline is significantly faster.
Specific: In the supplied benchmark, median processing time fell from 240 ms to 95 ms.
```

If no measurement is available, describe the concrete change without claiming a performance result.

## Audience and voice

- Match the reader's knowledge and the action they need to take.
- Preserve the author's recognizable voice unless asked to replace it.
- Use neutral language for references and specifications. Use a warmer voice for tutorials or community documents when appropriate.
- Keep documentation independent from the surrounding chat persona.
- Explain unfamiliar abbreviations on first use unless the audience is expected to know them.

## Structure

- Put the decision, result, requirement, or next action first.
- Organize sections around reader tasks rather than a fixed introduction-body-conclusion template.
- Use paragraphs for reasoning, lists for discrete items, tables for genuine comparisons, and code blocks for executable material.
- Use imperative verbs for procedures: `Run`, `Configure`, `Verify`.
- Keep a heading with the content it introduces. Remove sections that add no useful information.
- Follow the project's heading capitalization and formatting conventions.

## Sentences and paragraphs

- Prefer concrete nouns and direct verbs over strings of adjectives and adverbs.
- Keep the subject and verb close when possible.
- Use transitions that express a real relationship: cause, contrast, sequence, or consequence.
- Repeat a precise term when a synonym would introduce ambiguity.
- Vary sentence length when it improves flow, but do not manufacture variation.
- Make each paragraph advance one identifiable point.

## Document-specific defaults

- **README:** State what the project does, prerequisites, setup, basic use, and verification. Add other sections only when the project needs them.
- **Procedure or tutorial:** Include prerequisites, ordered actions, expected results, and recovery guidance for likely failures.
- **Specification:** Separate requirements from rationale and examples. Use normative terms consistently.
- **PR description or release note:** Describe the behavior change, reason, risk, migration impact, and verification relevant to the change.
- **Code comment:** Explain intent, constraints, or a non-obvious tradeoff. Do not narrate the code.
- **Rewrite or edit:** Preserve claims, scope, uncertainty, links, citations, and intentional terminology.

## Formatting

- Use formatting to aid scanning, not to decorate every statement.
- Avoid mechanical boldface and unnecessary callouts.
- Use emojis only when the target document or established style calls for them.
- Keep naturally sized lists. Three items are acceptable when there are three real items.
