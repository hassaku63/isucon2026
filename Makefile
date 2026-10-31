# HOSTS=isucon2026-europe1 のように指定すると、そのサーバーだけに絞る
HOSTS ?= all
ANSIBLE_PLAYBOOK = cd infra && uvx --from ansible-core ansible-playbook --limit '$(HOSTS)'

.PHONY: infra-fetch infra-diff infra-apply app-diff app-deploy bench-start bench-stop

# サーバー上の設定ファイルを infra/fetched/ に取り込む（サーバーは変更しない）
infra-fetch:
	$(ANSIBLE_PLAYBOOK) fetch.yml

# infra/files/ とサーバーの差分を表示する（サーバーは変更しない）
infra-diff:
	$(ANSIBLE_PLAYBOOK) infra.yml --check --diff

# 設定ファイルを適用し、変わったものだけ reload / restart する。最後に envcheck を実行する
infra-apply:
	$(ANSIBLE_PLAYBOOK) infra.yml --diff

# アプリをビルドして、配る内容がサーバーとどう違うかを表示する（サーバーは変更しない）
app-diff:
	$(ANSIBLE_PLAYBOOK) app.yml --check --diff

# アプリをビルドして配り、変わったら isureal-go を再起動する
app-deploy:
	$(ANSIBLE_PLAYBOOK) app.yml --diff

bench-start:
	scripts/bench-monitor.sh start

bench-stop:
	scripts/bench-monitor.sh stop
