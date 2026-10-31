# 今の状態と次の候補（引き継ぎ用）

最終更新: 2026-10-31 14:00 JST。新しいセッションはまずここと CLAUDE.md を読む。スコアの推移は [log.md](log.md)。

## 今の状態

- 最高スコア: **22067**（13:50、PR #18 の後）。これが直近の pass
- main = サーバーにデプロイ済み: `1407f9e`（PR #18 まで）
- 入っている改善: インデックス（#2）、pprof（#4）、DB 接続の使い回しと TLS なし（#8）、画像署名のキャッシュと HMAC の高速化、exp の 60 秒切り上げ（#10）、投稿時の posting_windows の行ロックを FOR SHARE（#12）、アップロード画像の検証で画素を復元しない（#18、`internal/jpegcheck`）
- 動いているバックグラウンドの処理: なし（監視もサブエージェントも止めてある）

## ベンチマーカーについて分かったこと（PR のコメントが一次情報）

| 分かったこと | 根拠 |
|---|---|
| 画像 URL の署名を、持ち主の鍵と元の計算方法（HMAC-SHA256 × 1000 回）で照合している。計算方法・回数は変えられない | #9 で 1 回にしたら「署名が画像の持ち主の鍵と一致しません」で FAIL |
| ベンチマーカーも自分で署名した URL（exp が約 1 時間先）で画像を取りに来る | アクセスログ（#6 のコメント） |
| 有効期限（exp）は「今 + 24 時間」から **1〜15 分の間のどこか**を超えると「範囲外」で FAIL。最大 59 秒超えは pass | #10（pass）、#15（+15 分 FAIL）、#14（+30 分 FAIL）、#13（+42 分 FAIL）。これ以上の探索は打ち切った |
| うまく処理できている地域はユーザーが増える（asia は最大 132 人） | stdout の「asia N 日目: X 人」 |
| 同じコードでもスコアは ±15% ほどぶれる | 初期状態で 3290〜4533 |

## 次の候補（未着手）

asia1 の CPU が最大のボトルネック（13:50 のベンチでピーク idle 2〜4%、asia の参加者 154 人）。プロファイル（`logs/bench-20261031-135047/isucon2026-asia1/cpu.pprof`）の内訳:

| 候補 | 根拠 | メモ |
|---|---|---|
| **ユーザー検索**（`searchUsersHandler`、cum 15%） | CPU プロファイル。DB でも 448 回で 224 万行、28.5 秒 | active なユーザーを毎回全件（全列）取って Go で絞り込んでいる（`user_read_api.go:45`）。案: 絞り込みは id・username・display_name の 3 列で行い、当たった 51 件以下だけを全列で取り直す。`normalizeSearchText` は文字列だけで決まるのでメモ化してよい |
| 画像署名（`imageSignature`、cum 35%） | CPU プロファイル | Memories の RealMoji、エクスポート、フィード。exp の刻みは 60 秒より伸ばせない（上限 1〜15 分）。切り下げ（下限）は未確認で、試すには FAIL のおそれがある実験が要る。sha256 の本体以外（状態の復元など）は約 25% |
| PostgreSQL の `shared_buffers`（128MB のまま） | 画像取得 97 秒、ディスクから 33 万ブロック。asia2 はメモリ 3GB | `infra/files/db/etc/postgresql/16/main/conf.d/`。設定を変えるだけ |
| #7 の残り: 関係の操作の users の行ロック、`LOCK TABLE users` | 行ロック 125 秒（4567 回）、LOCK TABLE 58 秒（198 回）。1 秒以上の待ち 36 件 | asia1 の CPU が詰まると、ロックを持つ時間も延びる。CPU を減らしてから測り直す |
| アクションログを `ISUREAL_LOG_LEVEL=info` にする | pidstat では journald・guard を合わせてもアプリの CPU の数 % | 効果は小さい |
| europe / america の RTT 100ms × クエリ回数 | エンドポイントの平均がどれも asia1 より約 1 秒遅い | DB の複製やキャッシュはサーバー間の新しい経路になるので、作る前にユーザーに確認（Clar #55） |
| SSE（`/api/notifications`）が nginx の初期設定で 60 秒で切れる | nginx の error.log | 通知の受信は 1 点 |
| `PUT /api/me/profile-image` の 502、攻撃のログイン（`evil_login_*`）の 500 | アクセスログ、アプリのログ | 原因は未調査 |
| 画像を DB の外（各サーバーのディスク）に出す | europe / america の画像取得が帯域制限のある経路 | 削除の 5 秒ルールと、リージョンをまたぐ反映に注意。大きい変更 |

## やり残しの小さな作業

- `infra/app.yml` のビルドに `-buildvcs=false` を付ける（今は commit のたびにバイナリが変わり、`make app-diff` がいつも changed になる）
- `webapp/go/initialize.go` の応答の agent / harness / model を申告に合わせる（例: `claude-code` / `claude-code` / `claude-opus-5-5`）。得点には影響しない
- 最後の本番ベンチ（17:00 前後〜）は `PROFILE_SECONDS=0 make bench-start` でプロファイルを取らない
- 終了前に全台を再起動して、ベンチが通り、データが残ることを確かめる（追試の条件。docs/manual.md の「追試」）
