#!/usr/bin/env bash
# 第三阶段端到端演示（无需 Docker / 无需 Meilisearch）：
#   1. 启动本地后端（元数据检索自动降级为 SQLite 匹配）
#   2. 上传一本含「独有正文词」的书（书名/作者/标签正常）
#   3. 演示分层搜索：正文词在服务端查不到，书名查得到
#   4. 客户端按 ETag 同步章节 → 本地 FTS5 索引 → 命中正文并高亮
#   5. 检查磁盘：只存在密文快照，没有任何明文
#
# 用法：bash scripts/demo-search.sh
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORK="$(mktemp -d)"
PORT="${PORT:-18085}"
BASE="http://127.0.0.1:${PORT}"
PASS="${DEMO_PASSWORD:-demo-password-2026}"
STORE="${WORK}/local-reader"
mkdir -p "${WORK}/data" "${STORE}"
trap 'kill ${SRV_PID:-0} 2>/dev/null || true; rm -rf "${WORK}"' EXIT

echo "== 1. 构建并启动后端 =="
export PATH="/usr/local/go/bin:${PATH}"
(cd "${ROOT}" && go build -o "${WORK}/novelstack" . && go build -o "${WORK}/novelclient" ./cmd/novelclient)

openssl rand -base64 32 > "${WORK}/kek"
chmod 600 "${WORK}/kek"

DATA_DIR="${WORK}/data" KEK_PATH="${WORK}/kek" \
  ADMIN_INITIAL_PASSWORD="${PASS}" COOKIE_SECURE=false ADDR="127.0.0.1:${PORT}" \
  TTS_PROVIDER=off MEILI_URL="" SEARCH_SYNC_ON_START=false \
  "${WORK}/novelstack" >"${WORK}/server.log" 2>&1 &
SRV_PID=$!
for _ in $(seq 1 60); do
  if curl -fsS "${BASE}/healthz" >/dev/null 2>&1; then break; fi
  sleep 0.1
done
curl -fsS "${BASE}/healthz" >/dev/null || { echo "服务未启动"; cat "${WORK}/server.log"; exit 1; }

echo "== 2. 上传书籍（正文含独有词「琥珀色灯塔」）=="
python3 - "${WORK}/book.txt" <<'PYEOF'
import sys
body = """第一章 夜航船
他推开门，屋里很暗。窗外的雨还在下，檐角的水一滴一滴落进石臼里。
那枚铜钥匙上刻着四个字：夜航船。那是父亲留下的东西。
第二章 归途
天亮的时候，雨停了。他沿着河堤往回走，河面上浮着一层薄雾。
远处有人在撑船，船桨划开水面的声音很轻，一下，又一下。
他在船头看见一座琥珀色灯塔，光在水面上碎成一片。
"""
open(sys.argv[1], "w").write(body)
PYEOF
JAR="${WORK}/cookies.txt"
curl -fsS -c "${JAR}" -H 'Content-Type: application/json' \
  -d "{\"username\":\"admin\",\"password\":\"${PASS}\"}" "${BASE}/api/login" >/dev/null

BOOK_JSON=$(curl -fsS -b "${JAR}" \
  -F 'title=夜航船' -F 'author=阿来' -F 'tags=海上,民国' \
  -F 'description=一部关于夜航船与归途的小说' \
  -F "file=@${WORK}/book.txt;type=text/plain" "${BASE}/api/books")
BOOK_ID=$(printf '%s' "${BOOK_JSON}" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')
echo "book_id=${BOOK_ID}"

echo
echo "== 3. 服务端元数据搜索（分层验证）=="
curl -fsS -b "${JAR}" --get --data-urlencode 'q=琥珀色灯塔' "${BASE}/api/search/metadata" > "${WORK}/s1.json"
curl -fsS -b "${JAR}" --get --data-urlencode 'q=夜航船' "${BASE}/api/search/metadata" > "${WORK}/s2.json"
python3 - "${WORK}/s1.json" "${WORK}/s2.json" <<'PYEOF'
import json, sys
body_hits = json.load(open(sys.argv[1]))["hits"]
title_hits = json.load(open(sys.argv[2]))["hits"]
print("  正文词「琥珀色灯塔」→ %d 条命中（预期 0：正文不进服务端索引）" % len(body_hits))
if title_hits:
    h = title_hits[0]
    print("  书名「夜航船」→ %d 条命中，字段=%s，高亮=%s" % (len(title_hits), h["matched_field"], h["highlight"]))
else:
    print("  书名「夜航船」→ 0 条命中（异常）")
PYEOF

echo
echo "== 4. 客户端同步并本地检索 =="
export NOVELCLIENT_PASSPHRASE="local-index-passphrase"
"${WORK}/novelclient" -base "${BASE}" -store "${STORE}" -password "${PASS}" sync
"${WORK}/novelclient" -base "${BASE}" -store "${STORE}" -password "${PASS}" search "琥珀色灯塔" > "${WORK}/hits.jsonl"
python3 - "${WORK}/hits.jsonl" <<'PYEOF'
import json, sys
lines = [l.strip() for l in open(sys.argv[1]) if l.strip()]
for line in lines:
    d = json.loads(line)
    print("  本地命中：%s 第%s章 → %s" % (d["book_title"], d["chapter_index"], d["snippet"]))
if not lines:
    print("  本地未命中（异常）")
PYEOF

echo
echo "== 5. 磁盘检查（本地只存密文快照）=="
SNAP="${STORE}/index.enc"
printf "  快照头 6 字节: "; head -c 6 "${SNAP}"; echo
printf "  快照中明文「琥珀色灯塔」出现次数: "; { grep -a -o '琥珀色灯塔' "${SNAP}" || true; } | wc -l
printf "  快照中明文「他推开门」出现次数: "; { grep -a -o '他推开门' "${SNAP}" || true; } | wc -l
printf "  服务端 data 目录明文出现次数: "; { grep -ra -o '琥珀色灯塔' "${WORK}/data" || true; } | wc -l

echo
echo "== 6. 指标 =="
curl -fsS -b "${JAR}" "${BASE}/api/admin/metrics" > "${WORK}/metrics.json"
python3 - "${WORK}/metrics.json" <<'PYEOF'
import json, sys
d = json.load(open(sys.argv[1]))
idx = d["metadata_index"]
print("  索引文档数/书籍数: %s / %s  backend=%s" % (idx.get("documents"), idx.get("books"), idx.get("backend")))
print("  搜索 P50/P95(ms): %s / %s  样本=%s" % (d["search"]["p50_ms"], d["search"]["p95_ms"], d["search"]["samples"]))
cov = d["coverage"][0] if d["coverage"] else {}
print("  章节覆盖度: %s / %s" % (cov.get("indexed_chapters"), cov.get("total_chapters")))
PYEOF

echo
echo "演示完成 ✓  本地索引目录: ${STORE}（结束时已清理）"
