"""Fail when one scope defines the same function or class twice (G-013 C, pyflakes F811).

Every module, class and function body is checked, nested functions included (stricter than the spec, intended).
"""
import ast
import os
import unittest

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SCAN_DIRS = ("scout", "web", "ci", os.path.join("roles", "hooks"), "tests")

# (relative path, scope, name) tuples that are known duplicates on main.
# Each entry needs a comment with the reason. Empty: main had none when this was added.
ALLOW = set()


def scan_files():
    files = [os.path.join(ROOT, "bin", "factory")]
    for d in SCAN_DIRS:
        for dirpath, dirnames, filenames in os.walk(os.path.join(ROOT, d)):
            dirnames[:] = sorted(n for n in dirnames if n != "__pycache__")
            files.extend(os.path.join(dirpath, n) for n in sorted(filenames) if n.endswith(".py"))
    return files


def _is_overload(node):
    """True when a decorator is exactly @overload or @typing.overload (not e.g. @register(overload))."""
    for dec in node.decorator_list:
        if isinstance(dec, ast.Name) and dec.id == "overload":
            return True
        if isinstance(dec, ast.Attribute) and dec.attr == "overload" and isinstance(dec.value, ast.Name) and dec.value.id == "typing":
            return True
    return False


def _accessor_kind(node):
    """Return (base, kind) for an @<base>.setter/.deleter/.getter decorator, else None."""
    for dec in node.decorator_list:
        if (isinstance(dec, ast.Attribute) and dec.attr in ("setter", "deleter", "getter")
                and isinstance(dec.value, ast.Name)):
            return dec.value.id, dec.attr
    return None


def find_duplicates(source, filename="<src>"):
    """Return [(lineno, scope, name)] for names defined twice by direct statements of one scope."""
    tree = ast.parse(source, filename=filename)
    found = []

    def check(body, scope):
        seen = {}
        accessors = set()
        for stmt in body:
            if isinstance(stmt, (ast.FunctionDef, ast.AsyncFunctionDef, ast.ClassDef)):
                if _is_overload(stmt):
                    continue
                acc = _accessor_kind(stmt)
                if acc and acc[0] == stmt.name and stmt.name in seen:
                    if acc in accessors:
                        found.append((stmt.lineno, scope, stmt.name))
                    accessors.add(acc)
                    continue
                if stmt.name in seen:
                    found.append((stmt.lineno, scope, stmt.name))
                seen[stmt.name] = stmt.lineno

    check(tree.body, "<module>")
    for node in ast.walk(tree):
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef, ast.ClassDef)):
            check(node.body, node.name)
    return found


class NoDuplicateDefs(unittest.TestCase):
    def test_scan_covers_files(self):
        files = scan_files()
        self.assertTrue(os.path.exists(files[0]), "bin/factory missing")
        self.assertGreater(len(files), 20, "scan found too few files: %d" % len(files))

    def test_no_duplicate_defs_in_repo(self):
        problems = []
        for path in scan_files():
            rel = os.path.relpath(path, ROOT)
            with open(path, encoding="utf-8") as f:
                src = f.read()
            for lineno, scope, name in find_duplicates(src, rel):
                if (rel, scope, name) in ALLOW:
                    continue
                problems.append("%s:%d: duplicate def %s in %s" % (rel, lineno, name, scope))
        self.assertEqual(problems, [], "remove the duplicate or add a commented ALLOW entry:\n" + "\n".join(problems))

    def test_detects_module_duplicate(self):
        self.assertEqual(find_duplicates("def a():\n    pass\n\ndef a():\n    pass\n"), [(4, "<module>", "a")])

    def test_detects_class_duplicate(self):
        src = "class C:\n    def m(self): pass\n    def m(self): pass\n"
        self.assertEqual(find_duplicates(src), [(3, "C", "m")])

    def test_detects_duplicate_class_name(self):
        self.assertEqual(find_duplicates("class A: pass\nclass A: pass\n"), [(2, "<module>", "A")])

    def test_if_else_alternatives_allowed(self):
        src = "if X:\n    def f(): pass\nelse:\n    def f(): pass\ntry:\n    def g(): pass\nexcept E:\n    def g(): pass\n"
        self.assertEqual(find_duplicates(src), [])

    def test_property_setter_and_overload_allowed(self):
        src = (
            "class C:\n"
            "    @property\n    def v(self): pass\n"
            "    @v.setter\n    def v(self, x): pass\n"
            "    @v.deleter\n    def v(self): pass\n"
            "@overload\ndef f(a): ...\n@overload\ndef f(a, b): ...\ndef f(*a): pass\n"
        )
        self.assertEqual(find_duplicates(src), [])

    def test_second_setter_flagged(self):
        src = (
            "class C:\n"
            "    @property\n    def v(self): pass\n"
            "    @v.setter\n    def v(self, x): pass\n"
            "    @v.setter\n    def v(self, x): pass\n"
        )
        self.assertEqual(find_duplicates(src), [(7, "C", "v")])

    def test_foreign_setter_does_not_exempt(self):
        src = (
            "class C:\n"
            "    @property\n    def v(self): pass\n"
            "    def w(self): pass\n"
            "    @v.setter\n    def w(self, x): pass\n"
        )
        self.assertEqual(find_duplicates(src), [(6, "C", "w")])

    def test_overload_inside_other_decorator_flagged(self):
        src = "@register(overload)\ndef f(a): ...\ndef f(*a): pass\n"
        self.assertEqual(find_duplicates(src), [(3, "<module>", "f")])

    def test_typing_overload_and_full_accessor_set_allowed(self):
        src = (
            "@typing.overload\ndef f(a): ...\ndef f(*a): pass\n"
            "class C:\n"
            "    @property\n    def v(self): pass\n"
            "    @v.getter\n    def v(self): pass\n"
            "    @v.setter\n    def v(self, x): pass\n"
            "    @v.deleter\n    def v(self): pass\n"
        )
        self.assertEqual(find_duplicates(src), [])

    def test_allow_set_entries_exist(self):
        # an ALLOW entry that no longer matches is stale and must be dropped
        live = set()
        for path in scan_files():
            rel = os.path.relpath(path, ROOT)
            with open(path, encoding="utf-8") as f:
                for _, scope, name in find_duplicates(f.read(), rel):
                    live.add((rel, scope, name))
        self.assertEqual(ALLOW - live, set(), "stale ALLOW entries")


if __name__ == "__main__":
    unittest.main()
