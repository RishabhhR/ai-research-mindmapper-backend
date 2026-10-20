"""Shared test setup: dummy env vars so app modules import without real services."""
import os
import sys

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

# Must be set before any app import — storage refuses to start without Turso config.
os.environ.setdefault("TURSO_URL", "libsql://test-db.example.turso.io")
os.environ.setdefault("TURSO_AUTH_TOKEN", "test-token")
os.environ.setdefault("RATE_LIMIT_DISABLED", "0")
