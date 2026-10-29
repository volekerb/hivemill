# qa plugins

Factory-authored. `qa-skills` carries only `workflows/multi-angle-review.js`, a symlink to the single script in `roles/workflows/`, so qa can run it by its namespaced name `qa-skills:multi-angle-review`. spawn loads the plugin from the main checkout (`--plugin-dir <FACTORY_HOME>/roles/plugins/qa`), never from the PR worktree. See `docs/review-workflow.md`.
