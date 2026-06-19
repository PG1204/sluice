# 0002 — Parser design: hand-written, recursive descent + Pratt

- **Status:** Accepted
- **Date:** 2026-06-19

## Context

Phase 1 turns SQL strings into an AST. Two design axes mattered: how to build
the parser (generator vs. by hand), and how to structure expression parsing
(precedence cascade vs. precedence climbing).

## Decisions

1. **Hand-written lexer and parser** — no parser generator (yacc/ANTLR/peg).
2. **Recursive descent for statement structure, a Pratt parser (precedence
   climbing) for expressions.**
3. **Errors are values, fail-fast** — every production returns `(node, error)`;
   parsing stops at the first error with a `line:column` position.

## Why

### Hand-written over a generator
- **Error quality.** We control the message and the source position exactly
  (`expected FROM, got "orders"` at `1:14`). Generators produce generic
  "syntax error near X" messages that are hard to improve.
- **It's the point of the exercise.** This is a portfolio project; a
  hand-written parser is the thing to be able to explain line by line.
- **No grammar-tool dependency** and no build-time codegen step.
- Cost: more code and we own the correctness. Mitigated by heavy table-driven
  tests (87% coverage, 30+ round-trip queries).

### Pratt parser for expressions
The textbook alternative is a method per precedence level
(`parseOr → parseAnd → parseComparison → parseSum → parseProduct → parsePrimary`).
That's ~6 near-identical methods that grow every time we add an operator.

Precedence climbing collapses all of that into one `parseExpression(minPrec)`
loop plus a prefix dispatch, driven by a small binding-power table. Adding an
operator is a one-line table entry, not a new method. Left-associativity falls
out of recursing at the operator's own precedence; right-associativity would be
`prec-1` (we have none yet). This is less code, easier to extend, and a clean
thing to whiteboard in an interview.

### Fail-fast errors
A query is short. The first syntax error is almost always the real problem, and
error *recovery* (synchronizing to resume parsing and collect more errors) adds
real complexity for little benefit here. Returning one precise, located error
is simpler and more useful. We can revisit if multi-error reporting ever earns
its keep.

## Consequences

- `lexer.Token` carries a `Position` (line/column/offset); the parser threads
  it into `ParseError`.
- The AST pretty-printer is precedence-aware, so it emits minimally
  parenthesized SQL that re-parses to an identical tree — the round-trip is a
  cheap, strong test oracle.
- Joins are parsed in a loop even though Phase 1 scopes to one join; the AST
  already holds a slice, so later phases need no parser change.
