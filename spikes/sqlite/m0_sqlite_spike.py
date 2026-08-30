"""M0-B1 SQLite behavior spike using only Python's standard library.

This validates SQLite semantics independently of the future Go driver. It does
not validate process-kill, OS crash, filesystem, or power-loss durability.
"""

from __future__ import annotations

import json
import platform
import sqlite3
import tempfile
import threading
from pathlib import Path


SCHEMA = """
CREATE TABLE decisions (
    id TEXT PRIMARY KEY,
    node_id TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('Automatic', 'Manual')),
    rule_id TEXT,
    canonical_target TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('Active', 'Expired', 'Revoked'))
);

CREATE UNIQUE INDEX uq_active_automatic_decision
ON decisions(node_id, rule_id, canonical_target)
WHERE kind = 'Automatic' AND state = 'Active';

CREATE UNIQUE INDEX uq_active_manual_decision
ON decisions(node_id, canonical_target)
WHERE kind = 'Manual' AND state = 'Active';

CREATE TABLE desired_projection (
    canonical_target TEXT PRIMARY KEY,
    active_decision_count INTEGER NOT NULL CHECK (active_decision_count > 0)
);

CREATE TABLE critical_audit (
    id INTEGER PRIMARY KEY,
    message TEXT NOT NULL CHECK (length(message) > 0)
);
"""


def connect(database: Path) -> sqlite3.Connection:
    connection = sqlite3.connect(database, timeout=5.0, isolation_level=None)
    connection.execute("PRAGMA foreign_keys=ON")
    connection.execute("PRAGMA busy_timeout=5000")
    return connection


def verify_pragmas(connection: sqlite3.Connection) -> dict[str, object]:
    journal_mode = connection.execute("PRAGMA journal_mode=WAL").fetchone()[0]
    connection.execute("PRAGMA synchronous=FULL")
    connection.execute("PRAGMA wal_autocheckpoint=1000")
    values = {
        "journal_mode": journal_mode.lower(),
        "synchronous": connection.execute("PRAGMA synchronous").fetchone()[0],
        "foreign_keys": connection.execute("PRAGMA foreign_keys").fetchone()[0],
        "busy_timeout_ms": connection.execute("PRAGMA busy_timeout").fetchone()[0],
        "wal_autocheckpoint": connection.execute(
            "PRAGMA wal_autocheckpoint"
        ).fetchone()[0],
    }
    expected = {
        "journal_mode": "wal",
        "synchronous": 2,
        "foreign_keys": 1,
        "busy_timeout_ms": 5000,
        "wal_autocheckpoint": 1000,
    }
    if values != expected:
        raise AssertionError(f"PRAGMA read-back mismatch: {values!r}")
    return values


def verify_concurrent_uniqueness(database: Path) -> dict[str, int]:
    worker_count = 8
    barrier = threading.Barrier(worker_count)
    results: list[str] = []
    lock = threading.Lock()

    def worker(index: int) -> None:
        connection = connect(database)
        try:
            barrier.wait()
            try:
                connection.execute("BEGIN IMMEDIATE")
                connection.execute(
                    """
                    INSERT INTO decisions(
                        id, node_id, kind, rule_id, canonical_target, state
                    ) VALUES (?, 'node-a', 'Automatic', 'rule-a', '192.0.2.1/32', 'Active')
                    """,
                    (f"decision-{index}",),
                )
                connection.execute("COMMIT")
                outcome = "inserted"
            except sqlite3.IntegrityError:
                connection.execute("ROLLBACK")
                outcome = "duplicate"
            with lock:
                results.append(outcome)
        finally:
            connection.close()

    threads = [threading.Thread(target=worker, args=(index,)) for index in range(worker_count)]
    for thread in threads:
        thread.start()
    for thread in threads:
        thread.join()

    summary = {outcome: results.count(outcome) for outcome in set(results)}
    if summary != {"inserted": 1, "duplicate": worker_count - 1}:
        raise AssertionError(f"unexpected concurrent insert outcomes: {summary!r}")
    return summary


def verify_atomic_rollback(database: Path) -> dict[str, int]:
    connection = connect(database)
    try:
        try:
            connection.execute("BEGIN IMMEDIATE")
            connection.execute(
                """
                INSERT INTO decisions(
                    id, node_id, kind, rule_id, canonical_target, state
                ) VALUES ('manual-rollback', 'node-a', 'Manual', NULL, '198.51.100.7/32', 'Active')
                """
            )
            connection.execute(
                "INSERT INTO desired_projection VALUES ('198.51.100.7/32', 1)"
            )
            connection.execute("INSERT INTO critical_audit(message) VALUES ('')")
            connection.execute("COMMIT")
        except sqlite3.IntegrityError:
            connection.execute("ROLLBACK")

        counts = {
            "decision": connection.execute(
                "SELECT count(*) FROM decisions WHERE id='manual-rollback'"
            ).fetchone()[0],
            "projection": connection.execute(
                "SELECT count(*) FROM desired_projection WHERE canonical_target='198.51.100.7/32'"
            ).fetchone()[0],
        }
        if counts != {"decision": 0, "projection": 0}:
            raise AssertionError(f"transaction did not roll back atomically: {counts!r}")
        return counts
    finally:
        connection.close()


def main() -> int:
    with tempfile.TemporaryDirectory(prefix="guard-m0-sqlite-") as temporary_directory:
        database = Path(temporary_directory) / "spike.db"
        connection = connect(database)
        try:
            pragmas = verify_pragmas(connection)
            connection.executescript(SCHEMA)
        finally:
            connection.close()

        uniqueness = verify_concurrent_uniqueness(database)
        rollback = verify_atomic_rollback(database)

    result = {
        "status": "PASS",
        "platform": platform.platform(),
        "python": platform.python_version(),
        "sqlite": sqlite3.sqlite_version,
        "checks": {
            "pragma_read_back": pragmas,
            "concurrent_partial_unique_index": uniqueness,
            "critical_audit_atomic_rollback": rollback,
        },
        "not_verified": [
            "future Go SQLite driver behavior",
            "cross-process SIGKILL recovery",
            "Linux reboot and filesystem behavior",
            "power-loss durability",
        ],
    }
    print(json.dumps(result, ensure_ascii=False, indent=2, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
