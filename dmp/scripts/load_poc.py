#!/usr/bin/env python3
"""DMP load / physical-deletion POC (I4-07, D-10).

Seeds a snapshot through the real import API, publishes it, measures audience
resolution latency under concurrency, then measures how long a scoped snapshot
deletion takes and proves the serving snapshot is unaffected.

Usage:
    python3 dmp/scripts/load_poc.py [--rows 100000] [--requests 600] [--concurrency 20]
"""
from __future__ import annotations

import argparse
import hashlib
import http.client
import json
import os
import pathlib
import random
import statistics
import sys
import threading
import time
import urllib.error
import urllib.request

BASE = "http://127.0.0.1:8082/internal/v1"
TOKEN = "dmp-internal-token-0123456789abcdef"
NAMESPACE = "publisher-account-v1"
PROVIDER = "dmp-provider-01"
# The sandbox tenant alias; resolution must always name a tenant.
TENANT = os.environ.get("DMP_TENANT_ID", "tenant-default")
SEGMENT = "seg-tech-enthusiasts"
RESOLVE_P95_TARGET_MS = 50.0
DELETE_TARGET_SECONDS = 60.0


def call(method: str, path: str, body: dict | None = None) -> tuple[int, dict]:
    data = json.dumps(body).encode() if body is not None else None
    request = urllib.request.Request(BASE + path, data=data, method=method)
    request.add_header("Content-Type", "application/json")
    request.add_header("Authorization", "Bearer " + TOKEN)
    try:
        with urllib.request.urlopen(request, timeout=120) as response:
            return response.status, json.loads(response.read() or b"{}")
    except urllib.error.HTTPError as err:
        raw = err.read()
        try:
            return err.code, json.loads(raw or b"{}")
        except json.JSONDecodeError:
            return err.code, {"raw": raw.decode()}


def token_for(index: int) -> str:
    return f"{index:016x}".rjust(64, "0")


def seed_snapshot(rows: int, source_version: str) -> tuple[str, float, float]:
    """Import and verify a snapshot, returning its id and the two timings."""
    tokens = [token_for(i) for i in range(rows)]
    checksum = hashlib.sha256(
        "".join(f"{token}\t1\t{SEGMENT}\n" for token in sorted(tokens)).encode()
    ).hexdigest()
    status, job = call("POST", "/snapshots", {
        "provider_id": PROVIDER, "namespace": NAMESPACE, "schema_version": 1,
        "source_version": source_version, "expected_rows": rows, "expected_checksum": checksum,
    })
    if status != 201:
        raise SystemExit(f"register snapshot failed: {status} {job}")
    snapshot_id = job["snapshot_id"]

    batch = 1000
    started = time.perf_counter()
    for start in range(0, rows, batch):
        chunk = [{"subject_token": token, "subject_key_version": 1, "segment_ids": [SEGMENT]}
                 for token in tokens[start:start + batch]]
        status, result = call("POST", f"/snapshots/{snapshot_id}/rows", {"rows": chunk})
        if status != 200:
            raise SystemExit(f"append rows failed: {status} {result}")
    imported = time.perf_counter() - started

    started = time.perf_counter()
    status, verified = call("POST", f"/snapshots/{snapshot_id}/verify")
    verify_seconds = time.perf_counter() - started
    if status != 200:
        raise SystemExit(f"verify failed: {status} {verified}")
    print(f"  import: {rows} rows in {imported:.2f}s ({rows / imported:,.0f} rows/s), "
          f"verify {verify_seconds:.2f}s, checksum ok")
    return snapshot_id, imported, verify_seconds


def resolve_latencies(tokens: list[str], requests: int, concurrency: int) -> tuple[list[float], list[int]]:
    latencies: list[float] = []
    statuses: list[int] = []
    lock = threading.Lock()

    def worker(count: int) -> None:
        connection = http.client.HTTPConnection("127.0.0.1", 8082, timeout=10)
        local: list[float] = []
        local_status: list[int] = []
        for _ in range(count):
            token = random.choice(tokens)
            payload = json.dumps({"tenant_id": TENANT, "namespace": NAMESPACE, "provider_id": PROVIDER, "subject_token": token})
            started = time.perf_counter()
            try:
                connection.request("POST", "/internal/v1/audiences:resolve", payload,
                                   {"Content-Type": "application/json"})
                response = connection.getresponse()
                response.read()
                local_status.append(response.status)
            except Exception:
                local_status.append(0)
                connection.close()
                connection = http.client.HTTPConnection("127.0.0.1", 8082, timeout=10)
            local.append((time.perf_counter() - started) * 1000)
        connection.close()
        with lock:
            latencies.extend(local)
            statuses.extend(local_status)

    per_worker = max(requests // concurrency, 1)
    threads = [threading.Thread(target=worker, args=(per_worker,)) for _ in range(concurrency)]
    started = time.perf_counter()
    for thread in threads:
        thread.start()
    for thread in threads:
        thread.join()
    elapsed = time.perf_counter() - started
    print(f"  resolve: {len(latencies)} requests in {elapsed:.2f}s "
          f"({len(latencies) / elapsed:,.0f} req/s), errors={sum(1 for s in statuses if s != 200)}")
    return latencies, statuses


def write_report(args: argparse.Namespace, report: dict) -> str:
    """Archive the run so a later topology re-test can be compared to it."""
    if args.report:
        path = pathlib.Path(args.report)
    else:
        stamp = time.strftime("%Y-%m-%d", time.gmtime())
        path = pathlib.Path(__file__).resolve().parents[2] / "docs" / "plan" / "iteration-4" / "poc" / f"{stamp}-{args.label}.json"
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(report, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
    print(f"  report archived to {path}")
    return str(path)


def percentile(values: list[float], fraction: float) -> float:
    if not values:
        return 0.0
    ordered = sorted(values)
    index = min(len(ordered) - 1, int(round(fraction * (len(ordered) - 1))))
    return ordered[index]


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--rows", type=int, default=100_000)
    parser.add_argument("--requests", type=int, default=600)
    parser.add_argument("--concurrency", type=int, default=20)
    parser.add_argument("--label", default="sandbox-single-node",
                        help="topology label written into the archived report")
    parser.add_argument("--report", default="",
                        help="path of the JSON report to archive (default: docs/plan/iteration-4/poc/<date>-<label>.json)")
    args = parser.parse_args()

    run = str(int(time.time()))[-6:]
    print(f"DMP load POC: rows={args.rows} requests={args.requests} concurrency={args.concurrency}")
    serving, import_seconds, verify_seconds = seed_snapshot(args.rows, f"poc-serving-{run}")
    print(f"  publish serving snapshot: {call('POST', f'/snapshots/{serving}/publish')[0]}")

    tokens = [token_for(i) for i in range(min(args.rows, 5000))]
    latencies, statuses = resolve_latencies(tokens, args.requests, args.concurrency)
    p50, p95, p99 = percentile(latencies, 0.50), percentile(latencies, 0.95), percentile(latencies, 0.99)
    print(f"  latency: p50={p50:.2f}ms p95={p95:.2f}ms p99={p99:.2f}ms max={max(latencies):.2f}ms")

    doomed, _, _ = seed_snapshot(args.rows, f"poc-doomed-{run}")
    started = time.perf_counter()
    status, deleted = call("POST", f"/snapshots/{doomed}/delete")
    delete_seconds = time.perf_counter() - started
    print(f"  delete {args.rows} rows: status={status} rows={deleted.get('deleted_rows')} in {delete_seconds:.2f}s")

    status, resolved = call("POST", "/audiences:resolve", {
        "tenant_id": TENANT, "namespace": NAMESPACE, "provider_id": PROVIDER, "subject_token": token_for(0)})
    served = len(resolved.get("segments") or [])
    print(f"  serving snapshot still resolves after the deletion: status={status} segments={served}")

    report = {
        "recorded_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "topology": args.label,
        "endpoint": BASE,
        "rows": args.rows, "requests": len(latencies), "concurrency": args.concurrency,
        "imported_rows_per_second": round(args.rows / max(import_seconds, 1e-9)),
        "p50_ms": round(p50, 2), "p95_ms": round(p95, 2), "p99_ms": round(p99, 2),
        "errors": sum(1 for s in statuses if s != 200),
        "import_seconds": round(import_seconds, 2),
        "verify_seconds": round(verify_seconds, 2),
        "delete_seconds": round(delete_seconds, 2),
        "serving_segments_after_delete": served,
        "p95_target_ms": RESOLVE_P95_TARGET_MS, "delete_target_seconds": DELETE_TARGET_SECONDS,
    }
    print(json.dumps(report, indent=2))
    write_report(args, report)

    failures = []
    if report["errors"]:
        failures.append(f"{report['errors']} resolve requests failed")
    if p95 > RESOLVE_P95_TARGET_MS:
        failures.append(f"p95 {p95:.2f}ms exceeds {RESOLVE_P95_TARGET_MS}ms")
    if delete_seconds > DELETE_TARGET_SECONDS:
        failures.append(f"delete {delete_seconds:.2f}s exceeds {DELETE_TARGET_SECONDS}s")
    if served != 1:
        failures.append("serving snapshot lost data during the deletion")
    if failures:
        print("POC FAILED: " + "; ".join(failures))
        return 1
    print("POC PASSED")
    return 0


if __name__ == "__main__":
    sys.exit(main())
