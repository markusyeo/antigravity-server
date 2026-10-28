#!/usr/bin/env python3
"""
Access Log Analyzer for Antigravity Server.

Analyzes request durations, in-flight concurrency, payload sizes, flush delays,
and errors from agy-server access logs.

Usage:
    python3 scripts/analyze_accesslog.py [path/to/access.log]
"""

import collections
import re
import sys


def parse_access_log(path):
    status_counts = collections.Counter()
    conn_events = collections.Counter()
    io_errors = collections.Counter()
    encodings = collections.Counter()

    durations_by_ep = collections.defaultdict(list)
    headers_by_ep = collections.defaultdict(list)
    write_by_ep = collections.defaultdict(list)
    flush_by_ep = collections.defaultdict(list)
    size_by_ep = collections.defaultdict(list)
    upstream_by_ep = collections.defaultdict(list)

    inflight_samples = []
    anomalies = []
    total_lines = 0

    with open(path, "r", encoding="utf-8", errors="replace") as f:
        for line in f:
            total_lines += 1
            line = line.strip()
            if not line:
                continue

            if "CONN OPEN" in line:
                conn_events["CONN OPEN"] += 1
                continue
            if "CONN closed" in line:
                conn_events["CONN closed"] += 1
                continue

            parts = line.split()
            if len(parts) < 6:
                continue

            time_str = parts[0]
            status = parts[1]
            dur_str = parts[2]
            size_str = parts[3]
            method = parts[4]
            endpoint = parts[5]

            status_counts[status] += 1

            # Inflight tracking
            m_inf = re.search(r"inflight=(\d+)", line)
            if m_inf:
                inflight_samples.append((int(m_inf.group(1)), line))

            # IO Errors
            m_err = re.search(r"io_error=\"([^\"]*)\"", line)
            if m_err and m_err.group(1):
                io_errors[m_err.group(1)] += 1
                anomalies.append(line)
            elif status not in ("200", "204", "304", "101", "---"):
                anomalies.append(line)

            # Encodings
            m_enc = re.search(r"encoding=([^\s]*)", line)
            if m_enc:
                encodings[m_enc.group(1)] += 1

            # Upstream duration
            m_up = re.search(r"upstream;dur=([0-9.]+)", line)
            if m_up:
                upstream_by_ep[endpoint].append(float(m_up.group(1)))

            # Numeric metrics
            if status != "---":
                try:
                    dur = float(dur_str.rstrip("s"))
                    size = int(size_str.rstrip("B")) if size_str.endswith("B") else 0
                    durations_by_ep[endpoint].append(dur)
                    size_by_ep[endpoint].append(size)

                    m_h = re.search(r"headers=([0-9.]+)s", line)
                    if m_h:
                        headers_by_ep[endpoint].append(float(m_h.group(1)))
                    m_w = re.search(r"write=([0-9.]+)s", line)
                    if m_w:
                        write_by_ep[endpoint].append(float(m_w.group(1)))
                    m_f = re.search(r"flush=([0-9.]+)s", line)
                    if m_f:
                        flush_by_ep[endpoint].append(float(m_f.group(1)))
                except ValueError:
                    pass

    return {
        "total_lines": total_lines,
        "status_counts": status_counts,
        "conn_events": conn_events,
        "io_errors": io_errors,
        "encodings": encodings,
        "durations_by_ep": durations_by_ep,
        "headers_by_ep": headers_by_ep,
        "write_by_ep": write_by_ep,
        "flush_by_ep": flush_by_ep,
        "size_by_ep": size_by_ep,
        "upstream_by_ep": upstream_by_ep,
        "inflight_samples": inflight_samples,
        "anomalies": anomalies,
    }


def print_report(data):
    print("=" * 70)
    print(" ANTIGRAVITY SERVER ACCESS LOG ANALYSIS")
    print("=" * 70)
    print(f"Total Logged Entries: {data['total_lines']:,}")

    print("\n--- Status Code Distribution ---")
    for s, c in data["status_counts"].most_common():
        pct = (c / data["total_lines"]) * 100
        desc = ""
        if s == "200":
            desc = "(OK)"
        elif s == "---":
            desc = "(Long-Lived Stream >5s OPEN)"
        elif s == "304":
            desc = "(Not Modified - Cached)"
        elif s == "302":
            desc = "(Found / Redirect)"
        elif s == "401":
            desc = "(Unauthorized)"
        print(f"  {s:>4s} {desc:32s}: {c:5d} ({pct:5.1f}%)")

    print("\n--- Connection Lifecycle ---")
    for ev, c in data["conn_events"].most_common():
        print(f"  {ev:20s}: {c}")

    if data["inflight_samples"]:
        data["inflight_samples"].sort(key=lambda x: x[0], reverse=True)
        max_inf = data["inflight_samples"][0][0]
        avg_inf = sum(x[0] for x in data["inflight_samples"]) / len(data["inflight_samples"])
        print(f"  Peak In-Flight Concurrency: {max_inf}")
        print(f"  Average In-Flight Concurrency: {avg_inf:.1f}")

    print("\n--- Payload Encodings ---")
    for enc, c in data["encodings"].most_common():
        label = enc if enc else "identity / none"
        print(f"  {label:20s}: {c:5d}")

    print("\n--- Top Endpoints by Call Frequency ---")
    counts = [(ep, len(d)) for ep, d in data["durations_by_ep"].items()]
    counts.sort(key=lambda x: x[1], reverse=True)
    print(f"  {'Endpoint':38s} {'Count':>6s} {'Avg Dur':>9s} {'P95 Dur':>9s}")
    print(f"  {'-'*38} {'-'*6} {'-'*9} {'-'*9}")
    for ep, c in counts[:15]:
        durs = data["durations_by_ep"][ep]
        avg_d = sum(durs) / c
        p95_d = sorted(durs)[int(c * 0.95)]
        print(f"  {ep:38s} {c:6d} {avg_d:8.3f}s {p95_d:8.3f}s")

    print("\n--- Largest Average Payload Sizes ---")
    avg_sizes = [(ep, sum(s) / len(s), max(s), len(s)) for ep, s in data["size_by_ep"].items() if s]
    avg_sizes.sort(key=lambda x: x[1], reverse=True)
    print(f"  {'Endpoint':38s} {'Avg Size':>10s} {'Max Size':>10s} {'Count':>6s}")
    print(f"  {'-'*38} {'-'*10} {'-'*10} {'-'*6}")
    for ep, avg, mx, c in avg_sizes[:10]:
        print(f"  {ep:38s} {avg/1024:9.1f}KB {mx/1024:9.1f}KB {c:6d}")

    print("\n--- Top Endpoints by Average Flush Latency ---")
    avg_flushes = [(ep, sum(fl) / len(fl), max(fl), len(fl)) for ep, fl in data["flush_by_ep"].items() if fl]
    avg_flushes.sort(key=lambda x: x[1], reverse=True)
    print(f"  {'Endpoint':38s} {'Avg Flush':>10s} {'Max Flush':>10s} {'Count':>6s}")
    print(f"  {'-'*38} {'-'*10} {'-'*10} {'-'*6}")
    for ep, avg, mx, c in avg_flushes[:8]:
        print(f"  {ep:38s} {avg*1000:9.1f}ms {mx*1000:9.1f}ms {c:6d}")

    print("\n--- Top Endpoints by Upstream Execution Duration ---")
    up_stats = [(ep, sum(u) / len(u), max(u), len(u)) for ep, u in data["upstream_by_ep"].items() if u]
    up_stats.sort(key=lambda x: x[1], reverse=True)
    print(f"  {'Endpoint':38s} {'Avg Upstream':>12s} {'Max Upstream':>12s} {'Count':>6s}")
    print(f"  {'-'*38} {'-'*12} {'-'*12} {'-'*6}")
    for ep, avg, mx, c in up_stats[:8]:
        print(f"  {ep:38s} {avg:11.1f}ms {mx:11.1f}ms {c:6d}")

    if data["io_errors"]:
        print("\n--- IO Errors Observed ---")
        for err, c in data["io_errors"].most_common():
            print(f"  {c:3d}x: {err}")

    if data["anomalies"]:
        print(f"\n--- Non-Standard / Error Requests ({len(data['anomalies'])}) ---")
        for a in data["anomalies"][:10]:
            print(f"  {a}")
        if len(data["anomalies"]) > 10:
            print(f"  ... and {len(data['anomalies']) - 10} more")

    print("=" * 70)


def main():
    log_path = sys.argv[1] if len(sys.argv) > 1 else "access.log"
    data = parse_access_log(log_path)
    print_report(data)


if __name__ == "__main__":
    main()
