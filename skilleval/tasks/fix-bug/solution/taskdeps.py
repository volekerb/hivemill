"""Resolve the order in which tasks must be worked on.

A task map looks like {"T-2": ["T-1"], "T-1": [], "T-3": ["T-1", "T-2"]}:
each key is a task id, each value the ids it depends on (must be finished
first).  resolve_order() returns every task id exactly once, dependencies
before dependents.  Among tasks that are free to go next, ids are taken in
sorted order so the result is deterministic.

Errors (ValueError):
  * a dependency that is not a key of the map: "unknown dependency <dep> for <task>"
  * a dependency cycle: "dependency cycle: <a> -> <b> -> ... -> <a>"
"""


def _normalize(tasks):
    """Return {id: sorted unique deps}, dropping duplicate dependency entries."""
    return {t: sorted(set(deps)) for t, deps in tasks.items()}


def resolve_order(tasks):
    """Return task ids ordered so every task comes after its dependencies."""
    graph = _normalize(tasks)
    for task in sorted(graph):
        for dep in graph[task]:
            if dep not in graph:
                raise ValueError("unknown dependency %s for %s" % (dep, task))
    _check_acyclic(graph)
    order = []
    done = set()
    while len(order) < len(graph):
        free = ready_tasks(graph, done)
        task = free[0]
        done.add(task)
        order.append(task)
    return order


def _check_acyclic(graph):
    state = {}

    def visit(task, path):
        if state.get(task) == "done":
            return
        if task in path:
            cycle = path[path.index(task):] + [task]
            raise ValueError("dependency cycle: " + " -> ".join(cycle))
        for dep in graph[task]:
            visit(dep, path + [task])
        state[task] = "done"

    for task in sorted(graph):
        visit(task, [])


def ready_tasks(tasks, done):
    """Return sorted ids of tasks not in `done` whose dependencies are all in `done`."""
    done = set(done)
    graph = _normalize(tasks)
    return sorted(
        t for t, deps in graph.items()
        if t not in done and all(d in done for d in deps)
    )


def format_order(tasks):
    """One line per task: "1. T-1" or "2. T-2 (after T-1)"."""
    graph = _normalize(tasks)
    lines = []
    for n, task in enumerate(resolve_order(tasks), 1):
        deps = graph[task]
        suffix = " (after %s)" % ", ".join(deps) if deps else ""
        lines.append("%d. %s%s" % (n, task, suffix))
    return "\n".join(lines)
