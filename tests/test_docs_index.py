import os
import re
import unittest
from collections import Counter

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DOCS = os.path.join(ROOT, "docs")
INDEX = os.path.join(DOCS, "README.md")
LINK = re.compile(r"\]\(([^)#\s]+)")


def doc_pages():
    pages = []
    for base, dirs, files in os.walk(DOCS):
        rel = os.path.relpath(base, DOCS)
        if rel == "demo" or rel.startswith("demo" + os.sep):
            dirs[:] = []
            continue
        for f in files:
            p = os.path.normpath(os.path.join(rel, f))
            if f.endswith(".md") and p != "README.md":
                pages.append(p)
    return sorted(pages)


def index_links(text):
    return Counter(os.path.normpath(m) for m in LINK.findall(text))


class DocsIndexTests(unittest.TestCase):
    def setUp(self):
        with open(INDEX, encoding="utf-8") as fh:
            self.text = fh.read()

    def test_every_page_linked_exactly_once(self):
        links = index_links(self.text)
        problems = [f"{p}: linked {links.get(p, 0)} times" for p in doc_pages() if links.get(p, 0) != 1]
        self.assertEqual(problems, [], "docs/README.md must link each page exactly once")

    def test_no_dangling_links(self):
        missing = [p for p in index_links(self.text) if not os.path.exists(os.path.join(DOCS, p))]
        self.assertEqual(missing, [])

    def test_four_groups(self):
        heads = re.findall(r"^## (.+)$", self.text, re.M)
        self.assertEqual(heads, ["Use it", "Operate it", "Contribute", "Internals"])

    def test_missing_link_is_detected(self):
        page = doc_pages()[0]
        stripped = re.sub(r"^.*\]\(%s\).*\n" % re.escape(page), "", self.text, flags=re.M)
        self.assertNotEqual(stripped, self.text)
        self.assertEqual(index_links(stripped).get(page, 0), 0)

    def test_duplicate_link_is_detected(self):
        page = doc_pages()[0]
        doubled = self.text + f"\n- [again]({page})\n"
        self.assertEqual(index_links(doubled)[page], 2)


if __name__ == "__main__":
    unittest.main()
