#!/bin/sh
# T-1624: commit-msg hook body for runtime=cursor task clones. Deletes only "Co-authored-by: Cursor ..." lines
# (case-insensitive, any email) and, when one was removed, the blank lines left at the end of the message.
# A message without such a line is not touched. Runs from the factory checkout (bin/), never from the PR branch
# inside the clone. Usage: cursor-commit-msg-strip.sh <msg-file>
f="$1"
[ -f "$f" ] || { echo "cursor-commit-msg-strip: no message file given" >&2; exit 1; }
tmp=$(mktemp "${f}.XXXXXX") || exit 1
dropped=0
while IFS= read -r line || [ -n "$line" ]; do
  lower=$(printf '%s' "$line" | tr 'A-Z' 'a-z')
  case "$lower" in
    "co-authored-by: cursor"*) dropped=1; continue ;;
  esac
  printf '%s\n' "$line" >> "$tmp"
done < "$f"
if [ "$dropped" = 1 ]; then
  while [ -s "$tmp" ] && [ -z "$(tail -n 1 "$tmp" | tr -d ' \t')" ]; do
    head -n -1 "$tmp" > "$tmp.t" && mv "$tmp.t" "$tmp"
  done
  cat "$tmp" > "$f"
fi
rm -f "$tmp" "$tmp.t"
