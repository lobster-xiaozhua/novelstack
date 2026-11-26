#!/usr/bin/env bash
# 第四阶段端到端演示：批量导入 → 加密备份 → 恢复演练 → KEK 轮换 → 轮换后读取 → 缓存清理。
#
# 全程在临时目录里跑（不碰 ./data、不碰生产 KEK），退出码 0 表示全部环节符合预期。
# 依赖：go、curl、openssl、python3、sha256sum（coreutils）。
set -uo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
TMP=$(mktemp -d /tmp/novelstack-lifecycle-XXXXXX)
PORT=$(( 18000 + RANDOM % 2000 ))
BASE="http://127.0.0.1:${PORT}"
DATA="$TMP/data"
KEK1="$TMP/kek.v1"
KEK2="$TMP/kek.v2"
PASS="$TMP/backup.pass"
COOKIE="$TMP/cookies.txt"
SRV_PID=""
FAILS=0

say()  { printf '\n\033[1m== %s\033[0m\n' "$*"; }
ok()   { printf '  \033[32mok\033[0m   %s\n' "$*"; }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*"; FAILS=$((FAILS+1)); }
cleanup() {
	if [ -n "$SRV_PID" ] && kill -0 "$SRV_PID" 2>/dev/null; then
		kill "$SRV_PID" 2>/dev/null
		wait "$SRV_PID" 2>/dev/null
	fi
	rm -rf "$TMP"
}
trap cleanup EXIT

wait_health() {
	for _ in $(seq 1 80); do
		if curl -fsS "$BASE/healthz" >/dev/null 2>&1; then return 0; fi
		sleep 0.2
	done
	return 1
}

json() { python3 -c "import json,sys;d=json.load(sys.stdin);print(eval(sys.argv[1],{'d':d}))" "$1"; }

say "0. 构建与临时环境 ($TMP)"
mkdir -p "$DATA" "$TMP/in"
CGO_ENABLED=0 go build -trimpath -o "$TMP/novelstack" "$ROOT" || { bad "go build"; exit 1; }
openssl rand -base64 32 > "$KEK1"
openssl rand -base64 32 > "$KEK2"
printf 'demo-passphrase-please-change' > "$PASS"
ok "二进制与 KEK v1/v2 就绪"

# 三份 txt，其中 b.txt 与 a.txt 内容相同（用于验证幂等）
cat > "$TMP/in/夜航船.txt" <<'EOF'
第一章 夜航船
他推开门，江上的雾正浓。

第二章 归途
灯塔在雾里忽明忽暗，他想起许多年前的那个夜晚。
EOF
cp "$TMP/in/夜航船.txt" "$TMP/in/夜航船-副本.txt"
cat > "$TMP/in/第二个故事.txt" <<'EOF'
第一章 起风
风从北面来，带着沙砾与旧年的信。
EOF

say "1. 启动服务（v1 KEK，mock TTS，无 Meilisearch）"
DATA_DIR="$DATA" KEK_PATH="$KEK1" KEK_VERSION=1 ADDR="127.0.0.1:$PORT" \
COOKIE_SECURE=false TTS_PROVIDER=mock MEILI_URL= LEGADO_ENABLED=false \
ADMIN_INITIAL_PASSWORD="demo-initial-password" \
	"$TMP/novelstack" serve >"$TMP/serve.log" 2>&1 &
SRV_PID=$!
wait_health && ok "healthz 通过" || { bad "服务未就绪"; cat "$TMP/serve.log"; exit 1; }

curl -fsS -c "$COOKIE" -H 'Content-Type: application/json' \
	-d '{"username":"admin","password":"demo-initial-password"}' "$BASE/api/login" >/dev/null \
	&& ok "登录成功" || bad "登录失败"

say "2. 批量导入（3 个文件，其中 1 个重复）"
DATA_DIR="$DATA" KEK_PATH="$KEK1" KEK_VERSION=1 \
	"$TMP/novelstack" import --dir "$TMP/in" --user admin | tee "$TMP/import1.txt"
grep -q 'imported=2 skipped=1' "$TMP/import1.txt" \
	&& ok "首次导入：2 本入库、1 本按 SHA-256 去重" || bad "首次导入计数不符"

DATA_DIR="$DATA" KEK_PATH="$KEK1" KEK_VERSION=1 \
	"$TMP/novelstack" import --dir "$TMP/in" --user admin | tee "$TMP/import2.txt"
grep -q 'imported=0 skipped=3' "$TMP/import2.txt" \
	&& ok "二次导入：全部跳过（幂等）" || bad "二次导入不幂等"

say "3. 下载一份明文，作为轮换后的比对基准"
BOOK=$(curl -fsS -b "$COOKIE" "$BASE/api/books" | json 'd[0]["id"]')
curl -fsS -b "$COOKIE" "$BASE/api/books/$BOOK/content" -o "$TMP/before.bin"
SHA_BEFORE=$(sha256sum "$TMP/before.bin" | cut -d' ' -f1)
ok "book=$BOOK sha=$SHA_BEFORE"

say "3b. 朗读一章（mock TTS）→ 生成加密音频缓存"
curl -fsS -b "$COOKIE" "$BASE/api/books/$BOOK/chapters/0/speak?mode=stream" -o "$TMP/ch0.wav" \
	|| bad "朗读接口失败"
WAV=$(stat -c%s "$TMP/ch0.wav" 2>/dev/null || echo 0)
[ "$WAV" -gt 1000 ] && ok "合成音频 ${WAV} 字节，片段已加密入缓存" || bad "朗读输出异常（${WAV} 字节）"

say "4. 备份 → 校验 → 恢复演练"
DATA_DIR="$DATA" "$TMP/novelstack" backup create --out-dir "$DATA/backups" \
	--passphrase-file "$PASS" --json > "$TMP/backup.json" || bad "备份失败"
PKG=$(json 'd["path"]' < "$TMP/backup.json")
echo "  package=$PKG"
echo "  sha256=$(json 'd["sha256"]' < "$TMP/backup.json")  books=$(json 'd["manifest"]["books"]' < "$TMP/backup.json")"

DATA_DIR="$DATA" "$TMP/novelstack" backup verify --package "$PKG" --passphrase-file "$PASS" \
	&& ok "包校验（含完整试解包）通过" || bad "包校验失败"

# 错口令必须失败
if DATA_DIR="$DATA" "$TMP/novelstack" backup verify --package "$PKG" \
	--passphrase-file <(printf 'wrong-passphrase-xxxx') >/dev/null 2>&1; then
	bad "错误口令竟然通过了校验"
else
	ok "错误口令被拒绝"
fi

NOVELSTACK_DRILL_PASSWORD='demo-initial-password' DATA_DIR="$DATA" \
	"$TMP/novelstack" restore-drill --package "$PKG" --kek "$KEK1" --kek-version 1 \
	--kek-old "1=$KEK1" --book "$BOOK" --data-dir "$DATA" --passphrase-file "$PASS" \
	> "$TMP/drill.json" 2>"$TMP/drill.log"
DRILL_SHA=$(json 'd["actual_sha256"]' < "$TMP/drill.json" 2>/dev/null)
if [ "$DRILL_SHA" = "$SHA_BEFORE" ]; then
	ok "演练：临时实例登录+下载成功，明文 SHA-256 与备份前一致"
else
	bad "演练 SHA 不一致（status=$(json 'd["result"]' < "$TMP/drill.json" 2>/dev/null)）"
	cat "$TMP/drill.log"
fi
[ -s "$DATA/backups/drill-log.jsonl" ] && ok "演练记录已追加 drill-log.jsonl" || bad "缺少演练记录"

say "5. KEK 轮换 v1 → v2（幂等）"
DATA_DIR="$DATA" "$TMP/novelstack" kek rotate --old "$KEK1" --new "$KEK2" --from 1 --to 2 \
	| tee "$TMP/rot1.txt"
grep -q 'books=2' "$TMP/rot1.txt" && ok "两本书的 DEK 已重包" || bad "轮换计数不符"
SEGS=$(sed -n 's/.*segments=\([0-9]*\).*/\1/p' "$TMP/rot1.txt")
[ "${SEGS:-0}" -gt 0 ] && ok "音频缓存片段也完成重包（segments=$SEGS）" || bad "音频片段未参与轮换"

DATA_DIR="$DATA" "$TMP/novelstack" kek rotate --old "$KEK1" --new "$KEK2" --from 1 --to 2 \
	| tee "$TMP/rot2.txt"
grep -q 'books=0 skipped=2' "$TMP/rot2.txt" && ok "重跑：全部跳过（幂等）" || bad "轮换不幂等"

DATA_DIR="$DATA" "$TMP/novelstack" kek verify --kek "1=$KEK1" --kek "2=$KEK2" --sample 3 \
	> "$TMP/verify.json" 2>&1 && ok "轮换后抽样解密全部通过" || { bad "抽样校验失败"; cat "$TMP/verify.json"; }

say "6. 旧 KEK 已无法解开新行（应失败）"
DATA_DIR="$DATA" "$TMP/novelstack" kek verify --kek "1=$KEK1" --sample 1 > "$TMP/oldonly.json" 2>&1 \
	&& bad "仅用 v1 竟然校验通过" || ok "仅加载 v1 无法解密已轮换的书籍"

say "7. 双密钥窗口内重启服务，确认轮换后仍可读"
kill "$SRV_PID" 2>/dev/null; wait "$SRV_PID" 2>/dev/null
DATA_DIR="$DATA" KEK_PATH="$KEK2" KEK_VERSION=2 KEK_OLD_PATHS="1=$KEK1" ADDR="127.0.0.1:$PORT" \
COOKIE_SECURE=false TTS_PROVIDER=mock MEILI_URL= LEGADO_ENABLED=false \
	"$TMP/novelstack" serve >>"$TMP/serve.log" 2>&1 &
SRV_PID=$!
wait_health || { bad "轮换后服务未就绪"; exit 1; }
curl -fsS -c "$COOKIE" -H 'Content-Type: application/json' \
	-d '{"username":"admin","password":"demo-initial-password"}' "$BASE/api/login" >/dev/null
curl -fsS -b "$COOKIE" "$BASE/api/books/$BOOK/content" -o "$TMP/after.bin"
SHA_AFTER=$(sha256sum "$TMP/after.bin" | cut -d' ' -f1)
[ "$SHA_AFTER" = "$SHA_BEFORE" ] && ok "轮换后解密结果与轮换前一致（sha=$SHA_AFTER）" \
	|| bad "轮换后明文不一致"

say "8. 缓存治理：统计 → 淘汰（dry-run）→ 按书清理"
CACHE_ENV=(env DATA_DIR="$DATA" KEK_PATH="$KEK2" KEK_VERSION=2 KEK_OLD_PATHS="1=$KEK1")
"${CACHE_ENV[@]}" "$TMP/novelstack" cache prune --max-mb 1024 --dry-run | tee "$TMP/prune.txt" \
	|| bad "cache prune 失败"
BEFORE=$(sed -n 's/.*bytes_before=\([0-9]*\).*/\1/p' "$TMP/prune.txt")
[ "${BEFORE:-0}" -gt 0 ] && ok "缓存统计到 ${BEFORE} 字节（dry-run 未删除）" || bad "缓存统计为 0"
"${CACHE_ENV[@]}" "$TMP/novelstack" cache clear --book "$BOOK" --yes | tee "$TMP/clear.txt" \
	|| bad "cache clear 失败"
LEFT=$(sed -n 's/.*segments_left=\([0-9]*\).*/\1/p' "$TMP/clear.txt")
[ "${LEFT:-1}" -eq 0 ] && ok "按书清理后该书的片段全部删除" || bad "清理后仍有 ${LEFT} 个片段"

say "9. 控制台与审计"
CODES=$(curl -s -o /dev/null -w '%{http_code}' -b "$COOKIE" "$BASE/api/admin/dashboard")
[ "$CODES" = "200" ] && ok "dashboard 200（已登录）" || bad "dashboard 返回 $CODES"
CODEA=$(curl -s -o /dev/null -w '%{http_code}' "$BASE/api/admin/dashboard")
[ "$CODEA" = "401" ] && ok "未登录 dashboard 401" || bad "未登录 dashboard 返回 $CODEA"
CODEL=$(curl -s -o /dev/null -w '%{http_code}' "$BASE/admin")
[ "$CODEL" = "303" ] && ok "未登录 /admin 303 → 登录页" || bad "未登录 /admin 返回 $CODEL"
for a in backup_created kek_rotated; do
	if curl -fsS -b "$COOKIE" "$BASE/api/admin/audit?action=$a" | grep -q "$a"; then
		ok "审计含 $a"
	else
		bad "审计缺少 $a"
	fi
done

say "结果"
if [ "$FAILS" -eq 0 ]; then
	printf '\033[32m全部通过\033[0m（临时目录随后清理）\n'
	exit 0
fi
printf '\033[31m%d 项失败\033[0m\n' "$FAILS"
exit 1
