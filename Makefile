.PHONY: help kek kek-migrate build web web-dev test test-race test-short test-all vet fmt up down logs \
	backup backup-verify backup-list restore-drill drill-log \
	kek-rotate kek-verify import cache-prune cache-clear \
	android apk android-test android-clean \
	admin-login backup-now health-check \
	accept demo-search demo-tts demo-lifecycle clean

SHELL := /bin/sh

# 依赖只读：go build/test/vet 绝不隐式改 go.mod / go.sum。
# 需要升级依赖时显式 `GOFLAGS= go get ...` 或 `GOFLAGS= go mod tidy`，再提交变更。
GOFLAGS ?= -mod=readonly
export GOFLAGS

# ---- 可覆盖变量（make backup PACKAGE=... 等）----
COMPOSE    ?= docker compose
BIN        ?= bin/novelstack
DATA_DIR   ?= ./data
SECRETS    ?= ./secrets
PASS_FILE  ?= $(SECRETS)/backup.pass
KEK_V1     ?= $(SECRETS)/kek.v1
KEK_V2     ?= $(SECRETS)/kek.v2
# 当前生效的 KEK 文件（轮换完成后改成 $(KEK_V2)）
KEK_CURRENT ?= $(KEK_V1)
# 容器内对应路径：compose secret 名为 /run/secrets/kek_v1 | kek_v2（下划线）
KEK_CURRENT_SECRET ?= /run/secrets/kek_v1
BACKUP_DIR ?= $(DATA_DIR)/backups
IMPORT_DIR ?= ./imports
# 最新备份包（按修改时间）；容器内路径为 /data/backups/...
PACKAGE    ?= $(firstword $(shell ls -t $(BACKUP_DIR)/*.nsb 2>/dev/null))
KEK_VERSION     ?= 1
KEK_NEW_VERSION ?= 2
# 恢复演练的登录口令（也可 export NOVELSTACK_DRILL_PASSWORD）
DRILL_PASSWORD ?=
# distroless/nonroot 的 uid:gid；数据目录与密钥文件需归它所有
APP_UID ?= 65532
APP_GID ?= 65532
# 第六阶段：控制台地址与临时 cookie（用于 make backup-now / health-check）
BASE_URL    ?= https://localhost:8443
ADMIN_COOKIE ?= /tmp/novelstack-admin.cookie

# app 容器是否在运行：决定命令走「一次性容器」还是「本地二进制」
CONTAINER := $(shell $(COMPOSE) ps -q app 2>/dev/null | head -n1)

# 一次性容器：--no-deps 不额外拉起 meilisearch；-T 让口令可以走管道。
# 数据目录（./data:/data）、导入目录（./imports:/imports）与 KEK secret
# （/run/secrets/kek_v1|kek_v2）都由 compose 定义，无需额外挂载。
NS_RUN = $(COMPOSE) run --rm --no-deps -T app

help:
	@echo "构建 / 开发"
	@echo "  make build            编译 bin/novelstack"
	@echo "  make web              编译控制台前端（Vite → internal/adminui/dist，go:embed 内嵌）"
	@echo "  make web-dev          前端开发服务器（/api 代理到 127.0.0.1:8080）"
	@echo "  make test / vet / fmt 测试 / 静态检查 / 格式化"
	@echo "  make test-short        单测（跳过完整栈/外部依赖，最快）"
	@echo "  make test-all          全量测试（含完整栈集成用例）"
	@echo "  make test-race         全量 -race（并发安全回归）"
	@echo ""
	@echo "运行"
	@echo "  make kek              生成 KEK v1 与预生成的 v2（离线备份！）"
	@echo "  make up / down / logs 容器启动 / 停止 / 日志"
	@echo ""
	@echo "备份与恢复（A）"
	@echo "  make backup           生成加密备份包（口令文件 $(PASS_FILE)）"
	@echo "  make backup-verify    校验最新包（或 PACKAGE=...）"
	@echo "  make restore-drill    一键恢复演练（需 DRILL_PASSWORD=登录口令）"
	@echo "  make drill-log        查看演练记录"
	@echo ""
	@echo "KEK 轮换（B）"
	@echo "  make kek-rotate       轮换 v1 → v2（幂等，可重跑）"
	@echo "  make kek-verify       抽样解密校验"
	@echo ""
	@echo "书库与缓存（D）"
	@echo "  make import           批量导入 $(IMPORT_DIR) 下的 .txt（幂等）"
	@echo "  make cache-prune      按上限/天数淘汰 TTS 缓存（默认 dry-run）"
	@echo "  make cache-clear      手工清理缓存（BOOK_ID=... 或 ALL=1）"
	@echo ""
	@echo "移动端（第五阶段）"
	@echo "  make android          构建 debug APK（需 JDK 17+ 与 Android SDK 35）"
	@echo "  make android-test     Kotlin 单元测试（:shared:testDebugUnitTest）"
	@echo "  make android-clean    清理 Gradle 构建产物"
	@echo ""
	@echo "第六阶段：运维"
	@echo "  make backup-now       经控制台立即触发一次自动备份（需 ADMIN_INITIAL_PASSWORD）"
	@echo "  make health-check     读取控制台健康横幅状态（JSON）"
	@echo ""
	@echo "验收"
	@echo "  make accept           端到端冒烟（经 Caddy，含设备令牌/同步/进度/预取/音频/自动备份）"
	@echo "  make demo-lifecycle           第四阶段演示（导入→备份→演练→轮换→缓存）"
	@echo "  make demo-search / demo-tts   前序阶段演示"

# ---- 密钥 ----

# 生成 KEK v1；同时预生成 v2（轮换时才启用，先离线保存）。
# 只执行一次；KEK 丢失等于全部密文不可恢复。
kek:
	@mkdir -p $(SECRETS)
	@if [ -f $(KEK_V1) ] || [ -f $(SECRETS)/kek ]; then \
		echo "$(KEK_V1) 或 $(SECRETS)/kek 已存在，拒绝覆盖（旧密钥可用 make kek-migrate 迁移）"; exit 1; fi
	@openssl rand -base64 32 > $(KEK_V1)
	@openssl rand -base64 32 > $(KEK_V2)
	@chmod 600 $(KEK_V1) $(KEK_V2)
	@if [ "$$(id -u)" = "0" ]; then chown $(APP_UID):$(APP_GID) $(KEK_V1) $(KEK_V2); fi
	@if [ ! -f "$(PASS_FILE)" ]; then 		openssl rand -base64 24 > "$(PASS_FILE)"; chmod 600 "$(PASS_FILE)"; 		echo "已生成 $(PASS_FILE)（备份口令，与 KEK 相互独立；同样需要离线备份）"; 	fi
	@echo "已生成 $(KEK_V1)（当前）与 $(KEK_V2)（预生成，轮换时启用）"
	@echo "  1) 立刻离线备份这两个文件（打印 / 硬件密钥 / 离线介质），不要提交进仓库"
	@echo "  2) 容器以 uid $(APP_UID) 运行，若非 root 生成：sudo chown $(APP_UID):$(APP_GID) $(KEK_V1) $(KEK_V2)"
	@echo "  3) .env 确认 KEK_PATH=/run/secrets/kek_v1、KEK_VERSION=1"

# 迁移第一阶段遗留的 secrets/kek（无版本号）到 secrets/kek.v1 + 预生成 v2
kek-migrate:
	@test -f $(SECRETS)/kek || { echo "$(SECRETS)/kek 不存在，无需迁移"; exit 1; }
	@test ! -f $(KEK_V1) || { echo "$(KEK_V1) 已存在，拒绝覆盖"; exit 1; }
	@cp $(SECRETS)/kek $(KEK_V1)
	@chmod 600 $(KEK_V1)
	@openssl rand -base64 32 > $(KEK_V2)
	@chmod 600 $(KEK_V2)
	@if [ "$$(id -u)" = "0" ]; then chown $(APP_UID):$(APP_GID) $(KEK_V1) $(KEK_V2); fi
	@echo "已迁移为 $(KEK_V1)（$(SECRETS)/kek 保留作离线副本）"
	@echo ".env 请改为：KEK_PATH=/run/secrets/kek_v1、KEK_VERSION=1"

# ---- 构建 ----

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o $(BIN) .

# 控制台前端：构建产物落到 internal/adminui/dist，由 //go:embed 打进二进制
web:
	@command -v pnpm >/dev/null 2>&1 || { echo "需要 pnpm：npm i -g pnpm"; exit 1; }
	pnpm --dir web install --frozen-lockfile
	pnpm --dir web build
	@echo "前端产物已生成：internal/adminui/dist（重新 make build 后生效）"

web-dev:
	pnpm --dir web dev

test:
	go test ./...

test-race:
	go test -race ./...

# 分层：默认 go test ./... 即 test-all；-short 只跑单测与不依赖完整栈的用例。
test-all:
	go test ./...

test-short:
	go test -short ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

# ---- 运行 ----

up:
	@test -f .env || { echo "请先: cp .env.example .env 并修改口令"; exit 1; }
	@test -f $(KEK_V1) || { echo "请先: make kek（或 make kek-migrate）"; exit 1; }
	@bd="$${BACKUP_HOST_DIR:-./backups}"; mkdir -p "$$bd"; \
		if [ "$$(id -u)" = "0" ]; then chown $(APP_UID):$(APP_GID) "$$bd"; fi
	docker compose up -d --build
	@echo "入口: https://127.0.0.1:8443   控制台: https://127.0.0.1:8443/admin"

down:
	docker compose down

logs:
	docker compose logs -f app caddy

# ---- A. 备份与恢复 ----

# 加密备份包：SQLite（VACUUM INTO 快照）+ 书籍密文 + TTS 缓存 + 客户端索引快照。
# 口令只从文件/管道读取，绝不来自命令行参数。
backup:
	@test -f $(PASS_FILE) || { echo "缺少 $(PASS_FILE)：写入至少 12 字符的备份口令并 chmod 600"; exit 1; }
	@if [ -n "$(CONTAINER)" ]; then \
		echo "==> 一次性容器内生成加密备份包"; \
		cat "$(PASS_FILE)" | $(NS_RUN) backup create --out-dir /data/backups --passphrase-file /dev/stdin; \
	else \
		echo "==> 本地生成加密备份包（$(DATA_DIR)）"; \
		test -x ./$(BIN) || $(MAKE) --no-print-directory build >/dev/null; \
		DATA_DIR="$(CURDIR)/$(DATA_DIR)" ./$(BIN) backup create --out-dir "$(BACKUP_DIR)" --passphrase-file "$(PASS_FILE)"; \
	fi

# 校验：读 .sha256 伴生文件 + 完整试解包 + 清点清单
backup-verify:
	@test -n "$(PACKAGE)" || { echo "找不到备份包：先 make backup 或指定 PACKAGE=..."; exit 1; }
	@test -f $(PASS_FILE) || { echo "缺少 $(PASS_FILE)"; exit 1; }
	@if [ -n "$(CONTAINER)" ]; then \
		cat "$(PASS_FILE)" | $(NS_RUN) backup verify \
			--package "/data/backups/$$(basename '$(PACKAGE)')" --passphrase-file /dev/stdin; \
	else \
		test -x ./$(BIN) || $(MAKE) --no-print-directory build >/dev/null; \
		./$(BIN) backup verify --package "$(PACKAGE)" --passphrase-file "$(PASS_FILE)"; \
	fi

backup-list:
	@ls -lh $(BACKUP_DIR)/*.nsb 2>/dev/null || echo "还没有备份包（$(BACKUP_DIR)）"

# 一键恢复演练：临时目录 + 临时端口 + 临时实例，登录 → 下载 → 比对明文 SHA-256
restore-drill:
	@test -n "$(PACKAGE)" || { echo "找不到备份包：先 make backup 或指定 PACKAGE=..."; exit 1; }
	@test -f $(PASS_FILE) || { echo "缺少 $(PASS_FILE)"; exit 1; }
	@pw="$(DRILL_PASSWORD)"; \
	if [ -z "$$pw" ]; then printf '登录口令（不回显）: '; stty -echo 2>/dev/null || true; read pw; stty echo 2>/dev/null || true; echo; fi; \
	if [ -z "$$pw" ]; then echo "缺少登录口令（DRILL_PASSWORD=... 或交互输入）"; exit 1; fi; \
	if [ -n "$(CONTAINER)" ]; then \
		cat "$(PASS_FILE)" | NOVELSTACK_DRILL_PASSWORD="$$pw" $(COMPOSE) run --rm --no-deps -T \
			-e NOVELSTACK_DRILL_PASSWORD \
			app restore-drill \
			--package "/data/backups/$$(basename '$(PACKAGE)')" \
			--kek "$(KEK_CURRENT_SECRET)" --kek-version $(KEK_VERSION) \
			--kek-old "1=/run/secrets/kek_v1,2=/run/secrets/kek_v2" --data-dir /data \
			--passphrase-file /dev/stdin; \
	else \
		test -x ./$(BIN) || $(MAKE) --no-print-directory build >/dev/null; \
		NOVELSTACK_DRILL_PASSWORD="$$pw" ./$(BIN) restore-drill \
			--package "$(PACKAGE)" --kek "$(KEK_CURRENT)" --kek-version $(KEK_VERSION) \
			--kek-old "1=$(KEK_V1),2=$(KEK_V2)" --data-dir "$(CURDIR)/$(DATA_DIR)" \
			--passphrase-file "$(PASS_FILE)"; \
	fi

drill-log:
	@test -f $(BACKUP_DIR)/drill-log.jsonl && tail -n 20 $(BACKUP_DIR)/drill-log.jsonl \
		|| echo "暂无演练记录（$(BACKUP_DIR)/drill-log.jsonl）"

# ---- B. KEK 轮换 ----

# 轮换前先把 .env 设成双密钥窗口（见 README「KEK 轮换」）：
#   KEK_PATH=/run/secrets/kek_v1  KEK_VERSION=1  KEK_OLD_PATHS=2=/run/secrets/kek_v2
# 这样轮换过程中服务仍能读旧行与新行；完成后切到 v2 再重启。
kek-rotate:
	@test -f $(KEK_V1) || { echo "缺少 $(KEK_V1)"; exit 1; }
	@test -f $(KEK_V2) || { echo "缺少 $(KEK_V2)（make kek 会预生成）"; exit 1; }
	@if cmp -s $(KEK_V1) $(KEK_V2); then echo "新旧 KEK 相同，拒绝轮换"; exit 1; fi
	@echo "==> 轮换 v$(KEK_VERSION) → v$(KEK_NEW_VERSION)（幂等，可重跑）"
	@if [ -n "$(CONTAINER)" ]; then \
		$(NS_RUN) kek rotate --old /run/secrets/kek_v1 --new /run/secrets/kek_v2 \
			--from $(KEK_VERSION) --to $(KEK_NEW_VERSION); \
	else \
		test -x ./$(BIN) || $(MAKE) --no-print-directory build >/dev/null; \
		DATA_DIR="$(CURDIR)/$(DATA_DIR)" ./$(BIN) kek rotate \
			--old "$(KEK_V1)" --new "$(KEK_V2)" --from $(KEK_VERSION) --to $(KEK_NEW_VERSION); \
	fi
	@echo ""
	@echo "下一步（按顺序）："
	@echo "  1) make kek-verify KEK_VERSION=$(KEK_NEW_VERSION)"
	@echo "  2) .env 改为 KEK_PATH=/run/secrets/kek_v2、KEK_VERSION=$(KEK_NEW_VERSION)、KEK_OLD_PATHS=1=/run/secrets/kek_v1"
	@echo "  3) docker compose up -d 重启；观察 24h 后再归档 kek.v1（代码永不自动删除）"

# 抽样校验：按 kek_version 分组，每组随机抽 N 本解密到底
kek-verify:
	@if [ -n "$(CONTAINER)" ]; then \
		$(NS_RUN) kek verify --kek 1=/run/secrets/kek_v1 --kek 2=/run/secrets/kek_v2 --sample 3; \
	else \
		test -x ./$(BIN) || $(MAKE) --no-print-directory build >/dev/null; \
		DATA_DIR="$(CURDIR)/$(DATA_DIR)" ./$(BIN) kek verify \
			--kek "1=$(KEK_V1)" --kek "2=$(KEK_V2)" --sample 3; \
	fi

# ---- D. 批量导入与缓存 ----

# 目录内 .txt 逐本走加密封存流程；按明文 SHA-256 幂等（重复文件标记 skipped）
import:
	@if [ -n "$(CONTAINER)" ]; then \
		$(NS_RUN) import --dir /imports --user admin; \
	else \
		test -x ./$(BIN) || $(MAKE) --no-print-directory build >/dev/null; \
		DATA_DIR="$(CURDIR)/$(DATA_DIR)" ./$(BIN) import --dir "$(IMPORT_DIR)" --user admin; \
	fi

# 默认 dry-run：先看会删多少，再 DRY_RUN=0 真正执行
DRY_RUN ?= 1
CACHE_MAX_MB ?= 0
CACHE_KEEP_DAYS ?= 0
cache-prune:
	@flags="--max-mb $(CACHE_MAX_MB) --keep-days $(CACHE_KEEP_DAYS)"; \
	if [ "$(DRY_RUN)" != "0" ]; then flags="$$flags --dry-run"; fi; \
	if [ -n "$(CONTAINER)" ]; then $(NS_RUN) cache prune $$flags; \
	else DATA_DIR="$(CURDIR)/$(DATA_DIR)" ./$(BIN) cache prune $$flags; fi

# 手工清理：BOOK_ID=xxx 清理单本；ALL=1 全量（流式播放中的片段受引用计数保护）
BOOK_ID ?=
ALL ?= 0
cache-clear:
	@if [ "$(ALL)" != "1" ] && [ -z "$(BOOK_ID)" ]; then echo "请指定 BOOK_ID=... 或 ALL=1"; exit 1; fi
	@if [ -n "$(CONTAINER)" ]; then \
		if [ "$(ALL)" = "1" ]; then $(NS_RUN) cache clear --all --yes; \
		else $(NS_RUN) cache clear --book "$(BOOK_ID)" --yes; fi; \
	else \
		if [ "$(ALL)" = "1" ]; then DATA_DIR="$(CURDIR)/$(DATA_DIR)" ./$(BIN) cache clear --all --yes; \
		else DATA_DIR="$(CURDIR)/$(DATA_DIR)" ./$(BIN) cache clear --book "$(BOOK_ID)" --yes; fi; \
	fi

# ---- 演示 / 验收 ----

demo-search:
	bash scripts/demo-search.sh

demo-tts:
	bash scripts/demo-tts.sh

# 第四阶段端到端演示（临时目录，不碰 ./data 与生产 KEK）
demo-lifecycle:
	bash scripts/demo-lifecycle.sh

# 第六阶段：立即触发一次与定时任务相同的备份，并查看健康状态
admin-login:
	@curl -fsS -c $(ADMIN_COOKIE) -H 'Content-Type: application/json' \
		-d '{"username":"admin","password":"$(ADMIN_INITIAL_PASSWORD)"}' $(BASE_URL)/api/login >/dev/null

backup-now: admin-login
	@curl -fsS -b $(ADMIN_COOKIE) -X POST $(BASE_URL)/api/admin/backup/run

health-check: admin-login
	@curl -fsS -b $(ADMIN_COOKIE) $(BASE_URL)/api/admin/dashboard | python3 -c \
		'import json,sys; h=json.load(sys.stdin)["health"]; print(json.dumps(h,ensure_ascii=False,indent=2))'

# 冒烟：healthz → login → /admin → dashboard → 上传 → 列表 → 下载 → 审计 → 登出
accept:
	go run ./scripts/acceptance --base https://localhost:8443 \
		--username admin --password "$$ADMIN_INITIAL_PASSWORD" --tls-skip-verify

# ---- 移动端（第五阶段）----
# 前置：JDK 17+、Android SDK（platform-35 + build-tools 35.0.0）。
# SDK 位置二选一：export ANDROID_HOME=... 或写 android/local.properties 的 sdk.dir=...
# 首次构建需要联网下载依赖；内网/镜像环境可加
#   NOVELSTACK_MAVEN_MIRROR=https://<mirror>/  make android
ANDROID_TASK   ?= :androidApp:assembleDebug
ANDROID_GRADLE ?= ./gradlew

android:
	@if [ -z "$$ANDROID_HOME$$ANDROID_SDK_ROOT" ] && [ ! -f android/local.properties ]; then \
		echo "缺少 Android SDK：export ANDROID_HOME=... 或在 android/local.properties 写 sdk.dir=..."; exit 1; fi
	cd android && $(ANDROID_GRADLE) $(ANDROID_TASK)
	@ls -lh android/androidApp/build/outputs/apk/debug/*.apk 2>/dev/null || true

apk: android

android-test:
	cd android && $(ANDROID_GRADLE) :shared:testDebugUnitTest

android-clean:
	cd android && $(ANDROID_GRADLE) clean

clean:
	rm -rf bin
