# ISUCON2026 作業リポジトリ

競技: 2026-10-31 10:00〜18:00 JST。AWS は `AWS_PROFILE=isucon2026`（ap-northeast-1）。

## 最初に読むもの

- [docs/regulation.md](docs/regulation.md) — レギュレーション要約（禁止事項・失格条件）
- [docs/manual.md](docs/manual.md) — 当日マニュアルの要点（レギュレーションより優先）
- [docs/environment.md](docs/environment.md) — インスタンス構成とネットワーク
- [docs/architecture.md](docs/architecture.md) — アプリ構成、ログの場所、ボトルネックの仮説
- [docs/log.md](docs/log.md) — 変更とスコアの記録。ベンチを回したら追記する

## サーバー

ssh: `isucon2026-asia1` / `isucon2026-europe1` / `isucon2026-america1`（アプリ）、`isucon2026-asia2`（PostgreSQL）。ルーター（13.115.174.242）へのログインは禁止。

## 絶対に守ること

- URI、メソッド、ステータス、レスポンス構造、JS/CSS、画像・動画は変更しない
- 書き込みの結果は、リージョンをまたいでもその後の読み取りに反映する。権限・公開範囲・ブロックは即時に反映する
- 再起動後も動き、書き込んだデータが残る状態を保つ。再起動順序が変わっても起動できること
- `isucon-register.service`、`isuadmin` ユーザー、envcheck 関連ファイル、admin アカウントには触らない
- ルーターを迂回する通信、リージョン・遅延・帯域制限を回避する変更はしない
- ベンチ中に、認められたサーバー以外へ処理を委譲しない
- 割り当てられていないサーバー（ベンチマーカー、ルーター）に接続しない
- CloudFormation で作られたリソース（インスタンスタイプ等）は変更しない。SG は新しいポートの追加だけ可（22/443/8080 は触らない）
- 設定を変えたら `sudo /usr/local/bin/envcheck` を実行する
- 問題内容を外部に出さない（Web 検索のクエリに問題固有の情報を含めない）
