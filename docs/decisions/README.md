# Design decisions

Short Architecture Decision Records (ADRs). Each non-obvious choice gets one
file capturing the context, the decision, and the tradeoffs we accepted — so
the reasoning survives even after the code changes.

## Convention

- One file per decision: `NNNN-short-title.md` (zero-padded, monotonic).
- Sections: **Context** → **Decision** → **Why** → **Tradeoffs** →
  **Consequences**.
- Records are immutable. To change course, add a new record that supersedes an
  earlier one rather than editing history.
