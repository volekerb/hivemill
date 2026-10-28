"""Model price table and cost estimate for the token views (stdlib only)."""

# USD per million tokens.
# source: platform.claude.com pricing page, checked 2026-10-25 (cache write 5m = 1.25x input, 1h = 2x input).
# Haiku rows are the <=100K prompt tier; the >100K tier is in HAIKU_LONG_PROMPT but not applied, because the
# token index aggregates per session x model x day and does not keep per-request prompt sizes.
PRICES = {
    "claude-opus-5-5": {"input": 4.00, "output": 20.00, "cache_read": 0.20, "cache_write_5m": 5.00, "cache_write_1h": 8.00},
    "claude-sonnet-5-5": {"input": 2.00, "output": 10.00, "cache_read": 0.10, "cache_write_5m": 2.50, "cache_write_1h": 4.00},
    "claude-haiku-5-5": {"input": 0.10, "output": 0.50, "cache_read": 0.01, "cache_write_5m": 0.125, "cache_write_1h": 0.20},
}
HAIKU_LONG_PROMPT = {"input": 0.50, "output": 2.50, "cache_read": 0.05, "cache_write_5m": 0.625, "cache_write_1h": 1.00}
PRICES_AS_OF = "2026-10-25"

FAST_MULTIPLIER = {"claude-opus-5-5": 2.0}  # usage.speed == "fast": $8 in / $40 out (2x all rates)

FIELDS = ("input", "output", "cache_read", "cache_write_5m", "cache_write_1h")


def cost(model, usage, speed=None):
    """Estimated USD for `usage` ({input, output, cache_read, cache_write_5m, cache_write_1h} token counts),
    or None for a model without a price (the UI shows n/a)."""
    p = PRICES.get(model)
    if p is None:
        return None
    mult = FAST_MULTIPLIER.get(model, 1.0) if speed == "fast" else 1.0
    return mult * sum(p[f] * (usage.get(f) or 0) for f in FIELDS) / 1_000_000
