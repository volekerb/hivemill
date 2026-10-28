"""The one place that picks the factory DB: <home>/db/factory.db, else the pre-migration <home>/factory.db.

Only the home decides (FACTORY_HOME, default ~/factory); no other environment variable picks the DB.
Stdlib only: bin/factory and web/context.py both import it."""
from pathlib import Path


def resolve(home):
    """Path of the DB for `home`: db/factory.db (also when neither exists yet: `init` creates it), else the legacy
    factory.db until the tick has migrated it."""
    home = Path(home)
    new = home / "db" / "factory.db"
    legacy = home / "factory.db"
    return new if new.exists() or not legacy.exists() else legacy
