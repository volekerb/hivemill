package cli

import "regexp"

// Constant lists of bin/factory_cli.py; the tests in roles_test.go compare each with the Python literal.
// workerRoles (WORKER_ROLES) lives in worker.go.

var factoryRoles = []string{"boss", "director", "lead", "pm", "coder", "qa", "reviewer", "judge", "cleaner",
	"staff-engineer", "researcher", "scout", "retro", "evaluator"}

var taskStatuses = []string{"todo", "in_progress", "in_review", "blocked", "done", "cancelled"}

var taskWaits = []string{"owner", "internal"}

var goalStatuses = []string{"proposed", "planning", "active", "blocked", "done", "cancelled"}

// taskIDRe is TASK_ID_RE = re.compile(r"^T-\d+$", re.I); Python's $ also matches before a final newline.
var taskIDRe = regexp.MustCompile(`(?i)^T-\p{Nd}+\n?\z`)
