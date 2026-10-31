#!/bin/bash
# ベンチ中に 15 秒ごとの様子を 1 行ずつ出す（Claude Code の Monitor で動かす想定。止めるまで続く）
# 出力: 時刻 | 各アプリサーバーの直近 15 秒のリクエスト数 / 5xx / 403（うち画像）/ CPU idle | DB の CPU idle と I/O 待ち
# 3 台ともリクエストが 0 件の間は何も出さない
while true; do
  line="$(date +%H:%M:%S)"
  for h in asia1 europe1 america1; do
    r=$(ssh -T -o ConnectTimeout=5 isucon2026-$h 'since=$(date -u -d "15 sec ago" +%Y-%m-%dT%H:%M:%S); sudo awk -v s="$since" "/^\\{/ && substr(\$0,10,19)>=s {n++; if(\$0 ~ /\"status\":\"5/)e++; if(\$0 ~ /\"status\":\"403\"/)f++; if(\$0 ~ /\\/api\\/images\\// && \$0 ~ /\"status\":\"403\"/)i++} END{printf \"%d/5xx:%d/403:%d(img:%d)\", n,e,f,i}" /var/log/nginx/access.log; printf " idle:%s" "$(vmstat 1 2 | tail -1 | awk "{print \$15}")"' 2>/dev/null || echo "ssh-fail")
    line="$line | $h $r"
  done
  d=$(ssh -T -o ConnectTimeout=5 isucon2026-asia2 "vmstat 1 2 | tail -1 | awk '{print \"idle:\"\$15\" wa:\"\$16}'" 2>/dev/null || echo "ssh-fail")
  case "$line" in *"asia1 0/"*"europe1 0/"*"america1 0/"*) ;; *) echo "$line | db $d";; esac
  sleep 12
done
