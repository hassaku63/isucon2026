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
# CPU プロファイルの長さ（秒）。ベンチは開始から 2 分半ほどで終わる。0 なら取らない（最後の本番ベンチ用）
PROFILE_SECONDS=${PROFILE_SECONDS:-150}
PGSTAT="sudo -u postgres psql -d isureal -AtF \$'\t' -c \"select relname, seq_scan, seq_tup_read, idx_scan, n_tup_ins, n_tup_upd, n_tup_del from pg_stat_user_tables order by relname\""

# DB の I/O の内訳（開始時と停止時に採って差を見る）。psql -f で流すため、SQL はファイルにして送る
db_snapshot() {
  local name=$1
  ssh -T "$DB" "cat > $REMOTE/snap.sql && sudo -u postgres psql -d isureal -AF \$'\\t' -f $REMOTE/snap.sql > $REMOTE/pg_snap.$name.tsv 2>&1; grep -E '^(MemFree|Cached|Dirty|Writeback|Shmem):' /proc/meminfo > $REMOTE/meminfo.$name.txt" <<'EOS'
\echo '## pg_stat_io'
select backend_type, object, context, reads, round(read_time::numeric,1) as read_ms, writes, round(write_time::numeric,1) as write_ms, extends, hits, evictions, fsyncs from pg_stat_io where reads > 0 or writes > 0 or hits > 0 order by backend_type, object, context;
\echo '## pg_stat_database'
select blks_read, blks_hit, round(blk_read_time::numeric,1) as blk_read_ms, round(blk_write_time::numeric,1) as blk_write_ms, temp_files, temp_bytes, xact_commit, xact_rollback, deadlocks from pg_stat_database where datname = 'isureal';
\echo '## pg_stat_bgwriter'
select checkpoints_timed, checkpoints_req, round(checkpoint_write_time::numeric) as ckpt_write_ms, round(checkpoint_sync_time::numeric) as ckpt_sync_ms, buffers_checkpoint, buffers_clean, buffers_backend, buffers_alloc from pg_stat_bgwriter;
\echo '## pg_statio_user_tables'
select relname, heap_blks_read, heap_blks_hit, idx_blks_read, idx_blks_hit, coalesce(toast_blks_read,0) as toast_blks_read, coalesce(toast_blks_hit,0) as toast_blks_hit from pg_statio_user_tables where heap_blks_read + coalesce(idx_blks_read,0) + coalesce(toast_blks_read,0) > 0 order by heap_blks_read + coalesce(idx_blks_read,0) + coalesce(toast_blks_read,0) desc limit 40;
EOS
}

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
  # DB: ディスクの読み書きの待ち時間（r_await / w_await）と、プロセスごとの I/O
  ssh -T "$DB" "cd $REMOTE && (nohup timeout $MAX iostat -dxmt 1 > iostat.txt 2>&1 &) && (nohup timeout $MAX pidstat -d -h 5 > pidstat-io.txt 2>&1 &)"
  # アプリの CPU プロファイル（pprof、127.0.0.1:6060）。PROFILE_SECONDS=0 なら取らない
  if [ "$PROFILE_SECONDS" -gt 0 ]; then
    for h in $APPS; do
      ssh -T "$h" "cd $REMOTE && (nohup curl -sS -o cpu.pprof 'http://127.0.0.1:6060/debug/pprof/profile?seconds=$PROFILE_SECONDS' > curl-pprof.log 2>&1 &)" &
    done
    wait
  fi
  ssh -T "$DB" "$PGSTAT > $REMOTE/pg_stat_tables.before.tsv"
  db_snapshot before
  ssh -T "$DB" "sudo -u postgres psql -d isureal -Atc \"select pg_stat_statements_reset()\" >/dev/null 2>&1 || echo 'pg_stat_statements is not enabled' >&2"
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
  # CPU プロファイルは途中で止めると壊れるので、取り終わるのを待つ
  for h in $APPS; do
    ssh -T "$h" "for i in \$(seq 1 $PROFILE_SECONDS); do pgrep -u isucon -f 'debug/pprof/profile' >/dev/null || break; sleep 1; done" &
  done
  wait

  db_snapshot after
  for h in $APPS $DB; do
    mkdir -p "$out/$h"
    rsync -a "$h:$REMOTE/" "$out/$h/" --exclude pg_activity.sh --exclude snap.sql &
  done
  for h in $APPS; do
    (
      # JSON 形式（{"time":"2026-10-31T02:02:23+00:00",...）の行だけを、先頭の時刻で切り出す
      ssh -T "$h" "sudo awk -v since=\"\$(date -u -d '$since' '+%Y-%m-%dT%H:%M:%S')\" '/^\\{/ && substr(\$0,10,19)>=since' /var/log/nginx/access.log" > "$out/$h/access.log"
      ssh -T "$h" "sudo awk -v since=\"\$(date -u -d '$since' '+%Y/%m/%d %H:%M:%S')\" 'substr(\$0,1,19)>=since' /var/log/nginx/error.log" > "$out/$h/nginx-error.log"
      ssh -T "$h" "journalctl -u isureal-go --since '$since' --utc --no-pager -q -o short-iso" > "$out/$h/app.log"
      ssh -T "$h" "journalctl -u isureal-guard --since '$since' --utc --no-pager -q -o short-iso" > "$out/$h/guard.log"
    ) &
  done
  (
    ssh -T "$DB" "sudo awk -v since='$since' 'substr(\$0,1,19)>=since' /var/log/postgresql/postgresql-16-main.log" > "$out/$DB/postgresql.log"
    ssh -T "$DB" "$PGSTAT" > "$out/$DB/pg_stat_tables.after.tsv"
    ssh -T "$DB" "sudo -u postgres psql -d isureal -AF \$'\\t' -c \"select calls, round(total_exec_time::numeric,1) as total_ms, round(mean_exec_time::numeric,2) as mean_ms, rows, shared_blks_hit, shared_blks_read, left(regexp_replace(query, '\\s+', ' ', 'g'), 400) as query, round(blk_read_time::numeric,1) as blk_read_ms, round(blk_write_time::numeric,1) as blk_write_ms, temp_blks_written from pg_stat_statements where dbid = (select oid from pg_database where datname='isureal') order by total_exec_time desc limit 100\"" > "$out/$DB/pg_stat_statements.tsv" 2>&1
  ) &
  wait
  rm -f "$STATE"
  python3 -I "$ROOT/scripts/analyze.py" "$out"
  echo "done: $out"
}

case "${1:-}" in
  start) start ;;
  stop) stop ;;
  *) echo "usage: $0 start|stop" >&2; exit 2 ;;
esac
