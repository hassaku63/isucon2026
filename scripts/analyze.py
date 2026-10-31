#!/usr/bin/env python3
"""bench-monitor.sh で集めたログを要約して <bench dir>/summary.md に書く

usage: scripts/analyze.py logs/bench-YYYYmmdd-HHMMSS
"""
import collections
import json
import re
import sys
from pathlib import Path

APPS = ["isucon2026-asia1", "isucon2026-europe1", "isucon2026-america1"]
DB = "isucon2026-asia2"

# /api/posts/<uuid>/comments → /api/posts/:id/comments のようにまとめる
ID_PATTERNS = [
    (re.compile(r"/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}"), "/:id"),
    (re.compile(r"/[0-9A-Za-z_-]{20,}"), "/:id"),
    (re.compile(r"/\d{4}-\d{2}-\d{2}"), "/:date"),
    (re.compile(r"/\d+(?=/|$)"), "/:n"),
]
FRIEND_REQUEST_ACTION = re.compile(r"(/api/friend-requests/:id)/(accept|reject|cancel)")


def normalize(uri):
    path = uri.split("?", 1)[0]
    for pattern, repl in ID_PATTERNS:
        path = pattern.sub(repl, path)
    return FRIEND_REQUEST_ACTION.sub(r"\1/:action", path)


def percentile(values, p):
    if not values:
        return 0.0
    values = sorted(values)
    return values[min(len(values) - 1, int(len(values) * p))]


def access_summary(path):
    stats = collections.defaultdict(lambda: {"times": [], "status": collections.Counter(), "bytes": 0})
    for line in path.read_text(errors="replace").splitlines():
        try:
            entry = json.loads(line)
        except json.JSONDecodeError:
            continue
        key = f'{entry["method"]} {normalize(entry["uri"])}'
        stat = stats[key]
        stat["times"].append(float(entry["response_time"] or 0))
        stat["status"][entry["status"]] += 1
        stat["bytes"] += int(entry["body_bytes"] or 0)
    rows = []
    for key, stat in stats.items():
        times = stat["times"]
        rows.append((sum(times), key, len(times), sum(times) / len(times), percentile(times, 0.99), max(times),
                     stat["bytes"], stat["status"]))
    rows.sort(reverse=True)
    lines = ["| endpoint | count | total(s) | avg(s) | p99(s) | max(s) | bytes | status |", "|---|---:|---:|---:|---:|---:|---:|---|"]
    for total, key, count, avg, p99, mx, nbytes, status in rows[:30]:
        status_text = " ".join(f"{k}:{v}" for k, v in sorted(status.items()))
        lines.append(f"| `{key}` | {count} | {total:.1f} | {avg:.3f} | {p99:.3f} | {mx:.3f} | {nbytes} | {status_text} |")
    return lines


def cpu_summary(path):
    """vmstat -t 1 から、CPU が使われていた区間の平均を出す"""
    samples = []
    for line in path.read_text(errors="replace").splitlines():
        cols = line.split()
        if len(cols) >= 17 and cols[0].isdigit():
            samples.append({"us": int(cols[12]), "sy": int(cols[13]), "id": int(cols[14]), "wa": int(cols[15])})
    busy = [s for s in samples if s["id"] < 90]
    if not busy:
        return f"ほぼ idle（{len(samples)} 秒）"
    avg = {k: sum(s[k] for s in busy) / len(busy) for k in ("us", "sy", "id", "wa")}
    return (f"負荷のあった {len(busy)}/{len(samples)} 秒の平均: us {avg['us']:.0f}% / sy {avg['sy']:.0f}% / "
            f"idle {avg['id']:.0f}% / wa {avg['wa']:.0f}%、idle 0% の秒数 {sum(1 for s in busy if s['id'] == 0)}")


def table_scan_summary(before, after):
    def load(path):
        rows = {}
        for line in path.read_text().splitlines():
            cols = line.split("\t")
            if len(cols) == 7:
                rows[cols[0]] = [int(c or 0) for c in cols[1:]]
        return rows
    b, a = load(before), load(after)
    diffs = []
    for name, cols in a.items():
        prev = b.get(name, [0] * 6)
        d = [x - y for x, y in zip(cols, prev)]
        if any(d):
            diffs.append((d[1], name, d))
    diffs.sort(reverse=True)
    lines = ["| table | seq_scan | seq_tup_read | idx_scan | ins | upd | del |", "|---|---:|---:|---:|---:|---:|---:|"]
    for _, name, d in diffs[:15]:
        lines.append(f"| {name} | " + " | ".join(str(x) for x in d) + " |")
    return lines


def statements_summary(path):
    text = path.read_text(errors="replace").splitlines()
    if not text or not text[0].startswith("calls"):
        return ["（pg_stat_statements なし）"]
    lines = ["| calls | total(ms) | mean(ms) | rows | blks_read | query |", "|---:|---:|---:|---:|---:|---|"]
    for row in text[1:21]:
        cols = row.split("\t")
        if len(cols) < 7:
            continue
        calls, total, mean, nrows, _hit, read, query = cols[:7]
        query = query.replace("|", "\\|")[:200]
        lines.append(f"| {calls} | {total} | {mean} | {nrows} | {read} | `{query}` |")
    return lines


def main():
    bench = Path(sys.argv[1])
    out = ["# ベンチ要約: " + bench.name, ""]

    out += ["## CPU", ""]
    for host in APPS + [DB]:
        vmstat = bench / host / "vmstat.txt"
        if vmstat.exists():
            out.append(f"- {host}: {cpu_summary(vmstat)}")
    out.append("")

    for host in APPS:
        access = bench / host / "access.log"
        if access.exists():
            out += [f"## nginx: {host}（合計時間の降順）", ""] + access_summary(access) + [""]

    statements = bench / DB / "pg_stat_statements.tsv"
    if statements.exists():
        out += ["## pg_stat_statements（合計時間の上位 20）", ""] + statements_summary(statements) + [""]

    before, after = bench / DB / "pg_stat_tables.before.tsv", bench / DB / "pg_stat_tables.after.tsv"
    if before.exists() and after.exists():
        out += ["## テーブルごとのスキャン（ベンチ前後の差）", ""] + table_scan_summary(before, after) + [""]

    pglog = bench / DB / "postgresql.log"
    if pglog.exists():
        log = pglog.read_text(errors="replace")
        out += ["## PostgreSQL ログ", "",
                f"- 100ms 以上のクエリ: {log.count('duration:')} 件",
                f"- ロック待ち（1秒以上）: {log.count('still waiting for')} 件",
                f"- デッドロック: {log.count('deadlock detected')} 件", ""]

    (bench / "summary.md").write_text("\n".join(out) + "\n")
    print(bench / "summary.md")


if __name__ == "__main__":
    main()
