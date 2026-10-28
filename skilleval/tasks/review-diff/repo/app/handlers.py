"""Request handlers: plain functions taking a connection and a dict of params."""
from app import store
from app.util import clean_title, truncate


def create(con, params):
    title = clean_title(params["title"])
    if not title:
        return {"status": 400, "error": "title required"}
    note_id = store.add_note(con, params["user"], title, params.get("body", ""))
    return {"status": 201, "id": note_id}


def show(con, params):
    row = store.get_note(con, int(params["id"]))
    if row is None or row["owner"] != params["user"]:
        return {"status": 404}
    return {"status": 200, "title": row["title"], "body": row["body"]}


def index(con, params):
    rows = store.list_notes(con, params["user"], int(params.get("page", 0)))
    return {"status": 200, "notes": [truncate(r["title"]) for r in rows]}


def search(con, params):
    term = params.get("q", "").strip()
    if not term:
        return {"status": 400, "error": "q required"}
    rows = store.search_notes(con, params["user"], term)
    return {"status": 200, "notes": [truncate(r["title"]) for r in rows]}
