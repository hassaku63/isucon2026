#!/bin/bash
# ベンチ中のメトリクスとログを全サーバーから集める（サーバーの設定は変えない）
# usage:
#   scripts/bench-monitor.sh start   収集を開始（各サーバーで裏で動かす。最大15分で自動停止）
#   scripts/bench-monitor.sh stop    収集を止めて logs/bench-<開始時刻>/ に回収
set -u
APPS="isucon2026-asia1 isucon2026-europe1 isucon2026-america1"
DB=isucon2026-asia2
ROOT=$(cd "$(dirname "$0")/.." && pwd)
STATE=$ROOT/logs/.bench-monitor.state
REMOTE=/tmp/benchmon
MAX=900
PGSTAT="sudo -u postgres psql -d isureal -AtF \$'\t' -c \"select relname, seq_scan, seq_tup_read, idx_scan, n_tup_ins, n_tup_upd, n_tup_del from pg_stat_user_tables order by relname\""

start() {
  if [ -f "$STATE" ]; then
    echo "already running: $(cat "$STATE")" >&2
    exit 1
  fi
  mkdir -p "$ROOT/logs"
  local ts since
  ts=$(date +%Y%m%d-%H%M%S)
  since=$(date -u '+%Y-%m-%d %H:%M:%S')
  echo "$ts|$since" > "$STATE"

  for h in $APPS $DB; do
    ssh -T "$h" "rm -rf $REMOTE && mkdir -p $REMOTE && cd $REMOTE &&
      (nohup timeout $MAX vmstat -t 1 > vmstat.txt 2>&1 &) &&
      (nohup timeout $MAX pidstat -u -r -h 5 > pidstat.txt 2>&1 &) &&
      (nohup timeout $MAX sar -n DEV 5 > sar-net.txt 2>&1 &)" &
  done
  wait
  ssh -T "$DB" "$PGSTAT > $REMOTE/pg_stat_tables.before.tsv"
  # DB: 1秒ごとに接続の状態と実行中クエリを採る
  ssh -T "$DB" "cd $REMOTE && cat > pg_activity.sh <<'EOS'
end=\$((SECONDS+$MAX))
while [ \$SECONDS -lt \$end ]; do
  sudo -u postgres psql -d isureal -AtF \$'\t' -c \"select now()::time(0), client_addr, state, coalesce(wait_event_type,'-'), coalesce(wait_event,'-'), extract(epoch from now()-query_start)::numeric(8,3), left(regexp_replace(query, '\s+', ' ', 'g'), 200) from pg_stat_activity where datname='isureal' and pid<>pg_backend_pid()\"
  sleep 1
done
EOS
    (nohup bash pg_activity.sh > pg_activity.tsv 2>&1 &)"
  echo "started: $since UTC (out: logs/bench-$ts)"
}

stop() {
  if [ ! -f "$STATE" ]; then
    echo "not running" >&2
    exit 1
  fi
  local ts since out
  IFS='|' read -r ts since < "$STATE"
  out=$ROOT/logs/bench-$ts

  for h in $APPS $DB; do
    ssh -T "$h" "pkill -u isucon -f '^timeout $MAX ' ; pkill -u isucon -f 'bash pg_activity.sh'; true" &
  done
  wait

  for h in $APPS $DB; do
    mkdir -p "$out/$h"
    rsync -a "$h:$REMOTE/" "$out/$h/" --exclude pg_activity.sh &
  done
  for h in $APPS; do
    (
      ssh -T "$h" "sudo awk -v since=\"\$(date -u -d '$since' '+%d/%b/%Y:%H:%M:%S')\" '{t=substr(\$4,2)} t>=since' /var/log/nginx/access.log" > "$out/$h/access.log"
      ssh -T "$h" "sudo awk -v since=\"\$(date -u -d '$since' '+%Y/%m/%d %H:%M:%S')\" 'substr(\$0,1,19)>=since' /var/log/nginx/error.log" > "$out/$h/nginx-error.log"
      ssh -T "$h" "journalctl -u isureal-go --since '$since' --utc --no-pager -q -o short-iso" > "$out/$h/app.log"
      ssh -T "$h" "journalctl -u isureal-guard --since '$since' --utc --no-pager -q -o short-iso" > "$out/$h/guard.log"
    ) &
  done
  (
    ssh -T "$DB" "sudo awk -v since='$since' 'substr(\$0,1,19)>=since' /var/log/postgresql/postgresql-16-main.log" > "$out/$DB/postgresql.log"
    ssh -T "$DB" "$PGSTAT" > "$out/$DB/pg_stat_tables.after.tsv"
  ) &
  wait
  rm -f "$STATE"
  echo "done: $out"
}

case "${1:-}" in
  start) start ;;
  stop) stop ;;
  *) echo "usage: $0 start|stop" >&2; exit 2 ;;
esac
