# Third-party skills

Hivemill registers these skills in `kit/skills.json` as external skills: a repository, a pinned commit and an install
command. **No file of theirs is copied into this repository.** The installer fetches each one from its own repository
on the user's machine, where the upstream licence applies.

| Skill | Author (repository) | Pinned commit | Licence | Why it is not vendored |
|---|---|---|---|---|
| ui-ux-pro-max | nextlevelbuilder (<https://github.com/nextlevelbuilder/ui-ux-pro-max-skill>) | `b73ee96f8d4ff7bbc279c52dadcd907ffe23629c` | MIT | The skill ships Python scripts, and this repository takes no new Python. |
| taste-skill | Leonxlnx (<https://github.com/Leonxlnx/taste-skill>) | `d1a2bb41d927083ac7118f60cfc9cdf711810ea0` | MIT for the repository | The skill directory bundles photos under CC BY, CC BY-SA and the Unsplash License (see its `assets/photos/photos.json`), so it is not permissive as a whole. |
| impeccable | pbakaus (<https://github.com/pbakaus/impeccable>) | `2fc5e1459efbbf06d92c7306517c286bf12490cf` | Apache-2.0 (see its `NOTICE.md`) | A copy does not pass this repository's pre-publication text check. |

Licences were read from each repository at the pinned commit. Check them again when you move a pin.
