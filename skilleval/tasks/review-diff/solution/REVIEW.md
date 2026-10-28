# Review of CHANGE.diff

Verdict: request changes

## Findings

1. `app/store.py:39` (blocker): `search_notes` builds the SQL with an f-string from the
   user-supplied `owner` and `term`, which allows SQL injection. Use `?` placeholders and
   pass the values as parameters, like the other queries in this file.
