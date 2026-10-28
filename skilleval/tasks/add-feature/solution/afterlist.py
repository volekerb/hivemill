"""Helpers for the `--after` option of a task-creation command."""

import re

TASK_ID = re.compile(r"^T-(\d+)$", re.IGNORECASE)


def parse_after(text, known=None, self_id=None):
    """Parse a comma-separated list of task ids, e.g. "T-1, t-2".

    Rules:
      * Ids are case-insensitive "T-<digits>" and are returned normalized
        to upper case ("t-2" -> "T-2"). Surrounding whitespace is ignored.
      * An empty or whitespace-only `text` (or None) means "no dependencies"
        and returns [].
      * Duplicates are dropped; the first occurrence keeps its position.
      * An empty entry between commas ("T-1,,T-2" or a trailing comma)
        raises ValueError("empty entry in --after list").
      * An entry that is not a task id raises ValueError("invalid task id: <entry>"),
        with the entry as the user typed it (stripped).
      * If `known` (a collection of ids) is given, an id not in it raises
        ValueError("unknown task id: <ID>"). Checked after normalizing.
      * If `self_id` is given and appears in the list, raise
        ValueError("task cannot depend on itself: <ID>").
    Returns the list of ids.
    """
    if text is None or not text.strip():
        return []
    result = []
    for raw in text.split(","):
        entry = raw.strip()
        if not entry:
            raise ValueError("empty entry in --after list")
        if not TASK_ID.match(entry):
            raise ValueError("invalid task id: %s" % entry)
        tid = entry.upper()
        if known is not None and tid not in known:
            raise ValueError("unknown task id: %s" % tid)
        if self_id is not None and tid == self_id.upper():
            raise ValueError("task cannot depend on itself: %s" % tid)
        if tid not in result:
            result.append(tid)
    return result
