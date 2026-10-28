#!/usr/bin/env python3
"""Seed a self-contained demo FACTORY_HOME with fictional data, for screenshots and documentation examples.

`python3 ci/demo_seed.py --home DIR [--now ISO] [--force]` runs `bin/factory init` of this checkout against DIR, then
fills the DB that init created (DIR/db/factory.db; stdlib sqlite3, schema created by init; timestamps cannot go through the CLI, which stamps the
real clock) and writes fake Claude usage logs under DIR/home/.claude for the tokens page. It never opens the live
factory DB and never writes outside DIR. Stdlib only. See docs/demo-data.md.
Exit codes: 0 seeded, 1 failure (including a scrub hit in the demo DB), 2 usage error, 3 refused.
"""
import argparse
import datetime as dt
import functools
import hashlib
import importlib.util
import json
import os
import pwd
import random
import shutil
import sqlite3
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
FACTORY = ROOT / "bin" / "factory"
SCAN = ROOT / "ci" / "scrub_scan.py"
REFUSE = "refusing to seed the live factory home"
DUMP_NAME = ".scrub-dump.sql"
SEED = 20261025


def db_path(home):
    """The DB `bin/factory` opens for `home`: the same web/dbpath.py resolution (db/factory.db, else the legacy file)."""
    spec = importlib.util.spec_from_file_location("factory_dbpath", ROOT / "web" / "dbpath.py")
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod.resolve(home)

MODEL = {"boss": "claude-opus-5-5", "director": "claude-opus-5-5", "lead": "claude-opus-5-5",
         "pm": "claude-opus-5-5", "reviewer": "claude-opus-5-5", "judge": "claude-opus-5-5",
         "coder": "claude-sonnet-5-5", "qa": "claude-sonnet-5-5", "cleaner": "claude-haiku-5-5",
         "staff-engineer": "claude-opus-5-5"}
REPO = "https://github.com/example-org/%s/pull/%d"


class Refused(Exception):
    pass


# ───────────────────────────── guard ─────────────────────────────

def main_checkout():
    """Parent of the absolute git common dir of this checkout (the main checkout when ROOT is a worktree), or None."""
    try:
        p = subprocess.run(["git", "rev-parse", "--path-format=absolute", "--git-common-dir"], cwd=str(ROOT),
                           capture_output=True, text=True)
    except OSError:
        return None
    if p.returncode != 0 or not p.stdout.strip():
        return None
    return Path(p.stdout.strip()).resolve().parent


def live_homes():
    """Protected roots: the account's ~/factory (from the passwd entry, so a temp $HOME cannot hide it), the main
    checkout, the checkout this script lives in, and $FACTORY_HOME when set."""
    roots = [Path(pwd.getpwuid(os.getuid()).pw_dir) / "factory", ROOT, main_checkout()]
    if os.environ.get("FACTORY_HOME"):
        roots.append(Path(os.environ["FACTORY_HOME"]).expanduser())
    return [r.resolve() for r in roots if r is not None]


def check_target(home, force):
    """Resolved target dir; raises Refused for the live home (equal, inside, or an ancestor of it) or an existing DB."""
    target = Path(home).expanduser().resolve()
    for live in live_homes():
        if target == live or live in target.parents or target in live.parents:
            raise Refused(f"{REFUSE}: {target} is or overlaps {live}. Pass a fresh directory such as $(mktemp -d)/demo.")
    existing = db_path(target)
    if existing.exists() and not force:
        raise Refused(f"{existing} already exists. Pass --force to rebuild this demo home, or choose a new directory.")
    return target


# ───────────────────────────── time helpers ─────────────────────────────

def parse_now(s):
    if s is None:
        d = dt.datetime.now(dt.timezone.utc)
    else:
        try:
            d = dt.datetime.fromisoformat(s)
        except ValueError:
            raise ValueError(f"--now {s!r} is not an ISO timestamp such as 2026-10-25T12:00:00+00:00")
        if d.tzinfo is None:
            d = d.replace(tzinfo=dt.timezone.utc)
    return d.astimezone(dt.timezone.utc).replace(minute=0, second=0, microsecond=0)


def iso(d):
    return d.isoformat(timespec="seconds")


def sha(*parts):
    return hashlib.sha1("/".join(map(str, parts)).encode()).hexdigest()


# ───────────────────────────── demo content ─────────────────────────────

PROJECTS = [("acme-web", "acme-web", "example-org/acme-web"), ("factory", "factory", "example-org/factory"),
            ("ledger-api", "ledger-api", "example-org/ledger-api"), ("docs-site", "docs-site", "example-org/docs-site")]

# id, project, title, status, priority, intent, criteria, created_days_ago, updated_hours_ago
GOALS = [
    ("G-001", "acme-web", "Launch the public landing page", "done", 2, "Ship a simple landing page for the Acme web shop.",
     "1. Hero section is live.\n2. Pricing table is live.\n3. Contact form delivers messages.", 14.5, 192),
    ("G-002", "acme-web", "Build the checkout flow", "active", 1, "Let a visitor pay for a cart in one short flow.",
     "1. Cart page works.\n2. Shipping details are collected.\n3. Payment step completes an order.\n4. Confirmation email is sent.",
     12, 2),
    ("G-003", "acme-web", "Add account settings", "active", 2, "Give signed-in users a settings area.",
     "1. Profile page works.\n2. Password change works.\n3. Notification preferences are stored.", 10, 3),
    ("G-004", "factory", "Prototype the mobile app shell", "blocked", 3, "Explore a minimal mobile shell. Waiting for a design decision.",
     "1. Shell renders a home screen.\n2. Push notifications are stubbed.", 6, 20),
]

# team id, goal, lead, scope, status, created_days_ago
TEAMS = [("g-001-a", "G-001", "lead-g-001-a", "Landing page sections and contact form", "closed", 14),
         ("g-002-a", "G-002", "lead-g-002-a", "Cart, shipping, payment and confirmation", "active", 12),
         ("g-003-a", "G-003", "lead-g-003-a", "Profile, password and notification settings", "active", 10),
         ("g-004-a", "G-004", "lead-g-004-a", "Mobile shell prototype", "active", 6)]

# id, goal, team, role, title, status, depends, pr number, attempts, assignee
TASKS = [
    ("T-0001", "G-001", "g-001-a", "pm", "Write landing page requirements", "done", [], None, 1, "pm-t-0001"),
    ("T-0002", "G-001", "g-001-a", "coder", "Add hero section [SC1]", "done", ["T-0001"], 101, 1, "coder-t-0002"),
    ("T-0003", "G-001", "g-001-a", "coder", "Add pricing table [SC2]", "done", ["T-0001"], 102, 2, "coder-t-0003"),
    ("T-0004", "G-001", "g-001-a", "coder", "Add contact form [SC3]", "done", ["T-0001"], 103, 3, "coder-t-0004"),
    ("T-0005", "G-001", "g-001-a", "cleaner", "Remove merged landing page worktrees", "done", ["T-0004"], None, 1, "cleaner-t-0005"),
    ("T-0006", "G-002", "g-002-a", "pm", "Specify the checkout flow", "done", [], None, 1, "pm-t-0006"),
    ("T-0007", "G-002", "g-002-a", "coder", "Add cart page [SC1]", "done", ["T-0006"], 104, 1, "coder-t-0007"),
    ("T-0008", "G-002", "g-002-a", "coder", "Add shipping form [SC2]", "done", ["T-0006"], 105, 2, "coder-t-0008"),
    ("T-0009", "G-002", "g-002-a", "coder", "Add order summary", "todo", ["T-0006"], None, 0, None),
    ("T-0010", "G-002", "g-002-a", "coder", "Add payment step [SC3]", "in_review", ["T-0008"], 107, 2, "coder-t-0010"),
    ("T-0011", "G-002", "g-002-a", "qa", "Verify checkout end to end", "in_progress", ["T-0010"], None, 1, "qa-t-0011"),
    ("T-0012", "G-002", "g-002-a", "coder", "Add order confirmation email [SC4]", "in_progress", ["T-0008"], None, 1, "coder-t-0012"),
    ("T-0013", "G-002", "g-002-a", "judge", "Decide merge for the payment step", "todo", ["T-0011"], None, 0, None),
    ("T-0014", "G-002", "g-002-a", "coder", "Add receipt download", "todo", ["T-0010"], None, 0, None),
    ("T-0015", "G-003", "g-003-a", "pm", "Specify account settings", "done", [], None, 1, "pm-t-0015"),
    ("T-0016", "G-003", "g-003-a", "coder", "Add profile page [SC1]", "done", ["T-0015"], 108, 1, "coder-t-0016"),
    ("T-0017", "G-003", "g-003-a", "coder", "Add password change form [SC2]", "done", ["T-0015"], 109, 2, "coder-t-0017"),
    ("T-0018", "G-003", "g-003-a", "coder", "Add legacy theme switch", "cancelled", ["T-0015"], None, 0, None),
    ("T-0019", "G-003", "g-003-a", "coder", "Add notification preferences [SC3]", "in_progress", ["T-0016"], None, 1, "coder-t-0019"),
    ("T-0020", "G-003", "g-003-a", "coder", "Add avatar upload", "done", ["T-0016"], 110, 1, "coder-t-0020"),
    ("T-0021", "G-004", "g-004-a", "pm", "Specify the mobile app shell", "done", [], None, 1, "pm-t-0021"),
    ("T-0022", "G-004", "g-004-a", "coder", "Add push notification stub [SC2]", "blocked", ["T-0021"], None, 1, "coder-t-0022"),
    ("T-0023", "G-004", "g-004-a", "coder", "Render the home screen [SC1]", "todo", ["T-0021"], None, 0, None),
    ("T-0024", "G-004", "g-004-a", "reviewer", "Review the shell design notes", "todo", ["T-0021"], None, 0, None),
]

# merged task -> (hours before now that it merged, rejections in order: reviewer | qa | judge, gate-blocked once)
MERGED = {"T-0002": (294, [], False), "T-0003": (267, ["reviewer"], False), "T-0004": (224, ["qa", "judge"], True),
          "T-0007": (194, [], False), "T-0008": (154, ["qa"], False), "T-0016": (73, [], False),
          "T-0017": (50, ["reviewer"], False), "T-0020": (3, [], False)}
# done tasks without a PR: hours ago they finished
PLAIN_DONE = {"T-0001": 330, "T-0005": 215, "T-0006": 280, "T-0015": 235, "T-0021": 140}


# task -> (spec, acceptance): short fictional text, 2-4 lines of spec and 2-4 numbered testable items
DETAIL = {
    "T-0001": ("Collect what the landing page must show and who it is for.\nOutput is a one-page requirements list the coders can build from.",
               "1. Lists the three page sections (hero, pricing, contact).\n2. Each section has a one-line purpose.\n3. Names the target browsers."),
    "T-0002": ("Add a hero section to the landing page: headline, one-line subtitle and a 'Shop now' button.\nThe button links to /shop. Use the existing page layout.",
               "1. GET / renders an h1 with the headline text.\n2. The 'Shop now' button links to /shop.\n3. At 375 px width the headline does not overflow."),
    "T-0003": ("Add a pricing table with three plans (Basic 9, Plus 19, Pro 39 per month) below the hero.\nPrices come from src/pricing.json so marketing can edit them.",
               "1. The table renders one column per plan in pricing.json.\n2. Prices show two decimals and a currency symbol.\n3. Editing pricing.json changes the page without a code change."),
    "T-0004": ("Add a contact form (name, email, message) that posts to /api/contact and sends the text to the shop inbox.\nReject empty or malformed input.",
               "1. A valid post returns 200 and queues one email.\n2. A missing email returns 422 with a field error.\n3. A message over 2000 characters is rejected."),
    "T-0005": ("Remove the merged worktrees and branches of the landing page tasks.\nLeave anything unmerged alone.",
               "1. No worktree of T-0002..T-0004 remains.\n2. The branches of those tasks are deleted.\n3. No other worktree is touched."),
    "T-0006": ("Describe the checkout flow from cart to confirmation for a guest visitor.\nCovers the happy path and a declined card.",
               "1. Lists the four steps in order.\n2. States what is stored after each step.\n3. Describes the declined-card message."),
    "T-0007": ("Add a cart page listing items with quantity, line price and a total.\nQuantity can be changed or the line removed.",
               "1. /cart lists every item in the session cart.\n2. Changing a quantity updates the total.\n3. Removing the last item shows the empty-cart message."),
    "T-0008": ("Add a shipping form (name, street, city, postcode, country) shown after the cart.\nValidate the postcode per country.",
               "1. Submitting a valid address stores it on the order.\n2. A postcode of the wrong shape shows an error and stores nothing.\n3. The back link returns to the cart with the items intact."),
    "T-0009": ("Show an order summary (items, shipping address, total) before payment.\nThe page is read-only with an edit link per block.",
               "1. The summary lists the cart items and the total.\n2. The shipping address matches the stored address.\n3. Each edit link opens the right step."),
    "T-0010": ("Add the payment step: card form handled by the payment provider's hosted fields.\nOn success mark the order paid; on a decline show the provider message.",
               "1. A successful test card marks the order paid.\n2. A declined card shows the message and leaves the order unpaid.\n3. Card numbers never reach our server logs."),
    "T-0011": ("Run the whole checkout in the test shop from cart to confirmation.\nUse one successful and one declined card.",
               "1. The successful run ends on the confirmation page with an order number.\n2. The declined run keeps the cart and shows an error.\n3. Both runs are listed in the QA report."),
    "T-0012": ("Send a confirmation email when an order becomes paid.\nThe email lists items, total and the shipping address.",
               "1. Paying an order queues exactly one email.\n2. The email contains the order number and total.\n3. A failed send is retried once and then logged."),
    "T-0013": ("Decide whether the payment step PR can merge, from the reviewer and QA verdicts.\nMerge only on two passes at the current head.",
               "1. Both verdicts are PASS for the PR head.\n2. The decision is recorded with a reason."),
    "T-0014": ("Let a customer download a PDF receipt from the order page once the order is paid.\nThe PDF repeats the order summary.",
               "1. A paid order shows a download link.\n2. An unpaid order shows no link.\n3. The PDF total equals the order total."),
    "T-0015": ("List what a signed-in user can change in the account area: profile, password and notifications.\nOne short page for the coders.",
               "1. Lists the three settings pages.\n2. Gives the fields of each page.\n3. Says which changes need the current password."),
    "T-0016": ("Add a profile page where the user edits display name and email.\nA changed email must be confirmed by a link.",
               "1. Saving a new display name shows it in the page header.\n2. A new email stays pending until the link is opened.\n3. An empty display name is rejected."),
    "T-0017": ("Add a password change form: current password, new password, repeat.\nEnforce at least 12 characters.",
               "1. A wrong current password changes nothing.\n2. A new password under 12 characters is rejected.\n3. After a change, other sessions are signed out."),
    "T-0018": ("Add a switch for an old light/dark theme.\nCancelled: the new design drops themes.",
               "1. The switch toggles the stored theme.\n2. The choice survives a reload."),
    "T-0019": ("Add notification preferences: email on order update, email on offers, both default off.\nStore them per user.",
               "1. Both boxes default to off for a new user.\n2. Saving stores the choice and shows a saved notice.\n3. A reload shows the stored choice."),
    "T-0020": ("Let the user upload an avatar (PNG or JPEG up to 1 MB) shown in the header.\nResize it to 128 px square.",
               "1. A 500 KB PNG is accepted and shown in the header.\n2. A 2 MB file is rejected with a size message.\n3. A GIF is rejected with a type message."),
    "T-0021": ("Describe the minimal mobile shell: a home screen and a place for push notifications.\nOpen question: which navigation pattern the design team picks.",
               "1. Lists the screens of the shell.\n2. Marks the navigation pattern as undecided."),
    "T-0022": ("Add a stub that registers the device for push notifications and logs received payloads.\nNo real provider yet.",
               "1. Registering stores a device token in the stub.\n2. A payload sent to the stub is logged.\n3. Without permission the app shows a hint instead of failing."),
    "T-0023": ("Render a home screen with the shop name and a list of three featured items.\nData comes from a local fixture.",
               "1. The screen shows the shop name.\n2. Three featured items are listed from the fixture.\n3. An empty fixture shows a placeholder."),
    "T-0024": ("Review the shell design notes for gaps before coding goes further.\nReport open questions only.",
               "1. Every open question names who can answer it.\n2. The verdict says whether coding may start."),
}

# merged task -> (reviewer pass text, qa pass text): what the passing worker checked
CHECKED = {
    "T-0002": ("Checked the diff against the spec; the button target and the heading markup match.",
               "Ran 3 of 3 acceptance items on a staging build, including the 375 px width check."),
    "T-0003": ("Checked src/pricing.json is the only source of prices; no price is hard-coded in the template.",
               "Edited pricing.json on staging and saw the table change; currency format checked for all three plans."),
    "T-0004": ("Checked input validation in api/contact.py and that the mail queue call is made once per request.",
               "Posted a valid, an invalid-email and a 2001-character message; got 200, 422 and 422 as specified."),
    "T-0007": ("Checked the total is computed server side from line prices; cart session handling unchanged.",
               "Added, changed and removed items; total and empty-cart message were correct each time."),
    "T-0008": ("Checked the postcode patterns cover the shipped countries and the order stores the address once.",
               "Submitted valid and invalid postcodes for DE, FR and NL; only valid ones were stored; back link keeps the cart."),
    "T-0016": ("Checked the email-change flow keeps the old address until the confirmation link is used.",
               "Changed name and email on staging; the new email stayed pending until the link was opened."),
    "T-0017": ("Checked the length rule and that sessions other than the current one are revoked after a change.",
               "Tried a wrong current password, a 9-character password and a valid change; other session was signed out."),
    "T-0020": ("Checked the upload size and type limits and the resize step; temp files are removed.",
               "Uploaded a 500 KB PNG, a 2 MB PNG and a GIF; accepted, size error and type error as specified."),
}
# (task, role, attempt) -> the concrete finding behind a fail / reject
FINDINGS = {
    ("T-0003", "reviewer", 1): "src/pricing.json:14 lists Pro at 49 but the spec says 39; the template reads a copy at src/pricing.js:6 instead of the JSON.",
    ("T-0004", "qa", 1): "POST /api/contact with email 'a@' returned 200, expected 422 (api/contact.py:31 only checks for an @ sign).",
    ("T-0004", "judge", 2): "Rejected: the diff edits src/cart.js:88, which no acceptance item of this task covers. Remove it or split it into its own task.",
    ("T-0008", "qa", 1): "A postcode of 'ABCDE' for DE returned 200 and stored the address; expected a validation error (forms/shipping.py:52).",
    ("T-0017", "reviewer", 1): "auth/password.py:27 compares the new password length with 8; the spec requires at least 12.",
    ("T-0010", "reviewer", 1): "payment/charge.py:63 swallows CardDeclined and marks the order paid; expected it to stay unpaid with the provider message.",
}


# ───────────────────────────── generated growth ─────────────────────────────
# Everything above is the hand-written core the docs quote (T-0001..T-0024, G-001..G-004). world(now) adds more goals,
# teams and tasks on top of it, as a pure function of `now`; no id, title or timestamp of the core changes.

PROJECT_OF = {"G-001": "acme-web", "G-002": "acme-web", "G-003": "acme-web", "G-004": "factory", "G-005": "ledger-api",
              "G-006": "ledger-api", "G-007": "docs-site", "G-008": "docs-site", "G-009": "acme-web", "G-010": "ledger-api",
              "G-011": "docs-site", "G-012": "acme-web"}

# goal -> (project, title, status, priority, intent, criteria, merge-day window (k = days before --now's date), quota)
NEW_GOALS = [
    ("G-005", "Export transactions as CSV", "done", 2, "Let an account owner download their transactions as a CSV file.",
     "1. The export endpoint returns a CSV file.\n2. Date and account filters work.\n3. Large exports stream without timing out.", (11, 20), 7),
    ("G-006", "Rate-limit the public API", "active", 1, "Protect the public API from bursts of traffic from a single key.",
     "1. Each API key has a request limit.\n2. A limited call gets a 429 with a Retry-After header.\n3. Limits are visible as metrics.\n4. The settings are documented.", (0, 19), 18),
    ("G-007", "Move the docs to the new theme", "active", 2, "Give the documentation site the new look and navigation.",
     "1. Header, sidebar and footer use the new theme.\n2. Dark mode and the mobile menu work.\n3. No page has a broken link.", (0, 16), 16),
    ("G-008", "Add search to the docs", "done", 3, "Let readers find a documentation page by typing a few words.",
     "1. A search box is on every page.\n2. Results are ranked and highlighted.\n3. The index stays under the size budget.", (5, 17), 9),
    ("G-009", "Localize the shop in German and French", "blocked", 2, "Offer the shop in German and French. Waiting for the legal texts from the owner.",
     "1. A visitor can switch the language.\n2. Prices and dates follow the locale.\n3. Order emails are translated.", (3, 13), 9),
    ("G-010", "Add webhooks for new transactions", "proposed", 3, "Notify partner systems when a transaction is booked. Waiting for the owner to approve the scope.",
     "1. A partner can register a callback URL.\n2. Each booked transaction is posted once.\n3. Failed posts are retried.", None, 0),
    ("G-011", "Add versioned docs", "planning", 3, "Let readers switch between the docs of the last two releases.",
     "1. A version picker is in the header.\n2. Old versions stay reachable by URL.", None, 0),
    ("G-012", "Add a wishlist", "planning", 3, "Let a signed-in visitor save items for later.",
     "1. A heart button saves an item.\n2. The wishlist page lists saved items.", None, 0),
]
# team id, goal, scope, status (closed | active | parked), parked note
NEW_TEAMS = [("g-005-a", "G-005", "CSV export endpoint and filters", "closed", None),
             ("g-006-a", "G-006", "Rate limiter, headers, metrics and docs", "active", None),
             ("g-007-a", "G-007", "New theme, navigation and dark mode", "active", None),
             ("g-008-a", "G-008", "Search index, box and ranking", "closed", None),
             ("g-009-a", "G-009", "Translations, locale switcher and emails", "parked",
              "Waiting for the owner to send the German and French legal texts; resume when they arrive.")]

# per goal: (feature title, file) for the merged coder tasks, in the order they are built
FEATURES = {
    "G-005": [("Add the CSV export endpoint", "api/export.py"), ("Add a date range filter to the export", "api/export_filters.py"),
              ("Add an account filter to the export", "api/export_filters.py"), ("Let the caller pick the columns", "api/export_columns.py"),
              ("Add an optional UTF-8 byte order mark", "api/export_encoding.py"), ("Stream large exports row by row", "api/stream.py"),
              ("Write a header row", "api/export_columns.py"), ("Cap an export at 500000 rows", "api/export_limits.py"),
              ("Name the download after the account and date", "api/export_headers.py"), ("Record every export in the audit log", "api/audit.py"),
              ("Reject an empty or reversed date range", "api/export_filters.py"), ("Quote fields that contain commas", "api/csv_writer.py"),
              ("Add the export button to the account page", "web/account_page.py"), ("Time out a stuck export cleanly", "api/stream.py"),
              ("Show export progress for big files", "web/export_status.py"), ("Document the export format", "docs/export.md")],
    "G-006": [("Add a per-key request limiter", "api/limiter.py"), ("Store counters in a token bucket", "api/bucket.py"),
              ("Return 429 with a JSON error body", "api/errors.py"), ("Send the Retry-After header", "api/errors.py"),
              ("Add limit headers to every response", "api/headers.py"), ("Read the limits from a config file", "api/config.py"),
              ("Let internal callers skip the limit", "api/limiter.py"), ("Count limited calls in a metric", "api/metrics.py"),
              ("Use a sliding window for the hourly limit", "api/window.py"), ("Fall back to in-memory counters", "api/bucket.py"),
              ("Add an admin endpoint to reset a key", "api/admin.py"), ("Log the first limited call per key", "api/limiter.py"),
              ("Validate limit settings at start-up", "api/config.py"), ("Add a limits page to the developer portal", "web/portal_limits.py"), ("Warn owners at 80 percent of the limit", "api/notify.py"),
              ("Show remaining calls in the portal", "web/portal_limits.py"),
              ("Return the same limit for a rotated key", "api/keys.py"), ("Add load tests for the limiter", "tests/test_limiter.py"),
              ("Clean up expired counters", "api/bucket.py"), ("Document the limit headers", "docs/limits.md")],
    "G-007": [("Add the theme design tokens", "src/theme/tokens.css"), ("Restyle the header navigation", "src/theme/header.css"),
              ("Build the collapsible sidebar", "src/theme/sidebar.js"), ("Restyle code blocks", "src/theme/code.css"),
              ("Add dark mode", "src/theme/dark.css"), ("Add the mobile menu", "src/theme/mobile.js"),
              ("Restyle the footer", "src/theme/footer.css"), ("Add a friendly 404 page", "src/pages/404.html"),
              ("Add breadcrumbs", "src/theme/breadcrumbs.js"), ("Add a print stylesheet", "src/theme/print.css"),
              ("Add the favicon set", "src/assets/icons.html"), ("Check every internal link in CI", "ci/linkcheck.py"),
              ("Restyle admonition boxes", "src/theme/admonition.css"), ("Add the table of contents sidebar", "src/theme/toc.js"),
              ("Add keyboard focus outlines", "src/theme/focus.css"), ("Compress the theme images", "ci/images.py"),
              ("Restyle the search results page", "src/theme/results.css"), ("Add a skip-to-content link", "src/theme/a11y.html")],
    "G-008": [("Build the search index at deploy time", "tools/index.py"), ("Add the search box", "src/search/box.js"),
              ("Rank title matches above body matches", "src/search/rank.js"), ("Highlight the matched words", "src/search/highlight.js"),
              ("Open search with the slash key", "src/search/keys.js"), ("Show a message when nothing matches", "src/search/results.js"),
              ("Remember the last three queries", "src/search/history.js"), ("Tolerate one typo per word", "src/search/fuzzy.js"),
              ("Filter results by section", "src/search/filters.js"), ("Keep the index under 300 KB", "tools/index.py"),
              ("Skip draft pages in the index", "tools/index.py"), ("Add search to the mobile menu", "src/search/box.js"),
              ("Debounce typing in the search box", "src/search/box.js"), ("Document how the index is built", "docs/search.md")],
    "G-009": [("Extract the shop strings into message files", "i18n/messages.js"), ("Add the language switcher", "web/lang_switch.js"),
              ("Format prices per locale", "i18n/currency.js"), ("Format dates per locale", "i18n/dates.js"),
              ("Add the German translations", "i18n/de.json"), ("Add the French translations", "i18n/fr.json"),
              ("Put the locale in the URL", "web/routes.js"), ("Fall back to English for a missing string", "i18n/messages.js"),
              ("Handle plural forms", "i18n/plural.js"), ("Translate the cart page", "web/cart_page.js"),
              ("Translate the contact form errors", "web/contact_form.js"), ("Remember the chosen language", "web/lang_switch.js")],
}
# (goal, role, title, status, mode, hours ago, file) for work in flight
INFLIGHT = [
    ("G-006", "coder", "Add per-route limit overrides", "in_progress", "live", 7, "api/limits.py"),
    ("G-006", "coder", "Expose limiter metrics per route", "in_progress", "live", 4, "api/metrics.py"),
    ("G-006", "coder", "Tune the burst allowance", "in_review", "review", 30, "api/burst.py"),
    ("G-006", "reviewer", "Review the burst allowance PR", "in_progress", "live", 3, None),
    ("G-006", "qa", "Verify the limiter under load", "in_progress", "live", 5, None),
    ("G-006", "judge", "Decide merge for the burst allowance PR", "todo", "todo", 10, None),
    ("G-006", "coder", "Add limit headers to error pages", "todo", "todo", 9, "api/errors.py"),
    ("G-006", "coder", "Document the limiter settings", "todo", "todo", 8, "docs/limits.md"),
    ("G-007", "coder", "Add the language picker to the header", "in_progress", "live", 6, "src/theme/header.js"),
    ("G-007", "pm", "Specify redirects for moved pages", "in_progress", "live", 9, None),
    ("G-007", "judge", "Decide merge for the footer PR", "in_progress", "live", 2, None),
    ("G-007", "coder", "Add a changelog page", "todo", "todo", 20, "src/pages/changelog.md"),
    ("G-007", "coder", "Add an RSS feed", "cancelled", "cancelled", 40, "tools/feed.py"),
    ("G-007", "reviewer", "Review the redirect map", "todo", "todo", 7, None),
    ("G-009", "coder", "Add Spanish placeholder strings", "blocked", "lost", 60, "i18n/es.json"),
    ("G-009", "coder", "Localize the order emails", "todo", "todo", 50, "i18n/emails.js"),
    ("G-009", "qa", "Verify the German checkout copy", "todo", "todo", 48, None),
]
REVIEW_PROBLEMS = ["the new branch has no test for an empty input", "an error is swallowed instead of being returned to the caller",
                   "a constant is copied from another module instead of reused", "the loop reads the file again for every item"]
QA_PROBLEMS = ["returned the wrong value for the edge case in acceptance item 2", "accepted an empty value that the spec says to reject",
               "did not handle the second call in a row the same way as the first"]
TOTAL_MERGES = 64      # target for all merged PRs in the 21-day window, the 8 hand-written ones included


def lifecycle(rejects, blocked):
    """Hours-offset steps (offset from the task's first event, kind) of a merged task, and the offset of the merge."""
    steps = [(0, "add"), (0.3, "spawn"), (0.3, "progress"), (2.8, "review")]
    cur = 2.8
    for r in rejects:
        if r == "judge":
            steps += [(cur + 1, "reviewer-pass"), (cur + 1.5, "qa-pass"), (cur + 2, "judge-reject")]
            cur += 2
        else:
            steps += [(cur + 1, f"{r}-fail")]
            cur += 1
        steps += [(cur + 0.5, "progress"), (cur + 2.5, "review")]
        cur += 2.5
    steps += [(cur + 1, "reviewer-pass"), (cur + 2, "qa-pass")]
    cur += 2
    if blocked:
        steps += [(cur + 0.3, "blocked")]
        cur += 0.3
    steps += [(cur + 0.5, "merge")]
    return steps, cur + 0.5


def merge_days(now, existing):
    """Merges per UTC calendar day for the 21 days ending at --now's date (k = 0 is today, a partial day).

    THE GENERATING RULE of the demo throughput curve. Each day gets a weight = trend x weekday x dip x jitter:
      trend   rises linearly from 2.4 (oldest day) to 5.6 (today), a mild upward trend;
      weekday 1.0 Monday-Friday, 0.3 Saturday and Sunday, so weekends are quiet;
      dip     0.2 on one weekday about 13 days back that holds no hand-written merge: one visible dip;
      jitter  a seeded +-15 %, so the curve is not a clean sawtooth.
    The weights are scaled to TOTAL_MERGES and rounded (at least 1 a day, at least the hand-written merges of that
    day); then any day equal to its neighbour is bumped by one until no two consecutive days match.
    `existing` = hours-before-now of the hand-written merges. Returns (per-day totals, per-day new merges, dip day).
    """
    n = 21
    today = now.date()
    ex = [0] * n
    for h in existing:
        k = (today - (now - dt.timedelta(hours=h)).date()).days
        if 0 <= k < n:
            ex[k] += 1
    rng = random.Random(SEED)
    w = []
    for k in range(n):
        weekend = (today - dt.timedelta(days=k)).weekday() >= 5
        w.append((2.4 + 3.2 * (n - 1 - k) / (n - 1)) * (0.3 if weekend else 1.0) * rng.uniform(0.85, 1.15))
    w[0] *= min(1.0, max(now.hour, 1) / 16)
    dip = next(k for k in (13, 14, 12, 15, 16, 11, 17, 18) if (today - dt.timedelta(days=k)).weekday() < 5 and ex[k] == 0)
    w[dip] *= 0.2
    scale = TOTAL_MERGES / sum(w)
    c = [max(round(x * scale), 1, ex[k]) for k, x in enumerate(w)]
    if now.hour < 2:
        c[0] = ex[0]       # no room for a merge before the first hour of the day
    for _ in range(100):
        bumped = False
        for k in range(n - 1, 0, -1):
            if c[k] == c[k - 1]:
                j = k if k - 1 == dip or (k == 1 and now.hour < 2) else k - 1
                c[j] += 1
                bumped = True
        if not bumped:
            break
    return c, [c[k] - ex[k] for k in range(n)], dip


class World:
    """The generated growth, for one `now`: all extra rows plus the hand-written core, merged."""


@functools.lru_cache(maxsize=8)
def world(now):
    W = World()
    existing = [v[0] for v in MERGED.values()]
    W.per_day, extra, W.dip = merge_days(now, existing)
    rng = random.Random(SEED + 1)
    midnight0 = dt.datetime.combine(now.date(), dt.time.min, tzinfo=dt.timezone.utc)
    merges = []                                   # (hours before now, day k)
    for k, cnt in enumerate(extra):
        for j in range(cnt):
            if k == 0:
                lo, hi = max(0.0, now.hour - 6.0), max(now.hour - 0.3, 0.5)
                clock = lo + (hi - lo) * (j + rng.uniform(0.1, 0.9)) / cnt
            else:
                clock = 9 + 7 * (j + rng.uniform(0.1, 0.9)) / cnt
            merges.append((round((now - midnight0).total_seconds() / 3600 + k * 24 - clock, 3), k))
    merges.sort(reverse=True)                     # oldest first
    # goal per merge: among the goals whose window holds the day, the one furthest below its quota
    used = {g[0]: 0 for g in NEW_GOALS}
    quota = {g[0]: g[7] for g in NEW_GOALS}
    window = {g[0]: g[6] for g in NEW_GOALS}
    jobs = []
    for i, (mh, k) in enumerate(merges):
        elig = [g for g in used if window[g] and window[g][0] <= k <= window[g][1]]
        g = min(elig, key=lambda x: (used[x] / quota[x], x))
        used[g] += 1
        title, path = FEATURES[g][(used[g] - 1) % len(FEATURES[g])]
        if used[g] > len(FEATURES[g]):
            title += " (follow-up)"
        rejects = (["reviewer"] if i % 5 == 1 else []) + (["qa"] if i % 7 == 3 else []) + (["judge"] if i % 17 == 5 else [])
        jobs.append({"goal": g, "title": title, "path": path, "mh": mh, "rejects": rejects, "blocked": i % 11 == 4, "role": "coder"})
    # per goal: first and last merge, to place the goal, its pm task and the cleaner
    first = {g: max((j["mh"] for j in jobs if j["goal"] == g), default=None) for g in used}
    last = {g: min((j["mh"] for j in jobs if j["goal"] == g), default=None) for g in used}
    items = []                                    # every new task before ids: (created hours ago, key order, dict)
    for j in jobs:
        _, end = lifecycle(j["rejects"], j["blocked"])
        j.update(status="done", created=j["mh"] + end, kind="merged")
        items.append(j)
    goals, teams = [], []
    W.goal_created = {}
    for gid, title, status, prio, intent, crit, win, q in NEW_GOALS:
        if first[gid] is None:
            cd, upd = 1.5, 9
        else:
            cd = (first[gid] + 45) / 24
            upd = {"done": last[gid] - 6, "blocked": 70}.get(status, 2 + len(goals) % 4)
        W.goal_created[gid] = cd
        goals.append((gid, PROJECT_OF[gid], title, status, prio, intent, crit, cd, upd))
        if first[gid] is not None:
            items.append({"goal": gid, "role": "pm", "title": "Specify " + title[0].lower() + title[1:], "status": "done", "kind": "plain",
                          "created": first[gid] + 45 - 12 + 6, "done": first[gid] + 45 - 12, "path": None})
        if status == "done":
            items.append({"goal": gid, "role": "cleaner", "title": f"Remove merged {PROJECT_OF[gid]} worktrees for {gid}", "status": "done",
                          "kind": "plain", "created": last[gid] - 4 + 3, "done": last[gid] - 4, "path": None})
    for tid_, gid, scope, st, note in NEW_TEAMS:
        cd = W.goal_created[gid] - 0.4
        teams.append((tid_, gid, "lead-" + tid_, scope, "closed" if st == "closed" else "active", cd))
    for goal, role, title, status, mode, hrs, path in INFLIGHT:
        items.append({"goal": goal, "role": role, "title": title, "status": status, "kind": mode, "hrs": hrs, "created": hrs + 12, "path": path})
    items.sort(key=lambda d: (-d["created"], d["goal"], d["title"]))
    for n, it in enumerate(items):
        it["id"] = f"T-{31 + n:04d}"
    pm_of = {it["goal"]: it["id"] for it in items if it["role"] == "pm" and it["kind"] == "plain"}
    review_of = {it["goal"]: it["id"] for it in items if it["kind"] == "review"}
    # a todo coder or qa task waits on the unfinished coder task of its goal, so it is not "ready" with nobody working it
    busy_of = {}
    for it in items:
        if it["kind"] in ("live", "lost") and it["role"] == "coder":
            busy_of.setdefault(it["goal"], it["id"])
    team_of = {t[1]: t[0] for t in teams}
    tasks, merged, plain, detail, checked, findings = [], {}, {}, {}, {}, {}
    for it in items:
        tid, g, role = it["id"], it["goal"], it["role"]
        who = f"{role}-{tid.lower()}"
        pr = 200 + int(tid[2:]) if it["kind"] in ("merged", "review") else None
        deps = [pm_of[g]] if g in pm_of and not (role == "pm") else []
        if it["title"].startswith("Review the burst"):
            deps = [review_of[g]]
        if it["title"].startswith("Decide merge for the burst"):
            deps = [review_of[g]]
        elif it["kind"] == "todo" and role != "judge" and not it["title"].startswith("Review the burst") and g in busy_of:
            deps = deps + [busy_of[g]]
        if it["kind"] == "merged":
            att = 1 + len(it["rejects"])
            merged[tid] = (it["mh"], it["rejects"], it["blocked"])
            checked[tid] = (f"Checked the diff in {it['path']} against the spec; naming and error handling match the surrounding code.",
                            "Ran every acceptance item on staging; the normal case and the bad-input case behaved as specified.")
            for a, r in enumerate(it["rejects"], 1):
                line = 12 + (int(tid[2:]) * 7 + a * 13) % 150
                if r == "reviewer":
                    findings[(tid, r, a)] = f"{it['path']}:{line} {REVIEW_PROBLEMS[int(tid[2:]) % len(REVIEW_PROBLEMS)]}."
                elif r == "qa":
                    findings[(tid, r, a)] = f"Item 2 fails: {it['path']}:{line} {QA_PROBLEMS[int(tid[2:]) % len(QA_PROBLEMS)]}."
                else:
                    findings[(tid, r, a)] = (f"Rejected: the diff edits {it['path'].rsplit('/', 1)[0]}/shared.py:{line}, which no acceptance item "
                                             "of this task covers. Remove it or split it into its own task.")
            status, attempts, assignee = "done", att, who
        elif it["kind"] == "plain":
            plain[tid] = it["done"]
            status, attempts, assignee = "done", 1, who
        elif it["kind"] == "live":
            status, attempts, assignee = it["status"], 1, who
        elif it["kind"] == "review":
            status, attempts, assignee = "in_review", 2, who
        elif it["kind"] == "lost":
            status, attempts, assignee = "blocked", 1, who
        else:                                      # todo, cancelled
            status, attempts, assignee = it["status"], 0, None
        path = it["path"]
        t0 = it["title"]
        if role == "pm":
            sp = f"Collect what '{t0.split(' ', 1)[1]}' needs and who it is for.\nOutput is a one-page list the coders can build from."
            ac = "1. Lists every page or endpoint that changes.\n2. Each item has a one-line purpose.\n3. Names what is out of scope."
        elif role in ("qa", "reviewer", "judge", "cleaner"):
            sp = f"{t0}.\nReport only what blocks the next step."
            ac = "1. Every finding names a file and line.\n2. The verdict says whether work may continue."
        else:
            sp = f"{t0}. Work in {path}.\nKeep the change small and add a test next to it."
            ac = (f"1. The change in {path} gives the documented result for a normal case.\n"
                  "2. A bad or empty input is rejected with a clear message.\n3. A test covers both cases.")
        detail[tid] = (sp, ac)
        tasks.append((tid, g, team_of.get(g), role, t0, status, deps, pr, attempts, assignee))
        it["tuple"] = tasks[-1]
    W.items, W.tasks, W.merged, W.plain, W.detail, W.checked, W.findings = items, tasks, merged, plain, detail, checked, findings
    W.goals, W.teams = goals, teams
    W.goal_upd = {g[0]: g[8] for g in GOALS + goals}
    W.pm_of = pm_of
    # live agents: boss, director, 3 core leads, 3 core workers, then the generated live leads and workers
    W.n_live = 8 + sum(1 for t in NEW_TEAMS if t[3] == "active") + sum(1 for it in items if it["kind"] == "live")
    return W


def goal_of(tid):
    return next(t[1] for t in TASKS if t[0] == tid)


def build(now):
    """Every row of the demo as lists of tuples, keyed by table. Pure function of `now`."""
    H = lambda h: now - dt.timedelta(hours=h)  # noqa: E731
    D = lambda d: now - dt.timedelta(days=d)   # noqa: E731
    W = world(now)
    all_tasks = TASKS + W.tasks
    all_teams = TEAMS + W.teams
    merged_of = {**MERGED, **W.merged}
    plain_done = {**PLAIN_DONE, **W.plain}
    checked_of = {**CHECKED, **W.checked}
    findings_of = {**FINDINGS, **W.findings}
    goal_upd = W.goal_upd
    parked = {t[0]: t[4] for t in NEW_TEAMS if t[3] == "parked"}
    lead_of = {t[0]: t[2] for t in all_teams}
    n_live = W.n_live
    task = {t[0]: t for t in all_tasks}
    pr_url = {t[0]: REPO % (PROJECT_OF[t[1]], t[7]) for t in all_tasks if t[7]}
    events, verdicts, agents = [], [], {}
    seq = [0]

    def ev(ts, actor, entity, eid, action, detail=None):
        seq[0] += 1
        events.append((ts, seq[0], actor, entity, eid, action, detail))

    def verdict(ts, tid, role, v, summary, attempt):
        # one head SHA per (task, attempt): reviewer, QA and judge of an attempt all judge the same head. No pure main
        # merge after a PASS is modelled, so every verdict is at the head being judged (nothing to carry over).
        verdicts.append((ts, len(verdicts), tid, pr_url.get(tid), sha(tid, attempt), role, v, summary, f"{role}-{tid.lower()}"))

    def agent(name, role, goal, team, tid, status, started, ended=None, summary=None, session=None, queued=None):
        agents[name] = (name, role, goal, team, tid, status, MODEL[role], iso(started) if started else None,
                        iso(ended) if ended else None, summary, session, iso(queued) if queued else None)

    # persistent agents
    agent("boss", "boss", None, None, None, "live", D(14.2))
    agent("director", "director", None, None, None, "live", D(14.1))
    for team, goal, lead, scope, status, days in all_teams:
        if team in parked:
            agent(lead, "lead", goal, team, None, "done", D(days - 0.1), H(goal_upd[goal] - 2), "parked: " + parked[team])
        elif status == "active":
            agent(lead, "lead", goal, team, None, "live", D(days - 0.1))
        else:
            agent(lead, "lead", goal, team, None, "done", D(days - 0.1), H(goal_upd[goal] - 2), f"Goal {goal} finished; team closed.")
        ev(D(days), "director", "team", team, "new", scope)
        ev(D(days), "director", "agent", lead, "spawn", f"role=lead team={team}")
        if team in parked:
            ev(H(goal_upd[goal] - 2), lead, "team", team, "park", parked[team])
        elif status != "active":
            ev(H(goal_upd[goal]), "director", "team", team, "close", "goal done")
            ev(H(goal_upd[goal] - 2), "director", "agent", lead, "done")
    ev(D(14.2), "owner", "agent", "boss", "spawn", "role=boss")
    ev(D(14.1), "boss", "agent", "director", "spawn", "role=director")
    ev(D(13), "director", "config", "max_live_agents", "set", str(n_live))
    ev(D(5), "director", "config", "max_total_claude", "set", str(n_live + 6))

    # task lifecycles
    for tid, goal, team, role, title, status, deps, prn, attempts, who in all_tasks:
        lead = lead_of.get(team)
        if tid in merged_of:
            mh, rejects, blocked = merged_of[tid]
            steps, end = lifecycle(rejects, blocked)
            base = mh + end                        # hours before now of offset 0
            at = lambda off: H(base - off)         # noqa: E731
            n = 0                                 # attempt number: each in_review opens a new head
            coder = who
            for off, kind in steps:
                ts = at(off)
                if kind == "add":
                    ev(ts, lead, "task", tid, "add", title)
                elif kind == "spawn":
                    ev(ts, lead, "agent", coder, "spawn", f"role=coder task={tid}")
                    agent(coder, "coder", goal, team, tid, "done", ts)
                elif kind == "progress":
                    ev(ts, coder, "task", tid, "update", "status=in_progress")
                elif kind == "review":
                    n += 1
                    ev(ts, coder, "task", tid, "update", f"status=in_review pr={pr_url[tid]}")
                    a = agents[coder]
                    agents[coder] = a[:8] + (iso(ts + dt.timedelta(minutes=10)), "Opened PR %d." % task[tid][7]) + a[10:]
                    ev(ts + dt.timedelta(minutes=10), coder, "agent", coder, "done")
                elif kind.endswith("-fail"):
                    r = kind.split("-")[0]
                    verdict(ts, tid, r, "fail", "Changes needed: " + findings_of[(tid, r, n)], n)
                elif kind == "reviewer-pass":
                    verdict(ts, tid, "reviewer", "pass", checked_of[tid][0], n)
                elif kind == "qa-pass":
                    verdict(ts, tid, "qa", "pass", checked_of[tid][1], n)
                elif kind == "judge-reject":
                    verdict(ts, tid, "judge", "reject", findings_of[(tid, "judge", n)], n)
                    ev(ts, f"judge-{tid.lower()}", "task", tid, "judge-reject", "unrelated file touched")
                elif kind == "blocked":
                    ev(ts, f"judge-{tid.lower()}", "task", tid, "judge-blocked", "checks not green: build pending")
                elif kind == "merge":
                    verdict(ts, tid, "judge", "merge", f"Reviewer and QA PASS at {sha(tid, n)[:8]}; checks green; merging.", n)
                    ev(ts, f"judge-{tid.lower()}", "task", tid, "merged", "@" + sha(tid, n)[:8])
                    ev(ts, f"judge-{tid.lower()}", "task", tid, "update", "status=done")
            if tid == "T-0020":                   # the recent merge also has finished review workers (ended < 24 h ago)
                for r, off in (("reviewer", 5.2), ("qa", 4.2), ("judge", 3.6)):
                    nm = f"{r}-t-0020"
                    agent(nm, r, goal, team, tid, "done", at(off - 0.4), at(off - 0.1), f"{r} verdict recorded.")
                    ev(at(off - 0.4), lead, "agent", nm, "spawn", f"role={r} task={tid}")
                    ev(at(off - 0.1), nm, "agent", nm, "done")
        elif status == "done":
            h = plain_done[tid]
            ev(H(h + 6), lead, "task", tid, "add", title)
            ev(H(h), who, "task", tid, "update", "status=done")
            if role == "cleaner":
                agent(who, "cleaner", goal, team, tid, "exited", H(h + 1), H(h), "Removed merged worktrees.")
                ev(H(h + 1), lead, "agent", who, "spawn", f"role=cleaner task={tid}")
    # open work
    ev(H(30), "lead-g-002-a", "task", "T-0010", "add", task["T-0010"][4])
    ev(H(29), "lead-g-002-a", "agent", "coder-t-0010", "spawn", "role=coder task=T-0010")
    agent("coder-t-0010", "coder", "G-002", "g-002-a", "T-0010", "done", H(29), H(20), "Opened PR 107.")
    ev(H(28), "coder-t-0010", "task", "T-0010", "update", "status=in_progress")
    ev(H(21), "coder-t-0010", "task", "T-0010", "update", f"status=in_review pr={pr_url['T-0010']}")
    verdict(H(18), "T-0010", "reviewer", "fail", "Changes needed: " + FINDINGS[("T-0010", "reviewer", 1)], 1)
    ev(H(17), "coder-t-0010", "task", "T-0010", "update", "status=in_progress")
    ev(H(9), "coder-t-0010", "task", "T-0010", "update", f"status=in_review pr={pr_url['T-0010']}")
    verdict(H(7), "T-0010", "reviewer", "pass", "Checked the decline path in payment/charge.py: an unpaid order keeps its cart and shows the provider message; hosted fields keep card data out of our logs.", 2)
    for tid, hrs, nm, role in (("T-0011", 6, "qa-t-0011", "qa"), ("T-0012", 5, "coder-t-0012", "coder"),
                               ("T-0019", 8, "coder-t-0019", "coder")):
        g, tm = task[tid][1], task[tid][2]
        lead = next(t[2] for t in TEAMS if t[0] == tm)
        ev(H(hrs + 12), lead, "task", tid, "add", task[tid][4])
        ev(H(hrs), lead, "agent", nm, "spawn", f"role={role} task={tid}")
        ev(H(hrs), nm, "task", tid, "update", "status=in_progress")
        agent(nm, role, g, tm, tid, "live", H(hrs))
    for tid, hrs in (("T-0009", 40), ("T-0013", 12), ("T-0014", 10), ("T-0018", 60), ("T-0023", 30), ("T-0024", 30)):
        lead = next(t[2] for t in TEAMS if t[0] == task[tid][2])
        ev(H(hrs), lead, "task", tid, "add", task[tid][4])
    ev(H(55), "lead-g-003-a", "task", "T-0018", "update", "status=cancelled")
    # blocked task with a lost agent
    ev(H(48), "lead-g-004-a", "task", "T-0022", "add", task["T-0022"][4])
    ev(H(47), "lead-g-004-a", "agent", "coder-t-0022", "spawn", "role=coder task=T-0022")
    ev(H(46), "coder-t-0022", "task", "T-0022", "update", "status=in_progress")
    ev(H(30), "director", "agent", "coder-t-0022", "lost", "pane closed")
    ev(H(29), "lead-g-004-a", "task", "T-0022", "update", "status=blocked")
    agent("coder-t-0022", "coder", "G-004", "g-004-a", "T-0022", "lost", H(47), H(30), "Pane closed before a PR was opened.")
    # generated work in flight (todo, cancelled, live worker, lost worker, PR in review)
    for it in W.items:
        kind = it["kind"]
        if kind in ("merged", "plain"):
            continue
        tid, g, role = it["id"], it["goal"], it["role"]
        tm = task[tid][2]
        lead, nm, hrs = lead_of[tm], f"{role}-{tid.lower()}", it.get("hrs", 0)
        ev(H(it["created"]), lead, "task", tid, "add", it["title"])
        if kind == "cancelled":
            ev(H(hrs), lead, "task", tid, "update", "status=cancelled")
        elif kind == "live":
            ev(H(hrs), lead, "agent", nm, "spawn", f"role={role} task={tid}")
            ev(H(hrs), nm, "task", tid, "update", "status=in_progress")
            agent(nm, role, g, tm, tid, "live", H(hrs))
        elif kind == "lost":
            ev(H(hrs), lead, "agent", nm, "spawn", f"role={role} task={tid}")
            ev(H(hrs - 1), nm, "task", tid, "update", "status=in_progress")
            ev(H(hrs - 14), "director", "agent", nm, "lost", "pane closed")
            ev(H(hrs - 15), lead, "task", tid, "update", "status=blocked")
            agent(nm, role, g, tm, tid, "lost", H(hrs), H(hrs - 14), "Pane closed before a PR was opened.")
        elif kind == "review":
            ev(H(hrs - 1), lead, "agent", nm, "spawn", f"role={role} task={tid}")
            agent(nm, role, g, tm, tid, "done", H(hrs - 1), H(hrs - 10), f"Opened PR {task[tid][7]}.")
            ev(H(hrs - 2), nm, "task", tid, "update", "status=in_progress")
            ev(H(hrs - 9), nm, "task", tid, "update", f"status=in_review pr={pr_url[tid]}")
            verdict(H(hrs - 12), tid, "reviewer", "fail", f"Changes needed: {it['path']}:41 {REVIEW_PROBLEMS[1]}.", 1)
            ev(H(hrs - 13), nm, "task", tid, "update", "status=in_progress")
            ev(H(hrs - 21), nm, "task", tid, "update", f"status=in_review pr={pr_url[tid]}")
            verdict(H(hrs - 23), tid, "reviewer", "pass", f"Checked the retry in {it['path']}: the earlier finding is fixed and the diff stays inside the spec.", 2)
    # on-demand and queued agents
    agent("staff-engineer", "staff-engineer", None, None, None, "stopped", H(16), H(14), "Answered one architecture question.")
    ev(H(16), "lead-g-002-a", "agent", "staff-engineer", "spawn", "role=staff-engineer")
    ev(H(14), "lead-g-002-a", "agent", "staff-engineer", "stopped")
    agent("pm-t-0030", "pm", "G-004", "g-004-a", None, "queued", None, None, None, None, H(2))
    agent("qa-t-0010", "qa", "G-002", "g-002-a", "T-0010", "queued", None, None, None, None, H(1))
    ev(H(1), "director", "agent", "qa-t-0010", "queued", f"agents live {n_live}/{n_live}: spawn gate closed")
    ev(H(2), "director", "agent", "pm-t-0030", "queued", f"agents live {n_live}/{n_live}: spawn gate closed")
    # evaluations
    evals = [("g-001-a", 5, "Clean delivery, one reviewer rejection.", D(8)), ("g-002-a", 4, "Good pace, two QA rejections.", D(2)),
             ("g-003-a", 4, "Steady, one reviewer rejection.", D(1)),
             ("g-005-a", 5, "Fast delivery of a small goal, a few reviewer rejections.", D(10)),
             ("g-008-a", 4, "Good pace, search shipped with two QA rejections.", D(4)),
             ("g-006-a", 4, "Many small merges, steady quality.", D(1.5)),
             ("g-007-a", 4, "Large theme goal moving along.", D(1.2))]
    for t, s, notes, ts in evals:
        ev(ts, "director", "team", t, "eval", str(s))

    goals_ev = [("G-001", D(14.5), "boss", "add"), ("G-002", D(12), "boss", "add"), ("G-003", D(10), "boss", "add"),
                ("G-004", D(6), "boss", "add")]
    goals_ev += [(g[0], D(g[7]), "boss", "add") for g in W.goals]
    for gid, ts, who, act in goals_ev:
        ev(ts, who, "goal", gid, act, next(g[2] for g in GOALS + W.goals if g[0] == gid))
    return events, verdicts, agents, evals, task, pr_url


def messages(now):
    """~34 bland messages (ts, sender, recipient, kind, subject, body, goal, task, read). Sorted by time."""
    H = lambda h: now - dt.timedelta(hours=h)  # noqa: E731
    out = []
    W = world(now)

    def m(h, s, r, k, subj, body, goal=None, tid=None, read=True):
        out.append((H(h), s, r, k, subj, body, goal, tid, read))
    for gid, h, title in (("G-001", 346, "landing page"), ("G-002", 288, "checkout flow"), ("G-003", 240, "account settings"),
                          ("G-004", 144, "mobile app shell")):
        m(h, "owner", "boss", "request", f"New goal: {title}", f"Please plan the {title}.", gid)
        m(h - 1, "boss", "director", "request", f"Staff goal {gid}", f"Goal {gid} is approved. Please staff a team.", gid)
    m(300, "director", "boss", "report", "Team g-001-a staffed", "Lead lead-g-001-a is running.", "G-001")
    for tid, goal, team, role, title, status, deps, prn, attempts, who in TASKS + W.tasks:
        lead = f"lead-{team}"
        if role == "coder" and who and tid in {**MERGED, **W.merged}:
            mh = {**MERGED, **W.merged}[tid][0]
            m(mh + 8, lead, who, "request", f"Kickoff {tid}", f"Implement: {title}. Open a PR when done.", goal, tid)
            m(mh + 4, who, lead, "report", f"Done {tid}", f"PR {prn} is open and the tests pass.", goal, tid)
    m(21, "lead-g-002-a", "coder-t-0010", "request", "Kickoff T-0010", "Implement the payment step.", "G-002", "T-0010")
    m(20, "coder-t-0010", "lead-g-002-a", "report", "Done T-0010", "PR 107 is open and the tests pass.", "G-002", "T-0010")
    m(7, "reviewer-t-0010", "lead-g-002-a", "report", "Review T-0010", "Reviewer verdict recorded: pass.", "G-002", "T-0010")
    m(28, "lead-g-004-a", "director", "escalation", "Task T-0022 is blocked",
      "The agent for T-0022 was lost and the design decision is still open. Need guidance.", "G-004", "T-0022", read=False)
    m(26, "director", "boss", "escalation", "Goal G-004 needs an owner decision",
      "The mobile shell needs a design choice before work can continue.", "G-004", None, read=False)
    m(24, "boss", "director", "decision", "Keep G-004 on hold", "Hold the mobile shell goal until the design is chosen.", "G-004")
    m(200, "director", "boss", "report", "Goal G-001 done", "All three criteria are met.", "G-001")
    m(4, "lead-g-003-a", "director", "report", "Status G-003", "Two tasks merged, one in progress.", "G-003")
    m(2, "lead-g-002-a", "director", "report", "Status G-002", "Cart and shipping merged, payment in review.", "G-002")
    m(1, "boss", "owner", "report", "Daily summary", "Four goals: one done, two active, one blocked.")
    for g in W.goals:
        gid, cd, upd, title = g[0], g[7], g[8], g[2]
        m(cd * 24 + 1, "owner", "boss", "request", f"New goal: {title}", f"Please plan: {title}.", gid)
        if g[3] not in ("proposed", "planning"):
            m(cd * 24, "boss", "director", "request", f"Staff goal {gid}", f"Goal {gid} is approved. Please staff a team.", gid)
            m(cd * 24 - 3, "director", "boss", "report", f"Team g-{gid[2:]}-a staffed", f"Lead lead-g-{gid[2:]}-a is running.", gid)
        if g[3] == "done":
            m(upd, "director", "boss", "report", f"Goal {gid} done", "All criteria are met.", gid)
        elif g[3] == "active":
            m(3 + int(gid[2:]) % 3, f"lead-g-{gid[2:]}-a", "director", "report", f"Status {gid}", "Several tasks merged, the rest in progress.", gid)
    m(70, "lead-g-009-a", "director", "escalation", "Goal G-009 is waiting for the legal texts",
      "German and French legal texts are not here yet; the team is parked until they arrive.", "G-009", None, read=False)
    m(68, "director", "boss", "escalation", "Goal G-009 needs input from the owner",
      "The translations of the terms and privacy pages need the owner's texts.", "G-009", None, read=False)
    m(66, "boss", "owner", "escalation", "Please send the German and French legal texts",
      "Goal G-009 cannot finish the shop translations without them. The team is parked until you reply.", "G-009", None, read=False)
    m(9, "boss", "owner", "request", "Approve goal G-010 (webhooks)?",
      "I drafted the scope for transaction webhooks. Reply yes to start a team, or tell me what to change.", "G-010", None, read=False)
    out.sort(key=lambda r: (r[0], r[1], r[4]))
    return out


# ───────────────────────────── DB + logs ─────────────────────────────

def fill_db(db, now):
    events, verdicts, agents, evals, task, pr_url = build(now)
    msgs = messages(now)
    W = world(now)
    all_tasks, all_teams = TASKS + W.tasks, TEAMS + W.teams
    detail_of = {**DETAIL, **W.detail}
    H = lambda h: now - dt.timedelta(hours=h)  # noqa: E731
    D = lambda d: now - dt.timedelta(days=d)   # noqa: E731
    sessions = session_plan(now, agents)
    con = sqlite3.connect(f"{Path(db).as_uri()}?mode=rw", uri=True)
    try:
        with con:
            for pid, name, gh in PROJECTS:
                con.execute("INSERT INTO projects(id,name,repo_path,gh_repo,base_branch,auto_merge,notes,created) VALUES(?,?,?,?,?,?,?,?)",
                            (pid, name, f"/srv/demo/{name}", gh, "main", 1, "Fictional demo project.", iso(D(15))))
            for gid, pid, title, status, prio, intent, crit, cd, uh in GOALS + W.goals:
                con.execute("INSERT INTO goals(id,project_id,title,intent,success_criteria,status,priority,requested_by,created,updated) "
                            "VALUES(?,?,?,?,?,?,?,?,?,?)", (gid, pid, title, intent, crit, status, prio, "owner", iso(D(cd)), iso(H(uh))))
                if status not in ("proposed", "planning"):   # a goal still being scoped or waiting for the owner has no team and no plan
                    con.execute("INSERT INTO plans(goal_id,version,body,author,created) VALUES(?,?,?,?,?)",
                                (gid, 1, "1. Specify\n2. Build\n3. Verify", f"lead-{ [t[0] for t in all_teams if t[1] == gid][0] }", iso(D(cd - 0.2))))
            for tid_, gid, lead, scope, status, days in all_teams:
                con.execute("INSERT INTO teams(id,goal_id,scope,lead_agent,status,created) VALUES(?,?,?,?,?,?)",
                            (tid_, gid, scope, lead, status, iso(D(days))))
            for tid_, gid, scope, st, note in NEW_TEAMS:
                if st == "parked":
                    con.execute("UPDATE teams SET parked_at=?, resume_note=? WHERE id=?", (iso(H(W.goal_upd[gid] - 2)), note, tid_))
            first = {}
            last = {}
            for ts, _, _, entity, eid, *_ in events:
                if entity == "task":
                    first[eid] = min(first.get(eid, ts), ts)
                    last[eid] = max(last.get(eid, ts), ts)
            for tid, goal, team, role, title, status, deps, prn, attempts, who in all_tasks:
                con.execute("INSERT INTO tasks(id,goal_id,team_id,title,spec,acceptance,role,status,assignee,depends_on,branch,pr_url,"
                            "attempts,created,updated) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
                            (tid, goal, team, title, detail_of[tid][0], detail_of[tid][1], role, status, who, json.dumps(deps),
                             f"factory/{tid.lower()}", pr_url.get(tid), attempts, iso(first[tid]), iso(last[tid])))
            for name, role, goal, team, tid, status, model, started, ended, summary, session, queued in agents.values():
                con.execute("INSERT INTO agents(name,role,goal_id,team_id,task_id,status,model,session_id,summary,queued_at,started,ended) "
                            "VALUES(?,?,?,?,?,?,?,?,?,?,?,?)",
                            (name, role, goal, team, tid, status, model, sessions.get(name, {}).get("sid"), summary, queued, started, ended))
            for ts, _, tid, pr, head, role, v, summary, author in sorted(verdicts):
                con.execute("INSERT INTO verdicts(task_id,pr_url,head_sha,role,verdict,summary,author,created) VALUES(?,?,?,?,?,?,?,?)",
                            (tid, pr, head, role, v, summary, author, iso(ts)))
            for ts, _, actor, entity, eid, action, detail in sorted(events, key=lambda e: (e[0], e[1])):
                con.execute("INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,?,?,?,?)",
                            (iso(ts), actor, entity, eid, action, detail))
            for ts, s, r, k, subj, body, goal, tid, read in msgs:
                con.execute("INSERT INTO messages(sender,recipient,kind,subject,body,goal_id,task_id,created,read_at) VALUES(?,?,?,?,?,?,?,?,?)",
                            (s, r, k, subj, body, goal, tid, iso(ts), iso(ts + dt.timedelta(minutes=5)) if read else None))
            for team, score, notes, ts in evals:
                con.execute("INSERT INTO evaluations(team_id,score,notes,author,created) VALUES(?,?,?,?,?)", (team, score, notes, "director", iso(ts)))
            con.execute("INSERT INTO config(key,value) VALUES('max_live_agents',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", (str(W.n_live),))
            con.execute("INSERT INTO config(key,value) VALUES('max_total_claude',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", (str(W.n_live + 6),))
            # init copies DEFAULT_CONFIG, whose serve_hostnames is the owner's local DNS name: replace it
            con.execute("INSERT INTO config(key,value) VALUES('serve_hostnames','demo.example') ON CONFLICT(key) DO UPDATE SET value=excluded.value")
        con.execute("PRAGMA wal_checkpoint(TRUNCATE)")
        count = {t: con.execute(f"SELECT COUNT(*) FROM {t}").fetchone()[0] for t in ("goals", "tasks", "verdicts", "messages", "agents", "events")}
        dump = "\n".join(con.iterdump())
    finally:
        con.close()
    return count, dump, sessions


def session_plan(now, agents):
    """agent name -> {sid, project, start, end, requests}: the sessions the fake logs describe."""
    plan = {}
    n = 0
    for name, a in agents.items():
        if a[7] is None or name.startswith(("reviewer", "judge", "staff", "cleaner")):
            continue
        n += 1
        start = dt.datetime.fromisoformat(a[7])
        end = dt.datetime.fromisoformat(a[8]) if a[8] else now
        goal, tid = a[2], a[4]
        proj = f"-srv-demo-worktrees-{goal}-{tid}" if goal and tid else "-srv-demo-factory"
        if not tid and not goal:
            proj = "-srv-demo-factory"
        reqs = 36 if a[1] in ("boss", "director") else (22 if a[1] == "lead" else 8)
        # attribution: most sessions resolve by the agent-name line, three by the stored session id
        sid = f"{n:08x}-0000-4000-8000-{n:012x}"
        plan[name] = {"sid": sid if n % 3 == 0 else None, "log_sid": sid, "project": proj, "start": start, "end": end,
                      "requests": reqs, "model": a[6], "name": name}
    return plan


def write_logs(home, now, sessions):
    """Fake ~/.claude: projects/<project>/<session>.jsonl in the format web/tokens.py parses, plus usage-limits.json."""
    rng = random.Random(SEED)
    claude = home / "home" / ".claude"
    shutil.rmtree(claude, ignore_errors=True)
    for name in sorted(sessions):
        s = sessions[name]
        d = claude / "projects" / s["project"]
        d.mkdir(parents=True, exist_ok=True)
        lines = [json.dumps({"type": "agent-name", "agentName": name, "sessionId": s["log_sid"]})]
        span = (s["end"] - s["start"]).total_seconds()
        big = s["model"] == "claude-opus-5-5"
        for k in range(s["requests"]):
            ts = s["start"] + dt.timedelta(seconds=int(span * (k + 0.5) / s["requests"]))
            usage = {"input_tokens": rng.randint(30, 400), "output_tokens": rng.randint(300, 4000) * (2 if big else 1),
                     "cache_read_input_tokens": rng.randint(20000, 160000),
                     "cache_creation_input_tokens": rng.randint(2000, 20000)}
            usage["cache_creation"] = {"ephemeral_5m_input_tokens": usage["cache_creation_input_tokens"], "ephemeral_1h_input_tokens": 0}
            lines.append(json.dumps({
                "type": "assistant", "sessionId": s["log_sid"], "cwd": "/srv/demo/work",
                "timestamp": ts.strftime("%Y-%m-%dT%H:%M:%S.000Z"), "requestId": f"req_demo_{s['log_sid'][:8]}_{k}",
                "message": {"id": f"msg_demo_{s['log_sid'][:8]}_{k}", "model": s["model"], "usage": usage}}))
        (d / f"{s['log_sid']}.jsonl").write_text("\n".join(lines) + "\n", encoding="utf-8")
    (claude / "usage-limits.json").write_text(json.dumps({
        "updated_at": int(now.timestamp()), "five_hour_pct": 38, "seven_day_pct": 54,
        "five_hour_resets_at": int(now.timestamp()) + 3 * 3600, "seven_day_resets_at": int(now.timestamp()) + 3 * 86400}) + "\n",
        encoding="utf-8")


# ───────────────────────────── main ─────────────────────────────

def run_init(home):
    env = {k: v for k, v in os.environ.items() if not k.startswith(("FACTORY_", "HERDR_")) and k != "CLAUDECODE"}
    env.update(FACTORY_HOME=str(home), FACTORY_AGENT="owner", HOME=str(home / "home"))
    (home / "home").mkdir(parents=True, exist_ok=True)
    p = subprocess.run([sys.executable, str(FACTORY), "init"], env=env, capture_output=True, text=True, cwd=str(home))
    if p.returncode != 0:
        raise RuntimeError(f"bin/factory init failed (exit {p.returncode}): {(p.stderr or p.stdout).strip()}")


def scrub(home, dump):
    path = home / DUMP_NAME
    path.write_text(dump + "\n", encoding="utf-8")
    try:
        p = subprocess.run([sys.executable, str(SCAN), "--path", str(path)], capture_output=True, text=True)
    finally:
        path.unlink(missing_ok=True)
    return p


def parser():
    p = argparse.ArgumentParser(
        description="Seed a demo FACTORY_HOME with fictional data (screenshots, docs examples). Refuses the live home.",
        epilog="The default --now is the current UTC time floored to the hour, NOT a fixed date: the web pages window on the "
               "real clock (throughput 14 days, agents ended in 24 h, resources 7 days). The same --now gives an identical "
               "dump. View it with: HOME=<DIR>/home FACTORY_HOME=<DIR> factory serve --bind 127.0.0.1 --port 8799 "
               "(the HOME prefix is mandatory: without it the tokens page shows the real owner's live usage).")
    p.add_argument("--home", required=True, metavar="DIR", help="demo factory home to create (must not be the live home)")
    p.add_argument("--now", metavar="ISO", help="reference time, floored to the hour (default: now in UTC, floored to the hour)")
    p.add_argument("--force", action="store_true", help="rebuild DIR when it already holds a DB (db/factory.db)")
    return p


def main(argv=None):
    a = parser().parse_args(argv)
    try:
        now = parse_now(a.now)
    except ValueError as e:
        parser().error(str(e))
    try:
        home = check_target(a.home, a.force)
    except Refused as e:
        print(f"demo_seed: {e}", file=sys.stderr)
        return 3
    if a.force:
        for base in (home / "db" / "factory.db", home / "factory.db"):
            for suffix in ("", "-wal", "-shm"):
                Path(f"{base}{suffix}").unlink(missing_ok=True)
    home.mkdir(parents=True, exist_ok=True)
    try:
        run_init(home)
        db = db_path(home)
        count, dump, sessions = fill_db(db, now)
        write_logs(home, now, sessions)
    except (RuntimeError, sqlite3.Error, OSError) as e:
        print(f"demo_seed: failed: {e}", file=sys.stderr)
        return 1
    p = scrub(home, dump)
    if p.returncode != 0:
        print(f"demo_seed: scrub scan failed (exit {p.returncode}); the demo DB is not safe to publish:\n{p.stdout}{p.stderr}", file=sys.stderr)
        return 1 if p.returncode == 1 else 2
    print(f"seeded {db} at now={iso(now)}: " + ", ".join(f"{n} {t}" for t, n in count.items()))
    print("scrub scan: no hits")
    print("view it with (the HOME prefix is MANDATORY: without it the tokens page shows the real owner's live usage):")
    print(f"  HOME={home}/home FACTORY_HOME={home} factory serve --bind 127.0.0.1 --port 8799")
    return 0


if __name__ == "__main__":
    sys.exit(main())
