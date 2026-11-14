// Turns the project's notes text into the plain-text TestFlight "What to Test" string. No dependencies.
//
// If the text has a "## What to Test" heading, only that section is used (up to the next "## " heading or the end).
// Otherwise the whole text is used. Markdown "**" and backticks are stripped, "- " bullets and blank-line structure
// are kept (3+ blank lines collapse to 2), and the result ends with one newline. TestFlight allows 4000 characters.
export const MAX_CHARS = 4000;

export function convert(text) {
  let lines = String(text).replace(/\r\n?/g, "\n").split("\n");
  const start = lines.findIndex((l) => /^##\s+What to Test\b/i.test(l));
  if (start !== -1) {
    let end = lines.findIndex((l, i) => i > start && /^##\s/.test(l));
    if (end === -1) end = lines.length;
    lines = lines.slice(start + 1, end);
  }
  const out = lines
    .map((l) => l.replace(/\*\*/g, "").replace(/`/g, "").trimEnd())
    .join("\n")
    .replace(/\n{3,}/g, "\n\n")
    .trim();
  if (!out) throw new Error("the What to Test text is empty");
  if (out.length > MAX_CHARS) throw new Error(`What to Test is ${out.length} characters; TestFlight allows ${MAX_CHARS}`);
  return out + "\n";
}
