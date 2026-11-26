#!/usr/bin/env bash
# 生成可直接导入 Legado 的书源 JSON：
#   1. 用账号口令登录管理接口，签发一个长期书源令牌（只存哈希，可单独撤销）
#   2. 登录网关，拿到网关会话 token（scope=legado，不能用于 /api）
#   3. 把两者填进 deploy/legado-book-source.json 的 header
#
# 用法：
#   BASE=https://localhost:8443 USER=admin PASS='...' bash scripts/legado-setup.sh
# 输出：deploy/legado-book-source.local.json（已 gitignore，含真实凭证，勿提交）
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BASE="${BASE:-https://localhost:8443}"
USER="${USER:-admin}"
PASS="${PASS:?需要 PASS 环境变量（管理员口令）}"
OUT="${OUT:-${ROOT}/deploy/legado-book-source.local.json}"
INSECURE="${INSECURE:--k}"
TOKEN_NAME="${TOKEN_NAME:-legado-$(date +%Y%m%d)}"

TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT

echo "== 1. 登录管理接口并签发书源令牌 =="
curl -fsS ${INSECURE} -c "${TMP}/cookies.txt" -H 'Content-Type: application/json' \
  -d "{\"username\":\"${USER}\",\"password\":\"${PASS}\"}" "${BASE}/api/login" >/dev/null
curl -fsS ${INSECURE} -b "${TMP}/cookies.txt" -H 'Content-Type: application/json' \
  -d "{\"name\":\"${TOKEN_NAME}\"}" "${BASE}/api/admin/legado/tokens" > "${TMP}/token.json"
BOOK_TOKEN="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["token"])' "${TMP}/token.json")"

echo "== 2. 登录网关（获取网关会话 token）=="
curl -fsS ${INSECURE} -H 'Content-Type: application/json' \
  -d "{\"username\":\"${USER}\",\"password\":\"${PASS}\"}" "${BASE}/gateway/legado/login" > "${TMP}/gw.json"
GW_TOKEN="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["session_token"])' "${TMP}/gw.json")"

echo "== 3. 生成书源 JSON =="
python3 - "${ROOT}/deploy/legado-book-source.json" "${OUT}" "${BASE}" "${GW_TOKEN}" "${BOOK_TOKEN}" <<'PYEOF'
import json, sys
src, out, base, gw, tok = sys.argv[1:6]
d = json.load(open(src))
d["bookSourceUrl"] = base
d["header"] = json.dumps({"X-Legado-Session": gw, "X-Legado-Token": tok}, ensure_ascii=False)
json.dump(d, open(out, "w"), ensure_ascii=False, indent=2)
print("已写入", out)
PYEOF

echo
echo "下一步："
echo "  1. 在 Legado「书源管理 → 本地导入」选择 ${OUT}"
echo "  2. 手机必须能访问 ${BASE}（默认 Caddy 只绑定 127.0.0.1，需隧道/局域网反向代理）"
echo "  3. 吊销：curl ${INSECURE} -b <管理会话> -X DELETE ${BASE}/api/admin/legado/tokens/<id>"
