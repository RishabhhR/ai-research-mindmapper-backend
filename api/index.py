import sys
import os

# Make project root importable so `from app.main import app` resolves
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from app.main import app  # noqa: F401 — Vercel looks for `app` at module level
