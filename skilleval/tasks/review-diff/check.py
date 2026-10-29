"""Check REVIEW.md in the current directory against the planted defect.

Passes iff REVIEW.md names the defect's file with a line number within +-2,
has a "Verdict:" line saying "request changes", and has at most 5 distinct
path:line or path: line (optionally -range) references, none spanning more than 10 lines.
"""
import json
import re
import sys

MAX_FINDINGS = 5
LINE_SLACK = 2
REF = re.compile(r"([\w./-]+\.[A-Za-z]\w*):[ \t]?(\d+)(?:-(\d+))?")
MAX_RANGE = 10
VERDICT = re.compile(r"^[\s>*_#-]*verdict\b[\s*_:-]*(.*)$", re.IGNORECASE | re.MULTILINE)


def fail(msg):
    print("FAIL: " + msg)
    sys.exit(1)


try:
    expected = json.load(open("expected.json"))
except (OSError, ValueError):
    fail("expected.json missing or invalid")
try:
    text = open("REVIEW.md", encoding="utf-8", errors="replace").read()
except OSError:
    fail("REVIEW.md not found")

verdicts = [v.strip().lower() for v in VERDICT.findall(text)]
if not verdicts:
    fail('no "Verdict:" line')
if not all(re.match(r"[\s*_`]*request[\s_-]+changes", v) for v in verdicts):
    fail('verdict is not "request changes"')

findings = {(m.group(1), int(m.group(2)), int(m.group(3) or m.group(2))) for m in REF.finditer(text)}
if len(findings) > MAX_FINDINGS:
    fail("%d findings listed (max %d)" % (len(findings), MAX_FINDINGS))

for m in REF.finditer(text):
    if m.group(3) and int(m.group(3)) - int(m.group(2)) > MAX_RANGE:
        fail("line range %s-%s too wide (max %d lines)" % (m.group(2), m.group(3), MAX_RANGE))

for m in REF.finditer(text):
    path, start = m.group(1), int(m.group(2))
    end = int(m.group(3)) if m.group(3) else start
    if path.endswith(expected["file"]) and \
            start - LINE_SLACK <= expected["line"] <= end + LINE_SLACK:
        print("PASS: defect at %s:%d found" % (expected["file"], expected["line"]))
        sys.exit(0)
fail("no finding at %s line %d (+-%d)" % (expected["file"], expected["line"], LINE_SLACK))
