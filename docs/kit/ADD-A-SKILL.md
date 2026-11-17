# Add a Hivemill Kit skill

A kit skill is a SKILL.md, one Go binary and a small JSON config, with no factory DB (ADR 0007). Adding one is a single PR. `kit-check` validates everything below, so run it before you push:

```
cd go && go run ./cmd/kit-check --root ..
```

It prints one line per problem (`kit-check: <path>: <problem>`) and exits 1, or `kit-check: ok (N skills)` and exits 0.

## Steps

1. **Pick the name.** Lowercase letters, digits and dashes, starts with a letter, at most 31 characters. Below, `<name>` is that name.
2. **Copy the template.** `cp -r kit/template plugins/kit-<name>`, then rename the files and replace the placeholder `NAME` and the `@VERSION@` token everywhere:
   - `skills/NAME/` to `skills/<name>/`
   - `NAME.example.json` to `<name>.example.json`
   - `bin/hm-NAME` to `bin/hm-<name>`
   - `@VERSION@` to the content of the `VERSION` file (plugin.json and the shim).
3. **Write the skill.** `plugins/kit-<name>/skills/<name>/SKILL.md` needs front matter with `name` (equal to `<name>`) and `description`. Keep the binary lookup order and the rule never to ask for or repeat a token. Never put a token, password or key in an example config, an eval case or a fixture; use an empty string or a placeholder such as `<your-token>`.
4. **Write the binary.** `go/cmd/hm-<name>/main.go` with the code in `go/internal/kit/<name>/`. Use `kit.LoadConfig` for the config (strict JSON, integer `schemaVersion`), the `kit.OK/Err/Usage/Refused` exit codes (0, 1, 2, 3) and `kit.HandleVersion` for `--version`. Do not import `go/internal/factorydb` or `modernc.org/sqlite`, and add no new Go module dependency; check with `cd go && go list -deps ./cmd/hm-<name> | grep -E 'factorydb|modernc.org/sqlite'` (must print nothing).
5. **Add eval cases.** At least 3 files `plugins/kit-<name>/eval/cases/*.json`. Each case has `name`, `argv`, optional `stdin`, `env` and `fixture` (a directory under `eval/fixtures/`), and the expectations `exitCode`, `stdoutContains`, `stderrContains`. The template has three to start from: version, usage error, config refused.
6. **Pin the shim.** The shim `bin/hm-<name>` carries `VERSION` and four pins `SHA_LINUX_AMD64`, `SHA_LINUX_ARM64`, `SHA_DARWIN_AMD64`, `SHA_DARWIN_ARM64` (64 lowercase hex each). 64 zeros mean "not pinned yet": the shim then refuses to run. The release process writes the real values; the download base stays https only.
7. **Register it.** Add one entry to `kit/skills.json`:

   ```json
   {
     "name": "<name>",
     "level": "SOLO",
     "plugin": "plugins/kit-<name>",
     "binary": "hm-<name>",
     "description": "One sentence, at most 200 characters."
   }
   ```

   `level` is `SOLO`, `HOMELAB` or `FULL` (each level is a superset of the one before).
8. **List it in the marketplace.** Add to `.claude-plugin/marketplace.json` an entry with `"name": "hivemill-<name>"` and `"source": "./plugins/kit-<name>"` (plus `description` and `category`).
9. **Check and open the PR.** Run `kit-check` as above, then the Go tests for your package. CI discovers the plugin from `kit/skills.json`; no workflow edit is needed.

## What kit-check verifies

For the registry: valid strict JSON with `schemaVersion`, field rules for every entry, no duplicate names. For every entry:

- `plugins/kit-<name>/.claude-plugin/plugin.json` with name `hivemill-<name>` and a version equal to the `VERSION` file;
- `skills/<name>/SKILL.md` with front matter `name` and `description`;
- `<name>.example.json`: valid JSON with `schemaVersion`;
- the shim `bin/hm-<name>`: a `VERSION` line equal to the `VERSION` file, the four pins, an https-only download base;
- at least 3 `.json` files in `eval/cases/`;
- the marketplace entry, and the directory `go/cmd/hm-<name>/`;
- a credential scan over the example config, eval cases and eval fixtures: a key whose name contains token, password, secret, apikey or privatekey (any case) with a non-empty value that is not an `<angle-bracket placeholder>`, any string starting `ghp_`, `github_pat_` or `sk-`, or containing `-----BEGIN`.

It also validates `kit/template` itself, substituting `example` for `NAME`, so the template cannot rot.
