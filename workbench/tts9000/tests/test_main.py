"""Tests for the TTS9000 bot's cache and logging behavior."""

import logging
import os
import time
from pathlib import Path

import main


def test_httpx_log_level_is_suppressed():
    # httpx request URLs contain the bot token; INFO logging would leak it.
    assert logging.getLogger("httpx").getEffectiveLevel() >= logging.WARNING


def test_prune_cache_removes_old_files(tmp_path):
    old_file = tmp_path / "old.mp3"
    old_file.write_bytes(b"data")

    old_time = time.time() - 40 * 86400
    os.utime(old_file, (old_time, old_time))

    recent_file = tmp_path / "recent.mp3"
    recent_file.write_bytes(b"data")

    main.prune_cache(tmp_path, max_age_days=30)

    assert not old_file.exists()
    assert recent_file.exists()


def test_prune_cache_keeps_fresh_files(tmp_path):
    fresh_file = tmp_path / "fresh.mp3"
    fresh_file.write_bytes(b"data")

    main.prune_cache(tmp_path, max_age_days=30)

    assert fresh_file.exists()


def test_prune_cache_missing_dir(tmp_path):
    # Must not raise when the cache directory does not exist yet.
    main.prune_cache(Path(tmp_path) / "nonexistent", max_age_days=30)
