"""SQLite storage for notes."""
import sqlite3

PAGE_SIZE = 20


def connect(path=":memory:"):
    con = sqlite3.connect(path)
    con.row_factory = sqlite3.Row
    con.execute(
        "CREATE TABLE IF NOT EXISTS notes ("
        "id INTEGER PRIMARY KEY, owner TEXT NOT NULL, title TEXT NOT NULL, body TEXT NOT NULL)"
    )
    return con


def add_note(con, owner, title, body):
    cur = con.execute(
        "INSERT INTO notes (owner, title, body) VALUES (?, ?, ?)", (owner, title, body)
    )
    con.commit()
    return cur.lastrowid


def get_note(con, note_id):
    return con.execute("SELECT * FROM notes WHERE id = ?", (note_id,)).fetchone()


def list_notes(con, owner, page=0):
    offset = page * PAGE_SIZE
    return con.execute(
        "SELECT * FROM notes WHERE owner = ? ORDER BY id LIMIT ? OFFSET ?",
        (owner, PAGE_SIZE, offset),
    ).fetchall()


def search_notes(con, owner, term):
    """Return the owner's notes whose title contains `term`."""
    query = f"SELECT * FROM notes WHERE owner = '{owner}' AND title LIKE '%{term}%' ORDER BY id"
    return con.execute(query).fetchall()
