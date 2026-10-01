#!/usr/bin/env python3
"""Summarize AGY_DEBUG conversation timings without displaying message contents."""
import argparse
import json
import math
from pathlib import Path


def read_records(path, phone=False):
    records = []
    for line in path.read_text().splitlines():
        try:
            row = json.loads(line)
        except ValueError:
            continue
        if not isinstance(row, dict) or row.get("kind") != "conversation-load":
            continue
        if not isinstance(row.get("at"), (int, float)) or not math.isfinite(row["at"]):
            continue
        if not isinstance(row.get("session"), str) or not row["session"] or not isinstance(row.get("event"), str):
            continue
        if row.get("conversation") is not None and not isinstance(row["conversation"], str):
            continue
        if "elapsed" in row and (not isinstance(row["elapsed"], (int, float)) or not math.isfinite(row["elapsed"])):
            continue
        ua = row.get("ua") or ""
        if phone and (not isinstance(ua, str) or not any(word in ua for word in ("iPhone", "iPad", "Android"))):
            continue
        records.append(row)
    return records


def attempts(records):
    sessions = {}
    for row in records:
        sessions.setdefault(row["session"], []).append(row)
    result = []
    for session, rows in sessions.items():
        rows.sort(key=lambda row: row["at"])
        providers = {}
        streams = {}
        for row in rows:
            conversation = row.get("conversation")
            if not conversation:
                continue
            key = (session, conversation)
            event = row.get("event")
            if event == "provider-created":
                load = {"session": session, "conversation": conversation,
                        "start": row["at"] - row.get("elapsed", 0), "time": row.get("time", "?"),
                        "events": {event: row}, "streams": []}
                later_click = next((click for click in rows if click.get("event") == "thread-click"
                                   and click["at"] > load["start"] and click.get("conversation") != conversation), None)
                load["left_ms"] = later_click["at"] - load["start"] if later_click else None
                providers.setdefault(key, []).append(load)
                result.append(load)
            elif event == "stream-request":
                stream = {"start": row["at"], "events": {event: row}}
                streams.setdefault(key, []).append(stream)
                if providers.get(key):
                    providers[key][-1]["streams"].append(stream)
            elif event.startswith("stream-"):
                start = row["at"] - row.get("elapsed", 0)
                candidates = streams.get(key, [])
                if candidates:
                    stream = min(candidates, key=lambda stream: abs(stream["start"] - start))
                    if abs(stream["start"] - start) <= 3:
                        stream["events"][event] = row
            elif event in ("first-state", "messages-mounted", "messages-painted", "still-waiting", "load-abandoned"):
                start = row["at"] - row.get("elapsed", 0)
                candidates = providers.get(key, [])
                if candidates:
                    load = min(candidates, key=lambda load: abs(load["start"] - start))
                    if abs(load["start"] - start) <= 3:
                        load["events"][event] = row
    return result


def summary(load):
    events = load["events"]
    painted = events.get("messages-painted")
    abandoned = events.get("load-abandoned")
    left = load.get("left_ms")
    painted_while_open = painted and (left is None or painted["elapsed"] <= left)
    outcome = "painted" if painted_while_open else "left-before-paint" if left is not None else "abandoned" if abandoned else "no-paint-record"
    elapsed = painted["elapsed"] if painted_while_open else None
    end = painted["at"] if painted_while_open else load["start"] + left if left is not None else float("inf")
    observed = list(events.values()) + [row for stream in load["streams"] for row in stream["events"].values()]
    visibility = [row["visibility"] for row in observed
                  if load["start"] <= row["at"] <= end and row.get("visibility")]
    return {"time": load["time"], "session": load["session"], "conversation": load["conversation"],
            "outcome": outcome, "elapsed_ms": elapsed,
            "start_event": "thread-click" if events["provider-created"].get("elapsed", 0) > 0 else "provider-created",
            "state_ms": events.get("first-state", {}).get("elapsed"),
            "mounted_ms": events.get("messages-mounted", {}).get("elapsed"),
            "time_until_switch_ms": left,
            "render_ms": painted.get("render") if painted else None,
            "background_during_load": "hidden" in visibility if visibility else None,
            "visibility_at_mount": events.get("messages-mounted", {}).get("visibility"),
            "visibility_at_paint": painted.get("visibility") if painted else None,
            "streams": [{"headers_ms": stream["events"].get("stream-headers", {}).get("elapsed"),
                         "frame_ms": stream["events"].get("stream-first-frame", {}).get("elapsed"),
                         "request_id": stream["events"].get("stream-headers", {}).get("requestId"),
                         "error": next((stream["events"][event].get("error") for event in
                                        ("stream-error", "stream-read-error", "stream-cancelled") if event in stream["events"]), None)}
                        for stream in load["streams"]]}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("path", nargs="?", type=Path, default=Path.home() / ".agy-remote/mobile-debug.log")
    parser.add_argument("--phone", action="store_true", help="Only include mobile browsers")
    parser.add_argument("--summary", action="store_true", help="Report each provider load separately, including failed loads and reconnects")
    args = parser.parse_args()
    records = read_records(args.path, args.phone)
    if args.summary:
        for load in attempts(records):
            print(json.dumps(summary(load)))
        return
    groups = {}
    for row in records:
        groups.setdefault((row["session"], row.get("conversation")), []).append(row)
    for (session, conversation), rows in groups.items():
        rows.sort(key=lambda row: row["at"])
        ua = rows[0].get("ua", "")
        device = "phone" if any(word in ua for word in ("iPhone", "iPad", "Android")) else "desktop"
        print(f"{rows[0].get('time', '?')} {device} session={session} conversation={conversation or 'startup'}")
        for row in rows:
            fields = " ".join(f"{key}={row[key]}" for key in ("elapsed", "render", "bytes", "blocked", "receivedState", "requestId") if key in row)
            print(f"  {row.get('event', '?'):<26} at={row['at']:>6}ms {fields}")


if __name__ == "__main__":
    main()
