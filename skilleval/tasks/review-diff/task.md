# Review a change

The working directory holds a small notes service (`app/`, `README.md`) in
the state *after* a change, and `CHANGE.diff`, the diff of that change.

## What to do

Review `CHANGE.diff` as you would a pull request before it is merged. Look
for real defects: correctness, security, data handling and fit with the
surrounding code. Do not edit any file except the one you are asked to write.

Write your review to `REVIEW.md` in the working directory:

- the first line after the title is the verdict, either `Verdict: approve`
  or `Verdict: request changes`;
- then a numbered list of findings, most severe first. Each finding starts
  its line with the location as `path:line` (for example `app/foo.py:12`),
  followed by what is wrong and how to fix it. Use the line numbers of the
  files as they are in the working directory.

## Rules

- Report only problems you are confident about. List at most 5 findings;
  a long list of nitpicks counts against the review.
- Style preferences and speculation are not findings.

## Done means

`REVIEW.md` exists, has a verdict, and its findings point at the exact
file and line of each real problem. Leave it in the working directory.
