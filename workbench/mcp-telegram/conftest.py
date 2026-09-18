import os

# The server reads its configuration from the environment at import time.
os.environ.setdefault("TELEGRAM_BOT_TOKEN", "123:test-token")
os.environ.setdefault("TELEGRAM_CHAT_ID", "-1001234")

# Makes pytest add the project root to sys.path so tests can import main.
