---
inclusion: always
---

# Edit Safety

- Before any file edit, read the exact target path first in the current session/context (`cave__read` preferred).
- Built-in write tools and Cave Tools both need a prior read of the target path when the host enforces it.
- In Plan Mode / read-only phase, never call write-capable tools: `fs_write`, `write`, `cave__edit`, `cave__write`, `cave__apply_patch`, or shell commands that modify files.
- Do not batch read and edit calls in parallel. Read must complete before edit.
- Prefer `cave__read` for inspection, then `cave__edit` or `cave__apply_patch` for edits.
- After edits made outside Cave Tools, call `cave__invalidate` with changed path(s). Do not use empty `cave__write` for cache-only invalidation.
