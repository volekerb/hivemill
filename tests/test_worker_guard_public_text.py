"""G-021 T2: the worker-guard public-text and frozen-repo rule, run through the real hook (python3 worker-guard.py
--role <r> | --hq, tool JSON on stdin) with a temp FACTORY_HOME/DB, a temp git repo with a fake origin URL and a temp
terms file. Private-looking values are built at run time so this file holds none."""
import json
import os
import shutil
import sqlite3
import subprocess
import sys
import tempfile
import time
import unittest
import _gitenv  # noqa: F401  (hermetic git: tests/_gitenv.py)
import harness

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
HOOK = os.path.join(ROOT, "roles", "hooks", "worker-guard.py")
GIT_ENV = {"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@example.com", "GIT_COMMITTER_NAME": "t",
           "GIT_COMMITTER_EMAIL": "t@example.com", "GIT_CONFIG_GLOBAL": os.devnull, "GIT_CONFIG_SYSTEM": os.devnull}
NOREPLY = "volekerb <1+volekerb@users.noreply.github.com>"
LAN_IP = ".".join(["192", "168", "1", "4"])
TEN_IP = ".".join(["10", "0", "0", "5"])


class PublicTextTests(unittest.TestCase):
    def setUp(self):
        self.tmp = os.path.realpath(tempfile.mkdtemp(prefix="zz-test-pubtext-"))
        self.addCleanup(shutil.rmtree, self.tmp, True)
        self.home = os.path.join(self.tmp, "home")
        os.makedirs(os.path.join(self.home, "db"))
        self.terms = os.path.join(self.tmp, "terms.txt")
        with open(self.terms, "w") as f:
            f.write("zebracorp\n")
        self.repo = os.path.join(self.tmp, "repo")
        self.git("init", "-q", "-b", "main", self.repo, cwd=self.tmp)
        self.set_origin("https://github.com/acme/pub.git")
        self.commit("first")
        self.git("update-ref", "refs/remotes/origin/main", "HEAD")
        self.config()

    # ── helpers
    def git(self, *args, cwd=None, env=None):
        return harness.git(*args, cwd=cwd or self.repo, env={**GIT_ENV, **(env or {})}, inert_remote=True)

    def set_origin(self, url):
        subprocess.run(["git", "remote", "remove", "origin"], cwd=self.repo, capture_output=True)
        self.git("remote", "add", "origin", url)

    def commit(self, msg, author=NOREPLY):
        name, email = author.split(" <")
        self.git("commit", "-q", "--allow-empty", "-m", msg,
                 env={"GIT_AUTHOR_NAME": name, "GIT_AUTHOR_EMAIL": email.rstrip(">"),
                      "GIT_COMMITTER_NAME": name, "GIT_COMMITTER_EMAIL": email.rstrip(">")})

    def config(self, **kv):
        db = os.path.join(self.home, "db", "factory.db")
        con = sqlite3.connect(db)
        con.execute("CREATE TABLE IF NOT EXISTS config (key TEXT PRIMARY KEY, value TEXT NOT NULL)")
        con.execute("DELETE FROM config")
        con.executemany("INSERT INTO config VALUES (?, ?)", kv.items())
        con.commit()
        con.close()

    def public(self, **kv):
        self.config(public_text_repos="acme/pub", **kv)

    def run_hook(self, cmd, mode="coder", terms=True, cwd=None):
        env = {k: v for k, v in os.environ.items() if k not in ("SCRUB_TERMS_FILE", "FACTORY_AGENT")}
        env.update(FACTORY_HOME=self.home, **GIT_ENV)
        if terms:
            env["SCRUB_TERMS_FILE"] = self.terms
        argv = [sys.executable, HOOK, "--hq"] if mode == "hq" else [sys.executable, HOOK, "--role", mode]
        payload = {"tool_name": "Bash", "tool_input": {"command": cmd}, "cwd": cwd or self.repo}
        return subprocess.run(argv, input=json.dumps(payload), capture_output=True, text=True, timeout=60, env=env)

    def blocked(self, cmd, *want, **kw):
        p = self.run_hook(cmd, **kw)
        self.assertEqual(p.returncode, 2, f"should block: {cmd!r}\n{p.stderr}")
        for w in want:
            self.assertIn(w, p.stderr)
        return p

    def allowed(self, cmd, **kw):
        p = self.run_hook(cmd, **kw)
        self.assertEqual(p.returncode, 0, f"should allow: {cmd!r}\n{p.stderr}")

    def write(self, name, text):
        path = os.path.join(self.tmp, name)
        with open(path, "w") as f:
            f.write(text)
        return path

    # ── 2 off by default
    def test_off_by_default_allows_everything(self):
        self.config()
        self.allowed(f'gh pr create --title "zebracorp at {TEN_IP}" --body x')
        self.allowed(f'git commit -m "zebracorp {TEN_IP}"')

    # ── 3 identity on push
    def test_push_blocks_real_author_email(self):
        self.public()
        self.commit("add rule", author="Alice <alice@realmail.test>")
        p = self.blocked("git push origin HEAD:factory/t-1")
        self.assertIn("public text:", p.stderr)
        self.assertIn("email", p.stderr)
        self.assertNotIn("alice", p.stderr)

    def test_push_allows_noreply_identity_and_clean_content(self):
        self.public()
        self.commit("add rule")
        self.allowed("git push origin HEAD:factory/t-1")

    def test_push_blocks_owner_term_in_an_older_commit_of_the_range(self):
        self.public()
        self.commit("add zebracorp notes")
        self.commit("fix the notes")
        p = self.blocked("git push origin HEAD:factory/t-1")
        self.assertIn("owner-term", p.stderr)
        self.assertNotIn("zebracorp", p.stderr)

    # ── 4 scope = the write command only
    def test_chained_cd_to_home_path_is_not_scanned(self):
        self.public()
        home_path = "/ho" + "me/alice/x"
        self.allowed(f'cd {home_path} && git commit -m "T-0528: add rule"', cwd=self.repo)

    def test_home_path_in_commit_message_is_blocked(self):
        self.public()
        self.blocked('git commit -m "see ' + "/ho" + 'me/alice/notes"', "home-path")

    # ── 5 bodies
    def test_body_with_owner_term_blocked(self):
        self.public()
        self.blocked("gh pr create --title t --body 'about zebracorp'", "public text:")

    def test_body_file_with_private_ip_blocked_and_clean_file_allowed(self):
        self.public()
        self.blocked(f"gh pr create --title t --body-file {self.write('bad.md', 'host ' + LAN_IP)}", "public text:")
        self.allowed(f"gh pr create --title t --body-file {self.write('ok.md', 'Fixes T-0528, PR #3')}")

    def test_unreadable_body_file_blocked(self):
        self.public()
        self.blocked(f"gh pr create --title t --body-file {self.tmp}/missing.md", "cannot read")

    def test_heredoc_body_checked(self):
        self.public()
        bad = "gh pr create --title t --body \"$(cat <<'EOF'\nabout zebracorp\nEOF\n)\""
        good = "gh pr create --title t --body \"$(cat <<'EOF'\nabout the rule\nEOF\n)\""
        self.blocked(bad, "public text:")
        self.allowed(good)

    def test_stdin_heredoc_with_body_file_dash_checked(self):
        self.public()
        self.blocked("gh pr create --title t --body-file - <<'EOF'\nzebracorp\nEOF\n", "public text:")
        self.allowed("gh pr create --title t --body-file - <<'EOF'\nfine\nEOF\n")

    def test_body_file_only_in_a_standalone_call(self):
        """The body file is read before the command runs, so any chain, pipe, substitution or newline around the call
        means the content may change: refused. The same call alone is scanned and allowed."""
        self.public()
        body = self.write("body.md", "Fixes T-0528")  # clean on disk; the probes below would rewrite it
        gh = f"gh pr create --title t --body-file {body}"
        other = self.write("other.md", "about zebracorp")
        probes = [
            f"cat > {body} <<'EOF'\nabout zebracorp\nEOF\n{gh}",
            f"cat <<'EOF' > {body}\nabout the rule\nEOF\n{gh}",
            f"echo hi > {body} && {gh}",
            f"git show main:x > {body} && gh pr edit 1 --body-file {body}",
            f"echo hi | tee {body} && {gh}",
            f"cp {other} {body}; {gh}",
            f"cd {self.repo} && {gh}",
            f"git checkout main -- body.md && gh pr create --title t --body-file body.md",
            f"git restore --source=main body.md && gh pr create --title t --body-file body.md",
            f"perl -pi -e 's/a/b/' {body} && {gh}",
            f"python3 -c \"open('{body}','w').write('x')\" && {gh}",
            f"awk -i inplace '1' {body} && {gh}",
            f"patch {body} < x.patch && {gh}",
            f"{gh} &",
            f"{gh} || true",
            f"echo $({gh})",
            f"sh -c '{gh}'",
            f"{gh}\necho done",
        ]
        for cmd in probes:
            self.blocked(cmd, "separate command")
        self.allowed(gh)
        self.allowed(f"{gh} 2>/dev/null")
        self.allowed(f"{gh} 2>&1")
        self.blocked(f"gh pr create --title t --body-file {other}", "public text:")

    def test_body_file_symlink_and_hardlink_refused(self):
        self.public()
        real = self.write("real.md", "Fixes T-0528")
        link = os.path.join(self.tmp, "lnk.md")
        os.symlink(real, link)
        self.blocked(f"gh pr create --title t --body-file {link}", "symlink")
        hard = os.path.join(self.tmp, "hard.md")
        os.link(real, hard)
        self.blocked(f"gh pr create --title t --body-file {hard}", "hard links")
        self.blocked(f"gh pr create --title t --body-file {real}", "hard links")
        self.allowed(f"gh pr create --title t --body-file {self.write('single.md', 'Fixes T-0528')}")

    def test_clean_existing_body_file_still_allowed(self):
        self.public()
        body = self.write("body.md", "Fixes T-0528")
        self.allowed(f"gh pr create --title t --body-file {body} 2>/dev/null")
        self.blocked(f"gh pr create --title t --body-file {self.write('bad.md', 'zebracorp')} > {self.tmp}/log.txt", "public text:")

    def test_process_substitution_body_file_message_names_the_form(self):
        self.public()
        p = self.blocked("gh pr create --title t -F <(echo hi)", "process-substitution")
        self.assertNotIn("file ''", p.stderr)

    def test_dynamic_text_blocked(self):
        self.public()
        self.blocked('gh pr create --title t --body "$X"', "write the text to a file")
        self.blocked("gh pr create --title t --body \"`date`\"", "write the text to a file")

    def test_gh_api_patch_body_checked(self):
        self.public()
        self.blocked("gh api -X PATCH repos/acme/pub/pulls/1 -f body=zebracorp", "public text:")
        self.allowed("gh api -X PATCH repos/acme/pub/pulls/1 -f body=fine")
        self.allowed("gh api repos/acme/pub/pulls/1")  # a read

    def test_other_text_flags_checked(self):
        self.public()
        self.blocked("gh pr review 1 --comment --body zebracorp", "public text:")
        self.blocked("gh issue comment 1 -b zebracorp", "public text:")
        self.blocked("git tag -a v1 -m zebracorp", "public text:")
        self.blocked(f"git commit -F {self.write('m.txt', 'zebracorp')}", "public text:")
        self.blocked("git commit -am zebracorp", "public text:")

    # ── 6 other repos
    def test_private_repo_untouched(self):
        self.public()
        self.set_origin("https://github.com/acme/private-proj.git")
        self.allowed("gh pr comment 1 --body zebracorp")
        self.allowed("git push origin HEAD:factory/t-1")

    def test_explicit_repo_flag_overrides_origin(self):
        self.public()
        self.set_origin("https://github.com/acme/private-proj.git")
        self.blocked("gh pr comment 1 -R acme/pub --body zebracorp", "public text:")

    # ── 7 frozen
    def test_frozen_repo_blocks_push_and_gh_writes(self):
        self.config(frozen_repos="acme/old")
        self.set_origin("git@github.com:acme/old.git")
        self.blocked("git push origin HEAD", "is frozen, G-021")
        self.blocked("gh pr create -R acme/old --title t --body b", "is frozen, G-021")
        self.blocked("git commit -m x", "is frozen, G-021")

    def test_frozen_blocks_cleaner_branch_delete_only_while_frozen(self):
        self.config(frozen_repos="acme/old")
        self.set_origin("https://github.com/acme/old.git")
        self.blocked("git push origin --delete factory/t-1", "is frozen, G-021", mode="cleaner")
        self.config()
        self.allowed("git push origin --delete factory/t-1", mode="cleaner")

    # ── 8 fail closed
    def test_no_terms_fails_closed(self):
        self.public()
        self.blocked("git commit -m ok", "no owner terms loaded", terms=False)

    def test_terms_from_db_config_when_env_unset(self):
        self.public(scrub_terms_file=self.terms)
        self.allowed("git commit -m ok", terms=False)
        self.blocked("git commit -m zebracorp", "public text:", terms=False)

    def test_unknown_target_is_public(self):
        self.public()
        self.set_origin("file:///srv/elsewhere.git")
        self.blocked("gh pr comment 1 --body zebracorp", "public text:")

    def test_unreadable_checker_import_fails_closed(self):
        # a hook copy whose ci/ has no checker: the rule applies, so the write is blocked
        fake = os.path.join(self.tmp, "fake", "roles", "hooks")
        os.makedirs(fake)
        shutil.copy(HOOK, fake)
        self.public()
        env = {**os.environ, "FACTORY_HOME": self.home, "SCRUB_TERMS_FILE": self.terms}
        p = subprocess.run([sys.executable, os.path.join(fake, "worker-guard.py"), "--role", "coder"],
                           input=json.dumps({"tool_name": "Bash", "tool_input": {"command": "git commit -m ok"},
                                             "cwd": self.repo}), capture_output=True, text=True, env=env, timeout=60)
        self.assertEqual(p.returncode, 2, p.stderr)
        self.assertIn("cannot be loaded", p.stderr)

    # ── 9 both modes + branch names
    def test_hq_mode_checks_the_same_writes(self):
        self.public()
        self.commit("add rule", author="Alice <alice@realmail.test>")
        self.blocked("git push origin HEAD:factory/t-1", "public text:", "email", mode="hq")
        self.blocked("gh pr create --title t --body zebracorp", "public text:", mode="hq")
        self.blocked("bash -c 'gh pr create --title t --body zebracorp'", "public text:", mode="hq")
        self.blocked("env X=1 gh pr create --title t --body zebracorp", "public text:", mode="hq")
        self.blocked("gh pr create --title t --body zebracorp", mode="hq", terms=True)
        self.allowed("gh pr create --title t --body fine", mode="hq")

    def test_hq_frozen(self):
        self.config(frozen_repos="acme/old")
        self.set_origin("https://github.com/acme/old.git")
        self.blocked("git push origin HEAD", "is frozen, G-021", mode="hq")

    def test_hq_dynamic_text_blocked_and_unparseable_write_blocked(self):
        self.public()
        self.blocked('gh pr create --title "$(some_cmd)" --body b', mode="hq")
        self.blocked("gh pr create --title 'unbalanced --body b", "cannot be parsed", mode="hq")
        self.allowed("echo 'unbalanced", mode="hq")  # not a write: HQ parse trouble still allows

    def test_branch_name_checked(self):
        self.public()
        self.blocked(f"git push origin HEAD:factory/{TEN_IP}", "public text:")
        self.blocked(f"git push origin HEAD:factory/{TEN_IP}", "public text:", mode="hq")

    # ── 10 speed
    def test_large_clean_body_is_fast(self):
        self.public()
        path = self.write("big.md", ("A clean line about T-0528 and PR #3.\n" * 1400)[:50_000])
        start = time.monotonic()
        self.allowed(f"gh pr create --title t --body-file {path}")
        self.assertLess(time.monotonic() - start, 2.0)

    # ── 13 guard contract
    def test_matched_value_never_printed(self):
        self.public()
        p = self.blocked("gh pr create --title t --body 'about zebracorp'")
        self.assertNotIn("zebracorp", p.stderr)

    # ── self-review findings (subshell cd, --git-dir, aliases, --repo=, refspec forms, api files)
    def other_repo(self):
        other = os.path.join(self.tmp, "other")
        self.git("init", "-q", "-b", "main", other, cwd=self.tmp)
        self.git("remote", "add", "origin", "https://github.com/acme/private-proj.git", cwd=other)
        return other

    def test_subshell_cd_does_not_hide_the_public_repo(self):
        self.public()
        other = self.other_repo()
        self.blocked(f"(cd {other}; true); git commit --allow-empty -m zebracorp", "public text:")
        self.blocked(f"(cd {other}; true); git commit --allow-empty -m zebracorp", "public text:", mode="hq")

    def test_git_dir_and_alias_forms_checked(self):
        self.public()
        other = self.other_repo()
        self.blocked(f"git --git-dir={self.repo}/.git commit --allow-empty -m zebracorp", "public text:", cwd=other)
        self.blocked("git -c alias.ci=commit ci --allow-empty -m zebracorp", "alias")
        self.blocked("git notes add -m zebracorp", "public text:")

    def test_push_repo_option_forms_check_the_refspec(self):
        self.public()
        url = "https://github.com/acme/pub.git"
        self.blocked(f"git push --repo={url} HEAD:factory/{TEN_IP}", "public text:")
        self.blocked(f"git push --repo {url} HEAD:factory/{TEN_IP}", "public text:")

    def test_push_delete_all_tags_and_url_remote(self):
        self.public()
        self.git("branch", f"factory/{TEN_IP}")
        self.blocked(f"git push origin --delete factory/{TEN_IP}", "public text:")
        self.blocked("git push --all origin", "public text:")
        self.git("branch", "-D", f"factory/{TEN_IP}")
        self.git("tag", "-a", "v-" + LAN_IP, "-m", "m")
        self.blocked("git push --tags origin", "public text:")
        self.git("tag", "-d", "v-" + LAN_IP)
        self.allowed("git push --all origin")
        self.blocked(f"git push https://github.com/acme/pub.git HEAD:factory/{TEN_IP}", "public text:")

    def test_push_unresolvable_source_and_missing_base_fail_closed(self):
        self.public()
        self.blocked("git push origin nosuchbranch:factory/x", "cannot resolve push source")
        self.git("update-ref", "-d", "refs/remotes/origin/main")
        self.blocked("git push origin HEAD:factory/x", "no base")

    def test_git_dash_c_dir_selects_repo(self):
        self.public()
        other = self.other_repo()
        self.blocked(f"git -C {self.repo} commit --allow-empty -m zebracorp", "public text:", cwd=other)
        self.allowed(f"git -C {other} commit --allow-empty -m zebracorp", cwd=other)

    def test_gh_api_files_and_input_and_release_and_head(self):
        self.public()
        bad, ok = self.write("bad.txt", "zebracorp"), self.write("ok.txt", "fine")
        self.blocked(f"gh api -X POST repos/acme/pub/issues -F body=@{bad}", "public text:")
        self.blocked(f"gh api -X POST repos/acme/pub/issues --input {bad}", "public text:")
        self.allowed(f"gh api -X POST repos/acme/pub/issues --input {ok}")
        self.blocked("gh release create v1 --notes zebracorp", "public text:")
        self.blocked(f"gh release create v-{TEN_IP} --notes ok", "public text:")
        self.blocked(f"gh pr create --title t --body b --head factory/{TEN_IP}", "public text:")
        self.blocked('gh pr create --title t --body b --head "$B"', "dynamic")

    def test_frozen_blocks_gh_api_write(self):
        self.config(frozen_repos="acme/old")
        self.blocked("gh api -X PATCH repos/acme/old/pulls/1 -f body=x", "is frozen, G-021")
        self.allowed("gh api repos/acme/old/pulls/1")

    def test_stdin_like_body_file_paths_are_the_heredoc_payload(self):
        self.public()
        for path in ("/dev/stdin", "/dev/fd/0", "/proc/self/fd/0", "/proc/1234/fd/0", "/dev/fd/../fd/0"):
            for flag in ("--body-file", "-F", "--body-file="):
                sep = "" if flag.endswith("=") else " "
                self.blocked(f"gh pr create --title t {flag}{sep}{path} <<'EOF'\nzebracorp\nEOF\n", "public text:")
            self.allowed(f"gh pr create --title t --body-file {path} <<'EOF'\nfine\nEOF\n")
        self.blocked("gh pr create --title t --body-file /dev/stdin", "stdin text cannot be inspected")
        self.blocked("gh release create v1 --notes-file /dev/fd/0 <<'EOF'\nzebracorp\nEOF\n", "public text:")
        self.blocked("gh api -X POST repos/acme/pub/issues --input /proc/self/fd/0 <<'EOF'\nzebracorp\nEOF\n", "public text:")
        self.blocked("git commit -F /dev/stdin <<'EOF'\nzebracorp\nEOF\n", "public text:")

    def test_non_regular_text_file_blocked(self):
        self.public()
        fifo = os.path.join(self.tmp, "fifo")
        os.mkfifo(fifo)
        self.blocked(f"gh pr create --title t --body-file {fifo}", "not a regular file")
        self.blocked("gh pr create --title t --body-file /dev/null", "not a regular file")
        link = os.path.join(self.tmp, "link")
        os.symlink("/dev/stdin", link)
        self.blocked(f"gh pr create --title t --body-file {link} <<'EOF'\nzebracorp\nEOF\n", "separate command")

    def test_pushurl_pointing_at_frozen_repo_is_seen(self):
        self.config(frozen_repos="acme/old")
        self.git("remote", "set-url", "--push", "origin", "https://github.com/acme/old.git")
        self.blocked("git push origin HEAD:factory/t-1", "is frozen, G-021")

    def test_pushurl_pointing_at_public_repo_is_seen(self):
        self.public()
        self.set_origin("https://github.com/acme/other.git")
        self.git("remote", "set-url", "--push", "origin", "https://github.com/acme/pub.git")
        self.blocked(f"git commit -m 'host {LAN_IP}'", "public text:")

    def test_missing_config_table_leaves_rule_off(self):
        os.remove(os.path.join(self.home, "db", "factory.db"))
        self.allowed(f"gh pr create --title 'zebracorp {TEN_IP}' --body x")


if __name__ == "__main__":
    unittest.main()
