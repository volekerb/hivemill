"""Small string helpers."""


def clean_title(title):
    """Collapse whitespace and strip a note title."""
    return " ".join(title.split())


def truncate(text, limit=80):
    """Shorten text to at most `limit` characters, ending with an ellipsis."""
    if len(text) <= limit:
        return text
    return text[: limit - 1] + "\u2026"
