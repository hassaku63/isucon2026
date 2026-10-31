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
- サーバー間の通信のためにルーターの IP（443/8080）を宛先にしない（Clar #55）。サーバー間で新しい通信経路を作るとき（キャッシュの共有、複製など）は、ルーターを迂回・回避しないか、事前にユーザーに確認する
- ベンチ中に、認められたサーバー以外へ処理を委譲しない
- 割り当てられていないサーバー（ベンチマーカー、ルーター）に接続しない
- CloudFormation で作られたリソース（インスタンスタイプ等）は変更しない。SG は新しいポートの追加だけ可（22/443/8080 は触らない）
- 設定を変えたら `sudo /usr/local/bin/envcheck` を実行する
- 問題内容を外部に出さない（Web 検索のクエリに問題固有の情報を含めない）

## 変更の手順

サーバー上で直接ファイルを編集しない。リポジトリを変えて、次のコマンドで反映する（`HOSTS=isucon2026-europe1` で対象を絞れる）。

| 対象 | 置き場所 | 差分の確認（サーバーは変えない） | 適用 |
|---|---|---|---|
| ミドルウェアの設定（nginx、PostgreSQL、systemd、env.sh） | `infra/files/{app,db,<ホスト名>}/<実際のパス>` | `make infra-diff` | `make infra-apply`（変わったものだけ reload / restart し、最後に envcheck を実行） |
| アプリ（Go、sql） | `webapp/` | `make app-diff` | `make app-deploy`（手元でビルドして3台に配り、再起動） |

- 管理する設定ファイルを増やすときは `infra/group_vars/{app,db}.yml` の `managed_files` に追加し、`make infra-fetch` で今の中身を `infra/fetched/` に取り込んでから `infra/files/` に置く
- 3台で同じ設定は `files/app/`、サーバーごとに違うものは `files/<ホスト名>/` に置く（ホスト名の方が優先）
- 変更禁止のファイルは `infra/group_vars/all.yml` の `forbidden_paths` に入っていて、当たると infra.yml が止まる
- ベンチの計測: `make bench-start` / `make bench-stop`

## 進め方（PR とベンチ）

- 実質的な変更は、issue（`.github/ISSUE_TEMPLATE/bottleneck.md` の形式）→ ブランチ → PR → マージ → デプロイの順で進める
- マージしてデプロイしたあとのベンチ結果は、**その PR にコメントで追記する**
  - ユーザーが貼るベンチマーカーの stdout を、**手を加えずにそのまま**コードブロックで載せる
  - PR 本文の「効果の確かめ方」の項目ごとに、前後の数値と判定を書く
  - 次の issue / PR につながる観測を書く
- ベンチの記録やドキュメントの更新（`docs/`）は main に直接 commit してよい
- issue / PR では、呼び出し関係（pprof）、サーバー間の往復、ロックの待ち合いが論点のときだけ mermaid で図を描く（sequenceDiagram / flowchart）。数字は図の中に書く
