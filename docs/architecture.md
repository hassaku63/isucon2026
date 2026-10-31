# アプリケーション構成（初期状態）

10:50 時点の調査結果。コードは `webapp/` に asia1 からコピーしてある（`._*` は macOS のゴミファイルなので除外）。guard のソースは `docs/reference/isureal-guard-src/`（参考用。デプロイ対象ではない）。

## 全体像

```
ベンチマーカー / ブラウザ
   │  X-Country-Code で振り分け、X-Client-IP を付与
   ▼
router 13.115.174.242:8080 / 443（ログイン禁止。内側からは 192.168.10.10 に見える）
   ├─ JP/KR/CN/AU ─▶ asia1    nginx:80 ─▶ isureal-go:8080 ─┐ RTT 1ms
   ├─ GB/DE/FR    ─▶ europe1  nginx:80 ─▶ isureal-go:8080 ─┤ RTT 100ms
   └─ US/CA/MX    ─▶ america1 nginx:80 ─▶ isureal-go:8080 ─┤ RTT 100ms
                                                           ▼
                                   asia2  PostgreSQL 16 :5432（16GB、うち画像が約15GB）
各アプリサーバーで isureal-guard が journal を読み、asia2 の ip_bans に BAN を書き込む
```

## サーバーごとの中身

| 項目 | asia1 / europe1 / america1 | asia2 |
|---|---|---|
| 動いているサービス | nginx、isureal-go、isureal-guard | postgresql@16-main だけ（nginx・アプリ・guard は inactive） |
| `~/env.sh` | `ISUREAL_REGION` = 各リージョン、`ISUREAL_DB_HOST=192.168.4.10` | `ISUREAL_DB_HOST=127.0.0.1` |
| アプリのバイナリ・nginx 設定 | 3台で同一（md5 一致） | - |

- 3台とも同じアプリで、違うのは `ISUREAL_REGION` だけ
- `RegionMiddleware`: セッションの region とサーバーの region が違うと 403 `region_mismatch`
- 管理系 systemd unit（`envcheck*`、`isucon-register`）も各台で enabled。触らない

## ディレクトリ

| パス | 中身 |
|---|---|
| `~/webapp/go/` | Go 実装（echo v5 + sqlx + pgx）。ビルドは `make`（`go build -o isureal cmd/isureal/main.go`）。Go は 1.26.5 |
| `~/webapp/{nodejs,perl,php,python,ruby,rust}/` | 他言語の実装 |
| `~/webapp/public/` | フロントエンド（index.html + assets）。nginx が直接返す |
| `~/webapp/sql/` | `001-schema.sql`、`003-initial-snapshot.sql`、`reset.sql`、`init.sh` |
| `/opt/isureal-guard/{bin,src}` | guard のバイナリと Go ソース（root 所有） |
| `/etc/nginx/sites-enabled/isureal.conf` | `/api/` を 127.0.0.1:8080 へ、それ以外は `public/` から返す |
| `/etc/postgresql/16/main/conf.d/isucon.conf` | `listen_addresses = '*'` だけ |
| `~/local/` | 各言語のランタイム |

## systemd

- `isureal-go.service`: User isucon、`WorkingDirectory=~/webapp/go`、`EnvironmentFile=~/env.sh`、`ExecStart=~/webapp/go/isureal`、`Restart=on-failure`
- `isureal-guard.service`: 環境変数で `GUARD_FAIL_THRESHOLD=10`、`GUARD_FAIL_WINDOW=10s`、`GUARD_BAN_DURATION=2m`

## ログの出力先

| 出所 | 出力先 | 備考 |
|---|---|---|
| アプリ（アクションログ） | stdout → journald（`journalctl -u isureal-go`） | 1リクエスト1行の JSON。`ISUREAL_LOG_LEVEL` の初期値は `debug` で、GET も全部出る |
| アプリ（init.sh の psql 出力） | 同上 | JSON ではない行（`DELETE 0` など）が混ざる |
| guard | journald（`journalctl -u isureal-guard`） | 30秒ごとに `stats:` を出す |
| nginx | `/var/log/nginx/access.log`（combined 形式、**応答時間は記録されない**）、`error.log` | |
| PostgreSQL | `/var/log/postgresql/postgresql-16-main.log` | スロークエリのログは無効（`log_min_duration_statement=-1`）。pg_stat_statements も未導入 |

## DB

- 主なテーブル（件数）: notifications 185万、realmojis 120万、posts 63万、comments 55万、users 5千、friendships 2.5万
- `image_objects` は `created_at` で月ごとにパーティション分割され、**画像の実体を DB に保存**している（約15GB）
- `initial_*` テーブルと `initial_data_boundary` を使って、`reset.sql` で初期状態に戻す
- 設定はほぼ初期値: `shared_buffers=128MB`、`max_connections=100`、`work_mem=4MB`、`synchronous_commit=on`
- アプリ側は `SetMaxOpenConns(30)`。MaxIdle は未設定（Go の初期値 2）
- DSN に `sslmode` の指定がない → pgx の初期値 `prefer` で **SSL 接続になっている**（`pg_stat_ssl` で確認）

## isureal-guard と IP BAN

- アプリのアクションログを `journalctl --follow` で読み、次の条件で IP を BAN する（`ip_bans` に2分の期限を書く）
  - `login` が 401: 10秒に10回
  - `put_profile_image` が 400 かつ `abuse=invalid_image`: 5秒に2回
  - `get_image` が 403 かつ `abuse=invalid_signature`: 5秒に3回
- アプリは `ip_bans` を 200ms ごとに読み直してメモリに持ち、BAN 中の IP には 403 `ip_banned` を返す
- ログレベルを `info` にしても、GET 以外のログと、不正な署名による `get_image` の 403 は出力され続ける（コードがそう作られている）→ guard の判定は維持したまま、GET のログを止められる
- ベンチマーカーはこの攻撃パターンも送ってくる（1回目のベンチで asia1 だけでログイン失敗 629件、不正な署名 218件、BAN 148件）

## 初回ベンチ（10:28〜10:31 JST）で観測したこと

- アクセス件数: nginx で asia1 約3400件、europe1 約400件、america1 約380件
- nginx の error.log に `/api/notifications` の `upstream timed out` が多数。SSE をプロキシするための設定（バッファリング無効、長いタイムアウト）がないため、60秒で切れていると思われる
- `login` が 500 になったものが2件

## ボトルネックの仮説（ベンチで確かめる）

1. **europe1 と america1 から DB まで RTT 100ms**。クエリ1本ごとに 100ms かかる。さらに SSL のハンドシェイクと、アイドル接続を2本しか持たないことによる再接続で、もっと遅くなっているはず
2. 画像も DB から、帯域制限のある経路で取っている
3. SSE（`/api/notifications`）が nginx でタイムアウトしている
4. アクションログ（debug）を全リクエスト分 journald に書いていて、それを guard が全行 JSON パースしている
5. PostgreSQL がほぼ初期設定（shared_buffers 128MB で、16GB の DB に対してキャッシュが小さい）
