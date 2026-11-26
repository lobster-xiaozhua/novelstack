#!/usr/bin/env bash
# 第二阶段端到端演示（无需任何外部模型）：
#   本地进程启动后端 + mock TTS Provider → 登录 → 上传带章节的 txt →
#   查章节 → 流式朗读第 1 章 → 把返回的 WAV 片段序列切分为可播放的片段文件 →
#   打印 TTS 指标 → 登出。
#
# 用法：bash scripts/demo-tts.sh          （或 make demo-tts）
set -euo pipefail

cd "$(dirname "$0")/.."
ROOT=$(pwd)
OUT="$ROOT/demo-out"
PORT="${DEMO_PORT:-18080}"
BASE="http://127.0.0.1:$PORT"
WORK=$(mktemp -d "${TMPDIR:-/tmp}/novelstack-demo.XXXXXX")
JAR="$WORK/cookies.txt"

cleanup() {
	if [[ -n "${PID:-}" ]]; then
		kill "$PID" 2>/dev/null || true
		wait "$PID" 2>/dev/null || true
	fi
	rm -rf "$WORK"
}
trap cleanup EXIT

mkdir -p "$WORK/data" "$OUT"
openssl rand -base64 32 >"$WORK/kek"
chmod 600 "$WORK/kek"

export DATA_DIR="$WORK/data"
export KEK_PATH="$WORK/kek"
export ADMIN_INITIAL_PASSWORD="demo-password-2026"
export COOKIE_SECURE=false
export ADDR="127.0.0.1:$PORT"
export TTS_PROVIDER=mock
export TTS_VOICE=demo
export TTS_SPEED=1.0
export TTS_MAX_SEGMENT_CHARS=100

echo "== 1. 构建并启动后端（TTS_PROVIDER=mock，数据目录 $WORK/data）"
go build -o "$WORK/novelstack" .
"$WORK/novelstack" >"$WORK/app.log" 2>&1 &
PID=$!

for _ in $(seq 1 100); do
	if curl -sf "$BASE/healthz" >/dev/null 2>&1; then break; fi
	sleep 0.1
done
curl -sf "$BASE/healthz" >/dev/null || { echo "启动失败，日志："; cat "$WORK/app.log"; exit 1; }
echo "   healthz ok"

echo "== 2. 准备示例书籍（两章）"
cat >"$WORK/book.txt" <<'BOOK'
第一章 夜航船
他推开门，屋里很暗。窗外的雨还在下，檐角的水一滴一滴落进石臼里。他摸到桌边坐下，把湿透的外衣挂在椅背上，听见楼上有人咳嗽。那声音很轻，像是怕惊动了谁。他抬起头，看见楼梯转角处有一盏灯，灯下站着一个人影，正低头看着他。
他没有说话，只是把手伸进口袋，摸到那枚冰凉的铜钥匙。钥匙上刻着三个字：夜航船。那是父亲留下的东西，也是他此行的全部理由。
第二章 归途
天亮的时候，雨停了。他沿着河堤往回走，河面上浮着一层薄雾。远处有人在撑船，船桨划开水面的声音很轻，一下，又一下。
他想起昨晚那个人影说过的话：船不开往对岸，只开往从前。他笑了笑，觉得这话很像父亲的口气。父亲一生都在水上，最后也留在水上。
BOOK

echo "== 3. 登录"
curl -sf -c "$JAR" -H 'Content-Type: application/json' \
	-d '{"username":"admin","password":"demo-password-2026"}' \
	"$BASE/api/login" >/dev/null
echo "   login ok"

echo "== 4. 上传（服务端流式加密）"
UP=$(curl -sf -b "$JAR" -F "title=夜航船（演示）" -F "file=@$WORK/book.txt;type=text/plain" "$BASE/api/books")
BOOK_ID=$(printf '%s' "$UP" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')
echo "   book_id=$BOOK_ID"

echo "== 5. 章节索引"
curl -sf -b "$JAR" "$BASE/api/books/$BOOK_ID/chapters" | python3 -c '
import json,sys
d=json.load(sys.stdin)
for c in d["chapters"]:
    print("   [%d] %s (%d 字)" % (c["index"], c["title"], c["chars"]))
'

echo "== 6. 流式朗读第 1 章（首片即返回，服务端逐段 flush）"
START=$(date +%s%N)
curl -sf -b "$JAR" -D "$WORK/headers.txt" \
	"$BASE/api/books/$BOOK_ID/chapters/1/speak?voice=demo&speed=1.0" \
	-o "$OUT/chapter-1.stream.wav"
END=$(date +%s%N)
grep -iE '^(content-type|x-tts)' "$WORK/headers.txt" | sed 's/^/   /'
echo "   下载耗时 $(( (END-START)/1000000 )) ms，大小 $(stat -c%s "$OUT/chapter-1.stream.wav") 字节"

echo "== 7. 切分 WAV 片段（客户端行为：按 RIFF 头切分后顺序播放）"
python3 - "$OUT/chapter-1.stream.wav" "$OUT" <<'PY'
import sys, struct, os
src, outdir = sys.argv[1], sys.argv[2]
data = open(src, 'rb').read()
assert data[:4] == b'RIFF', '返回内容不是 WAV'
offsets = [i for i in range(len(data) - 3) if data[i:i+4] == b'RIFF']
print('   片段数:', len(offsets))
for idx, off in enumerate(offsets):
    end = offsets[idx+1] if idx+1 < len(offsets) else len(data)
    seg = data[off:end]
    p = os.path.join(outdir, 'chapter-1-seg-%02d.wav' % idx)
    open(p, 'wb').write(seg)
    # 校验 RIFF size 字段与 data 块
    riff_size = struct.unpack('<I', seg[4:8])[0]
    ok = seg[8:12] == b'WAVE' and abs(riff_size + 8 - len(seg)) <= 1
    print('   %s  %d 字节  RIFF 结构 %s' % (os.path.basename(p), len(seg), 'OK' if ok else 'WARN'))
PY

echo "== 8. 命中缓存再取一次（不应触发新合成）"
curl -sf -b "$JAR" "$BASE/api/books/$BOOK_ID/chapters/1/speak?voice=demo&speed=1.0" \
	-o "$OUT/chapter-1.second.wav"
if cmp -s "$OUT/chapter-1.stream.wav" "$OUT/chapter-1.second.wav"; then
	echo "   两次输出完全一致（缓存命中）"
else
	echo "   !! 两次输出不一致"; exit 1
fi

echo "== 9. TTS 指标"
curl -sf -b "$JAR" "$BASE/api/admin/tts/stats?limit=50" | python3 -m json.tool | sed 's/^/   /'

echo "== 10. 磁盘检查：缓存目录必须是密文（无 RIFF 头）"
python3 - "$WORK/data" <<'PY'
import os, sys
root = os.path.join(sys.argv[1], 'tts-cache')
files = [os.path.join(dp, f) for dp, _, fs in os.walk(root) for f in fs if f.endswith('.bin')]
print('   缓存片段数:', len(files))
for f in files[:3]:
    head = open(f, 'rb').read(4)
    print('   %s  magic=%s' % (os.path.relpath(f, root), head.decode('latin1')))
    assert head == b'NSAC', '缓存文件不是密文格式'
PY

echo "== 11. 登出"
curl -sf -b "$JAR" -c "$JAR" -X POST "$BASE/api/logout" >/dev/null
echo "   登出完成"

echo
echo "完成 ✓ 可直接播放的音频片段："
ls -1 "$OUT"/chapter-1-seg-*.wav | sed 's/^/  /'
if command -v file >/dev/null 2>&1; then
	file "$OUT"/chapter-1-seg-00.wav | sed 's/^/  /'
fi
echo "（这些片段由客户端按顺序拼接播放；服务端不提供整章导出，也不落明文音频）"
