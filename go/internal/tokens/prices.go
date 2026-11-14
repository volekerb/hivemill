// Package tokens is the Go port of web/tokens.py (token tally of Claude Code transcripts and attribution to factory
// agents, tasks and goals), web/cursor_limits.py (Cursor plan limits) and web/prices.py (price table).
//
// Python and Go run side by side, so every JSON shape here is the one Python writes and reads: the TokenIndex
// snapshot (Python's idx.snapshot()) and the config row "cursor_model_limits". Cost numbers use the same float
// operations in the same order as Python, so they agree to the last bit.
package tokens

// PriceRow is USD per million tokens for one model.
type PriceRow struct{ Input, Output, CacheRead, CacheWrite5m, CacheWrite1h float64 }

// PricesAsOf mirrors web/prices.py PRICES_AS_OF.
const PricesAsOf = "2026-10-25"

// Prices mirrors PRICES (source: platform.claude.com pricing page, checked 2026-10-25).
var Prices = map[string]PriceRow{
	"claude-opus-5-5":   {Input: 4.00, Output: 20.00, CacheRead: 0.20, CacheWrite5m: 5.00, CacheWrite1h: 8.00},
	"claude-sonnet-5-5": {Input: 2.00, Output: 10.00, CacheRead: 0.10, CacheWrite5m: 2.50, CacheWrite1h: 4.00},
	"claude-haiku-5-5":  {Input: 0.10, Output: 0.50, CacheRead: 0.01, CacheWrite5m: 0.125, CacheWrite1h: 0.20},
}

// HaikuLongPrompt is the >100K prompt tier; like in Python it is documented but never applied.
var HaikuLongPrompt = PriceRow{Input: 0.50, Output: 2.50, CacheRead: 0.05, CacheWrite5m: 0.625, CacheWrite1h: 1.00}

// FastMultiplier mirrors FAST_MULTIPLIER (usage.speed == "fast").
var FastMultiplier = map[string]float64{"claude-opus-5-5": 2.0}

// Usage is the token counts Cost prices.
type Usage struct{ Input, Output, CacheRead, CacheWrite5m, CacheWrite1h int64 }

// Cost is prices.cost: estimated USD for u, ok=false (Python None) for a model without a price. speed "fast" applies
// FastMultiplier. The sum is left to right in the Python FIELDS order, then mult*sum/1e6, as in Python.
func Cost(model string, u Usage, speed string) (float64, bool) {
	p, ok := Prices[model]
	if !ok {
		return 0, false
	}
	mult := 1.0
	if speed == "fast" {
		if m, has := FastMultiplier[model]; has {
			mult = m
		}
	}
	s := 0.0
	s += p.Input * float64(u.Input)
	s += p.Output * float64(u.Output)
	s += p.CacheRead * float64(u.CacheRead)
	s += p.CacheWrite5m * float64(u.CacheWrite5m)
	s += p.CacheWrite1h * float64(u.CacheWrite1h)
	return mult * s / 1_000_000, true
}
