#!/usr/bin/env python3
"""2 回のベンチの summary.md から、エンドポイントごとの件数と応答時間を並べる。画像 URL の exp も分類する

usage: scripts/compare.py logs/bench-<前> logs/bench-<後>
"""
import collections
import datetime
import json
import re
import sys
import urllib.parse
from pathlib import Path

APPS = ["isucon2026-asia1", "isucon2026-europe1", "isucon2026-america1"]
ENDPOINTS = ["POST /api/posts", "GET /api/feeds/friends", "GET /api/memories", "GET /api/images/:id", "POST /api/auth/login"]


def endpoint_table(summary, host):
    sections = summary.split(f"## nginx: {host}")
    if len(sections) < 2:
        return {}
    rows = {}
    for line in sections[1].split("\n## ")[0].splitlines():
        m = re.match(r"\| `([^`]+)` \| (\d+) \| [\d.]+ \| ([\d.]+) \| ([\d.]+) \|", line)
        if m:
            rows[m.group(1)] = (int(m.group(2)), float(m.group(3)), float(m.group(4)))
    return rows


def image_exp_kinds(bench):
    """画像取得を exp の種類（アプリ発行 = 約 24 時間先、ベンチマーカー作成 = 約 1 時間先）とステータスで数える"""
    counts = collections.Counter()
    for host in APPS:
        path = bench / host / "access.log"
        if not path.exists():
            continue
        for line in path.read_text(errors="replace").splitlines():
            try:
                entry = json.loads(line)
            except json.JSONDecodeError:
                continue
            if not entry["uri"].startswith("/api/images/"):
                continue
            exp = urllib.parse.parse_qs(urllib.parse.urlsplit(entry["uri"]).query).get("exp", ["0"])[0]
            delta = int(exp) - datetime.datetime.fromisoformat(entry["time"]).timestamp() if exp.isdigit() else 0
            kind = "アプリ発行（約24時間）" if delta > 20 * 3600 else ("ベンチマーカー作成（約1時間）" if 1800 < delta < 2 * 3600 else "その他")
            counts[(kind, entry["status"])] += 1
    return counts


def main():
    before, after = Path(sys.argv[1]), Path(sys.argv[2])
    sb, sa = (before / "summary.md").read_text(), (after / "summary.md").read_text()
    print("| サーバー | エンドポイント | 件数 | 平均(秒) | p99(秒) |")
    print("|---|---|---|---|---|")
    for host in APPS:
        b, a = endpoint_table(sb, host), endpoint_table(sa, host)
        for key in ENDPOINTS:
            if key in b and key in a:
                print(f"| {host.removeprefix('isucon2026-')} | `{key}` | {b[key][0]} → {a[key][0]} | {b[key][1]:.3f} → {a[key][1]:.3f} | {b[key][2]:.3f} → {a[key][2]:.3f} |")
    print()
    print("画像取得の exp の内訳（後のベンチ）:")
    for (kind, status), n in sorted(image_exp_kinds(after).items()):
        print(f"- {kind} / {status}: {n}")


if __name__ == "__main__":
    main()
