"""Page registry. A view is a module (or namespace) with TITLE, data(ctx, **params) -> dict and render(ctx, data) -> str.

View tasks replace only their own module; this registry is fixed. `/goal/<id>` and `/task/<id>` map to the
`detail` attribute of the goals/tasks modules.
"""
from web.views import agents, changelog, goals, home, quality, resources, tasks, throughput, tokens

ROUTES = {
    "/": home,
    "/goals": goals,
    "/goal/<id>": goals.detail,
    "/tasks": tasks,
    "/task/<id>": tasks.detail,
    "/agents": agents,
    "/resources": resources,
    "/throughput": throughput,
    "/quality": quality,
    "/changelog": changelog,
    "/tokens": tokens,
}
