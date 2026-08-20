---
name: review
description: what to look for in a change here
---

Read the diff, then check for:

- Comments that say what the code does rather than why it does it
- A `Background()` call anywhere in `internal/ui`, which breaks transparency
- Errors from an endpoint that are paraphrased rather than shown verbatim
- A tool deciding its own permissions instead of leaving it to `Agent.dispatch`
- Anything that fails open: a dropped rule, a skipped check, a default that
  grants more than it refuses

Say what you would change and why. Do not change it yourself.
