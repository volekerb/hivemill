package install

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var (
	skillNameRe   = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}$`)
	skillRepoRe   = regexp.MustCompile(`^https://github\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	skillCommitRe = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

type externalSkill struct {
	Name   string `json:"name"`
	Repo   string `json:"repo"`
	Commit string `json:"commit"`
	Path   string `json:"path"`
}

// installSkills puts the external skills of the clone's kit/skills.json into the workers' skills directory
// (~/.claude/skills; workers run with the user's HOME). Each repository is fetched at its pinned commit and the skill
// directory is linked. A skill of the same name that the installer did not link is kept; its own link follows the pin.
// A skill is optional: a failure is reported and the install goes on.
func installSkills(c *Ctx) {
	raw, err := c.host.ReadFile(filepath.Join(c.fh, "kit", "skills.json"))
	if err != nil {
		return // nothing to read: a ref without the registry, or a dry run before the clone
	}
	var reg struct {
		Skills []externalSkill `json:"skills"`
	}
	if err := json.Unmarshal(raw, &reg); err != nil {
		c.note("skills: kit/skills.json is not valid JSON; no skill installed")
		return
	}
	for _, s := range reg.Skills {
		if s.Repo == "" {
			continue // a kit plugin, installed through the plugin marketplace
		}
		if !skillNameRe.MatchString(s.Name) || !skillRepoRe.MatchString(s.Repo) ||
			!skillCommitRe.MatchString(s.Commit) || !filepath.IsLocal(s.Path) {
			c.note(fmt.Sprintf("skills: %q has an invalid entry in kit/skills.json; not installed", s.Name))
			continue
		}
		if err := installSkill(c, s); err != nil {
			c.note(fmt.Sprintf("skills: %s not installed (re-run with --resume to retry): %s", s.Name,
				strings.TrimSpace(err.Error())))
		}
	}
}

func installSkill(c *Ctx, s externalSkill) error {
	skillsDir := filepath.Join(c.home, ".claude", "skills")
	dest := filepath.Join(skillsDir, s.Name)
	store := filepath.Join(c.home, ".local", "share", "hivemill", "skills")
	src := filepath.Join(store, s.Name+"@"+s.Commit[:12])
	target := filepath.Join(src, filepath.FromSlash(s.Path))
	// a store directory without the SKILL.md is the rest of an interrupted fetch and is fetched again
	fetched := c.host.Exists(filepath.Join(target, "SKILL.md"))
	relink := false
	if c.host.Exists(dest) {
		cur, err := c.host.Readlink(dest)
		ours := err == nil && strings.HasPrefix(cur, filepath.Join(store, s.Name+"@"))
		if !ours || (cur == target && fetched) {
			c.note("skills: kept the existing " + dest)
			return nil
		}
		relink = true
	}
	if !fetched {
		// fetch beside src and rename after the checkout, so a failed fetch is not taken for a finished one
		part := src + ".installing"
		if !c.plan {
			c.host.Rmtree(part)
			if err := c.host.Mkdirs(store); err != nil {
				return fmt.Errorf("create %s: %w", store, err)
			}
		}
		for _, argv := range [][]string{
			{"git", "init", "-q", part},
			{"git", "-C", part, "fetch", "--depth", "1", s.Repo, s.Commit},
			{"git", "-C", part, "checkout", "-q", "--detach", "FETCH_HEAD"},
		} {
			if _, _, err := c.do(argv, "fetch of "+s.Repo, "", 300*time.Second, true); err != nil {
				c.host.Rmtree(part)
				return err
			}
		}
		if c.plan {
			c.emit(fmt.Sprintf("    $ mv %s %s", part, src))
		} else {
			c.host.Rmtree(src)
			if err := c.host.Rename(part, src); err != nil {
				return fmt.Errorf("move the skill into place: %w", err)
			}
		}
	}
	if c.plan {
		c.emit(fmt.Sprintf("    $ ln -sfn %s %s", target, dest))
		return nil
	}
	if !c.host.Exists(filepath.Join(target, "SKILL.md")) {
		return fmt.Errorf("no SKILL.md in %s", target)
	}
	if err := c.host.Mkdirs(skillsDir); err != nil {
		return fmt.Errorf("create %s: %w", skillsDir, err)
	}
	if relink {
		c.host.Rmtree(dest) // removes the old link, not what it points to
	}
	if err := c.host.Symlink(target, dest); err != nil {
		return fmt.Errorf("link %s: %w", dest, err)
	}
	c.note("skills: installed " + s.Name)
	return nil
}
