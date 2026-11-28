# novelstack —— 私人小说阅读站（第一 ~ 十三阶段）

单用户、默认对外屏蔽的私人站点。

| 阶段 | 能力 |
|---|---|
| 一 | 认证（Argon2id + session）→ 信封加密落盘（AES-256-GCM）→ 解密读取 → 审计 |
| 二 | 章节切分 → 句子级分片 → 外部 TTS 合成 → 片段级加密音频缓存 → 流式朗读 |
| 三 | **分层搜索**（服务端只索引元数据 / 正文全文搜索在客户端本地）+ **Legado 书源网关** + ETag 章节同步 + 覆盖度指标 |
| 四 | **加密备份包 + 一键恢复演练** · **KEK 轮换** · **只读运营控制台 `/admin`**（go:embed 内嵌 Vue）· **批量导入 + 缓存生命周期** |
| 五 | **移动端 App（KMP + Compose）**：设备令牌/配对码 · 增量同步 · 加密章节预取 · SQLCipher + FTS5 离线库 · 设备内解密朗读 · 一键清除 |
| 六 | **最小运维加固**：每日自动备份 + 保留策略 · 健康横幅 + 可选 webhook · KEK 离线备份提醒 · Android release 签名模板 |
| 七~十 | **Android 生产化**：后台播放/通知栏/定时续听 · 发现—阅读—足迹—收藏闭环 · 国产 ROM 保活与全链路遥测 · 动态根地址与局域网配对 · 软删除闭环 + 服务端授时 |
| 十一+十二 | **全端苹果风（G3 连续曲率）+ 前后台分离 + Web 阅读器 + 多端同步**：用户端 `/` 与运营端 `/admin` 分家 · 去 WebSocket 改前台轮询 + 位置优先合并 · Web Crypto 解密 + SW/IndexedDB 离线读 · 移动端表格降级为卡片 |
| 十三 | **新手引导与响应式收口**：首启欢迎页（DataStore / localStorage 持久化）· 蒙层镂空高亮 · 空状态引导 · 设置内嵌「使用指南」· PC 侧栏 + 多列网格 · 危险操作降级为底部确认菜单 |

第四阶段的第一原则：**能恢复才算备份，能轮换才算密钥管理**。因此本阶段的
验收不是「命令跑通」，而是「错误口令/篡改包必须失败且不留半成品」「旧包在轮换后仍可解密」
「控制台在未登录时只是一个登录页」。详见 §13。

第五阶段补上路线 B：**服务端一行明文都不多给**。设备用一次性配对码换长期令牌，令牌只能
同步元数据、拉密文、报进度；章节密文与音频密文都在设备内用 Keystore 私钥解包后解密，
明文只存在于内存与 UI。设备令牌与浏览器会话是两套凭证，撤销互不影响（§14）。

第六阶段不再扩张功能：**云盘已经做物理冗余**，只补云厂商救不了的三件事——KEK 离线备份
确认、误操作防护（健康横幅/webhook）、自动备份保留策略；外加 release 签名让用户能长期
覆盖安装（§15）。

第三阶段最重要的判断：**搜索必须分层，不能一个方案通吃**。服务端持有的是密文，
因此服务端索引只允许出现「书名、作者、标签、简介、章节标题」；正文全文检索放在
客户端本地（FTS5 + BM25），查询词、索引、明文都不离开设备。

---

## 1. 第三阶段架构

```
                     ┌──────────────────────────── 服务端（密文） ────────────────────────────┐
 浏览器 / 客户端 ──TLS──▶ Caddy ──▶ app (Go, distroless)                                   │
                     │             ├── /api/books/...            信封加密的书籍密文           │
                     │             ├── /api/books/{id}/chapters  目录（只读 SQLite，不解密）   │
                     │             ├── /api/books/{id}/chapters/{i}/plain  解密单章 + ETag     │
                     │             ├── /api/search/metadata  ──▶ Meilisearch（只含元数据字段） │
                     │             └── /gateway/legado/*    书源网关（会话 + 令牌双层认证）    │
                     └──────────────────────────────────────────────────────────────────────┘

 客户端（路线 A：Legado；或未来 KMP App）
   ├─ 元数据检索：调 /api/search/metadata（服务端只看到书名类字段）
   └─ 全文检索：拉 /plain（ETag 协商）→ 本地加密库 → FTS5 倒排 → BM25 → 高亮
```

**为什么不做服务端全文搜索**：

- 服务端要支持正文检索，就必须持有可检索的明文（或做可搜索加密）。前者直接违背加密存储前提；
  后者是活跃研究领域，且会泄露访问模式（access pattern）。
- Meilisearch 之类引擎的索引本身就是明文的倒排表，放进容器等于把正文复制一份。
- 因此本阶段把「元数据」与「正文」彻底切开：前者可索引、可用于发现书籍；后者只解密到
  请求体/响应体，检索发生在设备上。

**已知代价**（写在限制里，不隐藏）：未缓存的章节无法全文搜索；跨设备索引不共享。

---

## 2. 文件树（★ 为第三阶段新增/修改）

```
novelstack/
├── main.go                          ★ 装配 search/legado；启动全量索引同步
├── clientcore/                      ★ 客户端本地检索参考实现（可执行规范）
│   ├── store.go                     ★ 内存 SQLite + FTS5 + 整库密文快照（AES-GCM）
│   ├── client.go                    ★ HTTP 客户端：登录/目录/ETag 拉章/回执/元数据搜索
│   ├── keystore.go                  ★ 设备密钥抽象（演示用 Argon2id；移动端换 Keystore）
│   └── store_test.go                ★ 本地检索/快照密文/篡改
├── cmd/novelclient/main.go          ★ 命令行阅读客户端（sync / search / cat / local）
├── internal/
│   ├── chapterscan/scan.go          ★ 流式章节标题扫描（不驻留正文）
│   │   └── scan_test.go             ★ 与整文本切分的一致性回归（含 CRLF/随机文本）
│   ├── search/                      ★ 服务端元数据检索层
│   │   ├── search.go                ★ 字段白名单/降级匹配/查询指纹/延迟分位
│   │   ├── meili.go                 ★ Meilisearch 最小 HTTP 客户端（设置/文档/搜索/统计）
│   │   └── *_test.go                ★ 白名单/索引无正文/高亮映射/降级
│   ├── books/
│   │   ├── books.go                 ★ 元数据(author/tags/description)、上传时流式扫描目录
│   │   └── chapters.go              ★ 目录 / 单章明文(ETag 304) / 回执 / PATCH 元数据 / 覆盖度
│   ├── legado/legado.go             ★ 书源网关 + 令牌库（只存哈希，可撤销）
│   ├── auth/auth.go                 ★ 会话作用域（api / legado）、网关 cookie
│   ├── db/db.go                     ★ 迁移 v3：元数据列、book_chapters、chapter_acks、
│   │                                    search_logs、legado_tokens、sessions.scope
│   ├── config/config.go             ★ MEILI_* / SEARCH_* / LEGADO_* 配置
│   ├── server/server.go             ★ 新路由、网关双层认证、/api/admin/metrics
│   ├── server/search_test.go        ★ 第三阶段集成测试（分层/ETag/网关/客户端）
│   ├── server/tts_test.go           （保持不变）
│   ├── tts/ ttscache/ cryptobox/    （保持不变；章节切分改为复用 chapterscan）
│   └── audit/                       （保持不变）
├── deploy/legado-book-source.json   ★ 可导入 Legado 的书源模板
├── scripts/
│   ├── demo-search.sh               ★ 端到端演示（分层搜索 + 客户端 FTS5 + 磁盘检查）
│   ├── legado-setup.sh              ★ 生成带凭证的书源 JSON
│   ├── demo-tts.sh                  （第二阶段演示）
│   └── acceptance/main.go           ★ 增加目录/ETag/回执/元数据搜索/指标/网关环节
├── docker-compose.yml               ★ 新增 meilisearch（不发布端口、mem_limit 128m）
├── .env.example                     ★ MEILI_* / LEGADO_* 模板
├── Makefile                         ★ demo-search
└── README.md                        ★ 本文
```

---

## 3. 移动端路线：A（Legado 网关，三阶段）+ B（KMP App，五阶段）均已实现

| | 路线 A：Legado 书源网关 | 路线 B：KMP 自建 App |
|---|---|---|
| 状态 | **已实现**（三阶段：网关 + 书源 JSON + 双层认证） | **已实现**（五阶段：KMP + Compose，见 §14） |
| 收益 | 立刻获得成熟阅读 UI（翻页/字体/背景/TTS 对接），只需维护网关与加密边界 | 加密边界完全自控，无第三方 App 参与 |
| 成本 | 依赖 Legado 的书源规则与网络可达性 | 需重写 Ktor 数据层、SQLCipher、Keystore、播放器 |
| 风险 | 书源令牌泄露 → 仅影响网关（可单独撤销） | 开发量显著更大 |

选择理由：本阶段目标是**跑通认证 → 解密 → 书源格式 → TTS 的端到端链路**。Legado 方案把
UI 与阅读器交互交给成熟客户端，后端只负责「认证 + 网关解密 + 格式转换」，加密边界清晰、
可验证。路线 B 作为独立后续阶段，届时复用本阶段的接口契约（ETag、ack、本地索引语义）。

> 安全前提：Legado 自身 Web 服务多数接口无认证，因此**不允许 Legado 直连后端内部接口**。
> 所有访问都必须经过 `/gateway/legado/*`，且该路径强制「网关会话 + 书源令牌」双层认证。

---

## 4. 服务端接口

### 4.1 新增业务接口（均要求登录 session，且校验 `books.owner_id`）

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/search/metadata?q=&limit=20` | 只查 Meilisearch 元数据索引；返回 `book_id/title/author/matched_field/highlight` |
| GET | `/api/books/{id}/chapters` | 目录：`index/title/chars/word_count/etag`，**不解密正文** |
| GET | `/api/books/{id}/chapters/{i}/plain` | 解密单章纯文本；支持 `If-None-Match`，未变更返回 **304** |
| POST | `/api/books/{id}/chapters/{i}/ack` | 客户端本地索引回执（幂等，非强制） |
| PATCH | `/api/books/{id}` | 只改可索引元数据（title/author/tags/description），触发重新索引 |

ETag 由「书籍密文身份（bookID + 明文 SHA-256 + chunkSize）+ 章节 rune 偏移」导出：
同一 bookID 下稳定，书籍重新加密（内容变化）必然变化。

### 4.2 Legado 网关（`/gateway/legado/*`）

| 方法 | 路径 | 认证 |
|---|---|---|
| POST | `/login` | 口令 → 网关会话（scope=legado，cookie `ns_gw_session` + 响应内 `session_token`） |
| POST | `/logout` | 网关会话 |
| GET | `/search?key=&page=` | **会话 + 令牌** |
| GET | `/book/{id}` | **会话 + 令牌** |
| GET | `/toc/{id}` | **会话 + 令牌** |
| GET | `/content/{id}/{index}` | **会话 + 令牌**（解密正文，返回 `{"content": "..."}`） |

令牌管理（需要 **api 会话**，令牌本身不能调用）：

```
POST   /api/admin/legado/tokens        {"name":"legado-20261125"} → 明文令牌（仅此一次）
GET    /api/admin/legado/tokens        列出（不含令牌本身）
DELETE /api/admin/legado/tokens/{id}   立即撤销
```

**双层认证语义**（`server.gatewayAuth`）：

1. 身份层：`ns_gw_session` cookie，或 `X-Legado-Session` / `Authorization: Bearer` 头部。
   该会话 `scope=legado`，**不能**用于 `/api/*`（`LookupSession` 只接受 `scope=api`）。
2. 令牌层：`X-Legado-Token` 必须命中未撤销的 `legado_tokens` 行，且与会话用户一致。

结果：令牌单独存在 → 401；会话单独存在 → 403；两者齐全 → 200。
令牌泄露的爆炸半径被限制在网关，且可单独撤销，不影响管理接口与其他设备。

---

## 5. Meilisearch 索引

**容器**（`docker-compose.yml`）：`getmeili/meilisearch:v1.12`，`mem_limit: 128m`，
**不发布任何宿主端口**，仅接入 `private` 内部网络；另设置
`MEILI_MAX_INDEXING_MEMORY=64Mb`、`MEILI_MAX_INDEXING_THREADS=1` 压住索引期内存峰值
（默认按 CPU 核数开线程，在 128MB 限制下会被 OOM kill）。

**索引设置**（`Meili.EnsureSchema`，先 `POST /indexes` 幂等建索引，再 `PATCH settings`）：

```json
{
  "searchableAttributes": ["title", "author", "tags", "description", "chapter_titles"],
  "displayedAttributes":  ["id","title","author","tags","description",
                           "chapter_titles","chapter_count","size_bytes","created_at"],
  "filterableAttributes": ["tags"],
  "sortableAttributes":   ["created_at"],
  "prefixSearch": "indexingTime",
  "typoTolerance": { "enabled": true }
}
```

`searchableAttributes` 只列元数据字段：**即使有人误写入正文字段，也不会被检索到**。

**文档**（`search.Document`，字段白名单 `AllowedDocFields`）：

```json
{ "id": "...", "title": "夜航船", "author": "阿来", "tags": ["海上","民国"],
  "description": "简介", "chapter_titles": ["第一章 夜航船","第二章 归途"],
  "chapter_count": 2, "size_bytes": 102400, "created_at": "2026-11-25T..." }
```

三重防线，任何一条被破坏都会在测试中失败：

1. `Document` 结构体字段与白名单一一对应（`TestDocumentFieldsMatchAllowlist`）。
2. 发送前 `ValidateDocumentJSON` 校验 JSON 字段名 ⊆ 白名单，出现 `content/text/body` 直接拒绝。
3. 索引 `searchableAttributes` 与 `displayedAttributes` 同样不含正文字段。

**同步策略**：

- 元数据变更（上传 / PATCH / 删除）→ `books.Service.Index`（`search.Service`）投递到队列，worker 串行 upsert/delete。
- 启动时执行一次 `SyncAll`（幂等兜底）：重建全部文档、清理 `DocIDs` 中已不存在的残留。
- Meilisearch 不可用时 `Search` **自动降级**为 SQLite `LIKE`（仍只匹配元数据列），
  响应带 `"degraded": true`，并在指标里暴露 `last_error`。

---

## 6. 客户端本地全文搜索

### 6.1 本阶段的参考实现（`clientcore`）

```
GET /api/books/{id}/chapters          → 目录 + 每章 ETag
GET /api/books/{id}/chapters/{i}/plain （If-None-Match）
     200 → 明文 + 新 ETag ／ 304 → 跳过（本地已最新）
        ↓
clientcore.Store.PutChapter  → 内存 SQLite（chapters 表 + FTS5 虚拟表）
        ↓
Search("词") → FTS5 MATCH（逐字分词 + 短语查询）→ bm25() 排序 → Go 侧生成 <mark> 片段
        ↓
Close() → 整库导出 JSON → AES-256-GCM（设备密钥）→ 原子写 index.enc
```

- **分词**：把正文按字加空格写入 FTS5（`unicode61`），查询同样逐字组成短语
  （`"推 开 门"`）。这样 1~3 个汉字的任意子串都能命中，不依赖外部分词器。
- **落盘**：磁盘上只有 `NSIDX1 || nonce || AES-GCM(快照)`，无明文索引、无正文。
  启动时解密回内存并重建 FTS 索引（私有书库体量下 < 100ms）。
- **不上传**：查询词、命中片段、索引结构都留在本机；只有 `/ack` 会告诉服务端
  「第几章已索引」，不含任何文本。

### 6.2 映射到路线 B（KMP + IReader 数据层）

| 本阶段参考实现 | 移动端落地 |
|---|---|
| `KeyProvider`（`FileKeyProvider`，Argon2id） | Android Keystore / iOS Keychain 包裹 32B 随机密钥，**不硬编码**、不写 SharedPreferences/UserDefaults |
| 内存 SQLite + FTS5（`modernc.org/sqlite`） | SQLCipher + FTS5（整体库加密，语义等价：磁盘无明文） |
| 加密快照 `index.enc` | SQLCipher 数据库文件本身 |
| `Client.ChapterPlain` + `If-None-Match` | Ktor 客户端同一语义（304 不重复传输） |
| `Store.Search` → `snippet()` | DAO 里 `SELECT snippet(...)` 或同等高亮逻辑 |
| TTS 流式（阶段二 `audio/wav` 片段） | 内存解密播放，明文不落盘 |

即：**数据层要替换的只有「密文 → 本地解密 → 加密库」这一段**，接口契约（ETag/ack/分片）
已经在本阶段固化并有测试覆盖。

---

## 7. 数据控制台指标

`GET /api/admin/metrics`（需登录）：

```json
{
  "books": {"total": 3, "chapters": 8, "acked_chapters": 5},
  "metadata_index": {"backend": "meilisearch", "index": "books", "health": "ok",
                     "documents": 3, "books": 3, "drift": 0, "last_error": ""},
  "search": {"window": 200, "samples": 42, "p50_ms": 1.8, "p95_ms": 6.4},
  "coverage": [{"book_id":"...","title":"夜航船","indexed_chapters":2,"total_chapters":2,"coverage":1}],
  "legado": {"enabled": true, "window": 200, "total": 12, "ok": 12, "success_rate": 1}
}
```

- `metadata_index.drift = documents - books`：非 0 说明索引与库不一致（同步失败/残留）。
- 搜索延迟来自 `search_logs`：只存 `q_len` 与 **HMAC-SHA256（密钥由 KEK 经 HKDF 派生）** 指纹，
  不含查询词，无法用字典反推。
- 覆盖度来自 `/ack` 回执（`chapter_acks` 去重计数），用于判断哪些章节已进客户端索引。

---

## 8. 配置项（第三阶段新增）

| 变量 | 默认 | 说明 |
|---|---|---|
| `MEILI_URL` | 空 | 空 = 不启用 Meilisearch，元数据检索降级为 SQLite 匹配 |
| `MEILI_MASTER_KEY` / `MEILI_MASTER_KEY_FILE` | 空 | 二选一；文件优先。至少 16 字节，不入库、不写日志 |
| `MEILI_INDEX` | `books` | 索引名 |
| `SEARCH_SYNC_ON_START` | `true` | 启动时全量重建索引（幂等兜底） |
| `SEARCH_TIMEOUT_MS` | `5000` | 单次索引/搜索请求超时 |
| `LEGADO_ENABLED` | `true` | 关闭后网关路由返回 503 |
| `LEGADO_SESSION_TTL_HOURS` | `720` | 网关会话有效期（与 `/api` 的 24h 会话分离） |

---

## 9. 快速开始

```bash
# 0. 配置
cp .env.example .env
#   ADMIN_INITIAL_PASSWORD=...
#   MEILI_MASTER_KEY=<openssl rand -base64 24>
python3 - <<'EOF'
import secrets,re
p='.env'; s=open(p).read()
s=re.sub(r'^MEILI_MASTER_KEY=.*$','MEILI_MASTER_KEY='+secrets.token_urlsafe(24),s,flags=re.M)
open(p,'w').write(s)
EOF

make kek                       # 生成 secrets/kek.v1（当前）与 secrets/kek.v2（预生成，轮换用）
sudo chown 65532:65532 secrets/kek.v1 secrets/kek.v2 && sudo chown -R 65532:65532 data
# 备份口令（至少 12 字符）：只放在文件里，绝不进命令行参数
openssl rand -base64 24 > secrets/backup.pass && chmod 600 secrets/backup.pass
mkdir -p imports               # 批量导入目录（compose 只读挂载为 /imports）

# 1. 测试
go vet ./... && go test ./...

# 2. 本地端到端演示（无需 Docker/Meilisearch）
make demo-search               # 分层搜索 + 客户端 FTS5 + 磁盘密文检查
make demo-tts                  # 第二阶段：朗读音频片段
make demo-lifecycle            # 第四阶段：导入 → 备份 → 演练 → 轮换 → 淘汰（全部在临时目录）

# 3. 容器启动（app + caddy + meilisearch）
make up                        # 访问 https://localhost:8443   控制台 /admin
```

### 导入 Legado 书源

```bash
BASE=https://localhost:8443 USER=admin PASS='<口令>' bash scripts/legado-setup.sh
# 产物：deploy/legado-book-source.local.json（含真实凭证，已 gitignore）

# 在 Legado：书源管理 → 本地导入 → 选择该 JSON
# 吊销令牌：
curl -k -b /tmp/cookies.txt -X DELETE https://localhost:8443/api/admin/legado/tokens/1
```

书源里的 `header` 同时携带两层凭证：

```json
{"X-Legado-Session":"<网关会话>","X-Legado-Token":"<书源令牌>"}
```

> 默认 Caddy 只绑定 `127.0.0.1:8443`（第一阶段约束）。手机访问需自建隧道（SSH/Tailscale）
> 或把端口映射改为局域网地址 —— 后者会扩大暴露面，README 不作默认建议。

### 客户端同步与检索

```bash
export NOVELCLIENT_PASSPHRASE='本地索引口令'
go run ./cmd/novelclient -base https://localhost:8443 -insecure -store ./local-reader \
  -password '<口令>' sync
go run ./cmd/novelclient ... search '琥珀色灯塔'
go run ./cmd/novelclient ... local        # 本地已缓存书籍与索引规模
```

---

## 10. 测试与验收

```bash
go vet ./...
go test ./...                                   # 全部单元 + 集成
go test ./internal/chapterscan/ -v
go test ./internal/search/ -v
go test ./internal/server/ -run 'Metadata|ChapterPlain|Legado|Client' -v
```

| 验收标准 | 覆盖位置 |
|---|---|
| 1 正文关键词在服务端元数据搜索无结果；书名/作者有结果 | `server/search_test.go: TestMetadataSearchExcludesBody` |
| 2 索引中不存在正文字段 | `search/meili_test.go: TestMeiliSchemaAndDocumentsExcludeContent`、`TestMetadataIndexDocumentsContainNoBody`、`TestValidateDocumentJSONRejectsContent` |
| 3 客户端解密章节 → 本地 FTS5 命中并高亮 | `server/search_test.go: TestClientLocalFullTextSearch`（真实拉章 + ETag + 快照） |
| 4 未登录访问 `/api/search/metadata`、`/plain` → 401 | `TestChapterPlainETagAndAuth` |
| 5 ETag 二次请求 304；内容变化后 ETag 变化 | 同上 |
| 6 网关无认证 401，令牌不能绕过会话 | `TestLegadoGatewayAuth`（401/401/403/200/撤销 403） |
| 7 `go test ./...` / `go vet ./...` 通过 | 上述命令 |
| 8 Meilisearch 不发布宿主端口、limit 生效 | `docker port` 为空；`docker inspect` limit=134217728 |

补充：

| 项 | 位置 |
|---|---|
| 流式扫描与整文本切分完全一致（含 CRLF/随机文本） | `chapterscan/scan_test.go` |
| 查询词不进审计（只记长度 + HMAC） | `search/search_test.go: TestSearchLogHasNoPlaintext` |
| 后端故障自动降级 | `search/meili_test.go: TestServiceDegradesWhenBackendDown` |
| 本地快照篡改/错误密钥必须失败 | `server/search_test.go: TestClientSnapshotTamper` |

容器级验收（含 Meilisearch 与网关）：

```bash
docker run --rm --network novelstack_private -v "$PWD":/src -w /src -e GOPROXY=off \
  golang:1.22-alpine go run ./scripts/acceptance \
  --base https://localhost:8443 --username admin --password '<口令>' \
  --tls-skip-verify --dial-addr caddy:8443
# 输出：healthz / 401 / login / 上传 / SHA-256 / 目录 / 明文+304 / 回执 /
#       元数据搜索（正文不命中）/ 指标 / 网关双层认证 / 审计 / 登出
```

实测结果（本机容器）：

- 元数据索引文档字段：`[author chapter_count chapter_titles created_at description id size_bytes tags title]`，无正文字段。
- `searchableAttributes = ["title","author","tags","description","chapter_titles"]`。
- 参考客户端经 Caddy（TLS）同步 3 章 → 本地 `search "他推开门"` 命中并返回 `<mark>` 高亮；
  二次同步 3 章全部 304 跳过；`index.enc` 中 `他推开门 / 夜航船` 出现次数均为 0。

---

## 11. 已知限制（第三阶段）

1. **未缓存章节不可全文搜索**：这是分层模型的必然结果。客户端需先 `sync`/`load` 该章。
2. 本地索引不跨设备同步；换设备需重新拉取（重新解密传输，安全上可接受）。
3. 本文的 `clientcore` 是**可执行参考实现**（Go + 内存 FTS5 + 加密快照），不是最终 App；
   路线 B（KMP + SQLCipher）按 §6.2 映射落地，接口契约已固定。
4. 元数据检索的中文分词依赖 Meilisearch 内置 CJK 分段；降级模式下仅 `LIKE` 子串匹配（无容错/前缀）。
5. ETag 基于「书籍密文身份 + 章节偏移」，不是章节级密文摘要：书籍不可变（重传即新 bookID），
   因此语义等价；若未来支持就地替换正文，需要引入章节级密文摘要。
6. 每次 `/plain` 都会解密整本书（≤20MB），未做明文缓存；这是有意的安全取舍。
7. 网关会话有效期默认 30 天，撤销令牌即时生效，但已签发的网关会话需等过期或调用 `/logout`。
8. Meilisearch 在 128MB 限制下需调低索引线程/内存（已在 compose 中设置）；若索引超大书库仍可能 OOM。
9. 沙箱环境的 Docker 端口发布不可用（`docker port` 为空），因此容器验收经内部网络完成；
   compose 的端口绑定配置本身为 `127.0.0.1:8443:8443`。

---

## 12. 前几个阶段（保持可用）

- 认证：首次启动用 `ADMIN_INITIAL_PASSWORD` 建唯一用户；登录限流 5 次/分钟；session 只存 token 的 SHA-256。
- 加密：KEK（第四阶段起为版本化 `secrets/kek.v1|kek.v2`，容器内 `/run/secrets/kek_v1|kek_v2`）→
  每书独立 DEK（AES-256-GCM 包装入库）→ 1MiB 分块加密，
  每块 AAD 绑定 `bookID + chunkIndex + 版本`；明文 SHA-256 入库复核；密文目录 `data/ciphertext`。
- 朗读：`GET /api/books/{id}/chapters/{i}/speak?voice=&speed=&mode=stream|sse`，
  片段级加密缓存（HKDF 派生音频 KEK）、逐段 `Flush()`、篡改即 409。
- 审计：`audit_logs` 只记动作/结果/摘要，不含正文；`/api/admin/audit` 可查。
- 迁移：v1 → v2（owner_id、TTS 表）→ v3（元数据列、目录、回执、搜索日志、网关令牌、会话作用域）
  → **v4（`tts_segments.last_access/kek_version`、合成统计、备份/轮换/演练记录表）**，
  启动自动执行；旧数据可继续读取，目录在首次访问时补齐。

---

## 13. 第四阶段：运营控制台与数据生命周期

四件事，按优先级：**A 备份与恢复** → **B KEK 轮换** → **C 只读控制台** → **D 批量导入与缓存治理**。

落地顺序与依赖（也是复查时的阅读顺序）：

| 顺序 | 内容 | 为什么排在这里 | 依赖 |
|---|---|---|---|
| 0 | 迁移 v4 + `app.New` 统一装配 + CLI 子命令骨架 | 后面四件事都要复用同一套加解密/缓存/审计 | 无 |
| A | `NSBKP` 包 → 备份 → 校验 → 演练 | 没有可验证的恢复，轮换就是不可逆操作；演练也顺带成为后续改动的回归手段 | 0 |
| B | 多版本 keyring → 音频格式 v2 → 轮换/抽样校验 | 依赖 A：先能备份，才敢动全部密文 | 0, A |
| C | dashboard 聚合 → go:embed 控制台 → 缓存清理端点 | 指标里要展示 A/B 的结果（最近备份、轮换记录） | A, B |
| D | 批量导入 → `last_access` → 淘汰策略 | 体量上来后才有缓存压力；淘汰要看 C 的缓存页 | 0, C（端点） |

每一步都先补测试再接线：`backup`/`kekrotate`/`dashboard`/`bulk`/`tts admin` 五个包各自独立可测，
不依赖 HTTP，也不依赖真实 TTS 引擎。

贯穿全部四件事的一条边界：**控制台不能解密**。`/admin` 只有聚合指标与「清缓存」这一个写操作，
没有任何解密入口；解密能力只存在于 `/api/books/...`（需要 api 会话）与 Legado 网关（双层认证）。

### 13.1 新增文件

```
novelstack/
├── internal/backup/            ★ 加密备份包
│   ├── format.go               ★ NSBKP 容器：Argon2id → AES-256-GCM 分块流
│   ├── archive.go              ★ tar 布局 + manifest + 安全解包（拒绝绝对路径/../符号链接）
│   ├── create.go               ★ VACUUM INTO 快照 → 清洗 → 打包 → SHA-256 伴生文件
│   ├── extract.go              ★ 校验 → 解包 → 完整性复核 → 失败即回滚（不留半成品）
│   ├── drill.go                ★ 一键恢复演练（临时实例 + 登录 + 下载 + 明文比对 + 记录）
│   └── *_test.go               ★ 往返/错口令/逐字节篡改/空库/缺密文/演练端到端
├── internal/kekrotate/         ★ KEK 轮换与轮换后校验
├── internal/ttscache/store.go  ★ 格式 v2（带 keyVersion）+ 多版本 keyring + Rewrap
├── internal/tts/admin.go       ★ 引用计数保护 / 缓存统计 / 清理 / 淘汰 / last_access 批提交
├── internal/dashboard/         ★ 控制台聚合层（单次请求返回全部指标，逐块降级）
├── internal/adminui/           ★ go:embed all:dist（Vite 构建产物）
├── internal/server/admin.go    ★ /admin 路由、登录页、dashboard、缓存清理/淘汰、审计过滤
├── internal/books/bulk.go      ★ 目录批量导入（明文 SHA-256 幂等）
├── internal/app/app.go         ★ 统一装配（多版本 keyring、TTS 服务、搜索、网关、HTTP handler）
├── internal/cli/               ★ 子命令：serve/backup/restore-drill/kek/import/cache
├── web/                        ★ Vue 3 + Vite 控制台源码（base=/admin/，outDir=internal/adminui/dist）
├── scripts/demo-lifecycle.sh   ★ 临时目录里跑完导入→备份→演练→轮换→淘汰
├── imports/                    ★ 批量导入投递目录（compose 只读挂载为 /imports）
├── docs/                       说明与截图目录（二进制截图仅在本地保留）
├── Dockerfile                  ★ 增加 RUNTIME_IMAGE 构建参数；预置可写 /tmp（演练需要）
└── Makefile                    ★ backup / backup-verify / restore-drill / kek-rotate / kek-verify /
                                   import / cache-prune / cache-clear / web / demo-lifecycle
```

### 13.2 备份包格式（`NSBKP`）

不用「tar + 单层口令加密」的现成格式，也不复制数据库文件：

| 决策 | 原因 |
|---|---|
| SQLite 用 `VACUUM INTO` 而不是 `cp app.db` | WAL 模式下直接复制会得到撕裂的库；`VACUUM INTO` 在只读事务里生成一致快照 |
| 快照先清洗再打包 | `DELETE FROM sessions`、`DELETE FROM legado_tokens`、`journal_mode=DELETE`、`VACUUM`、`integrity_check`：备份不该携带可用会话 |
| 整包 AES-256-GCM 分块加密 | 单次 `Seal` 受内存限制；分块可流式读写，AAD 绑定块序号，乱序/拼接/截断一定失败 |
| Argon2id 派生包密钥，参数写进头部 | 换机器、换工具仍可解；参数可随硬件演进调高而不破坏旧包 |
| **备份口令 ≠ 主 KEK** | 备份包泄露不应等于书籍泄露：包内仍是 KEK 包裹的 DEK 与密文，解密需要 KEK 与包口令两把钥匙 |
| KEK **绝不进包**，离线备份是人工步骤 | 自动化把 KEK 写进包，等于把「加密备份」降级为「混淆备份」 |

包结构（全部为小端）：

```
header (37 B): "NSBKP" | ver=1 | kdf=1(argon2id) | time u32 | memory u32(KiB) | threads u8
               | compression u8 (0=none,1=gzip) | salt 16B | chunkSize u32
body        : [ nonce 12B | ciphertext+tag ] × N     AAD = header || chunkIndex
tar (PAX)   : manifest.json
              data/app.db
              data/ciphertext/<book_id>/...
              data/tts-cache/<xx>/<cache_key>.nsa
              data/client-index/...        （存在才打）
```

`manifest.json` 记录 `format_version / created_at / tool / data_layout / books / chapters /
segments / ciphertext_bytes / tts_cache_bytes / client_index_files / db_bytes / db_sha256 /
kek_versions / book_list / orphan_files / notes`。恢复端的完整性复核全部基于它：
`db_sha256` 对得上、`integrity_check=ok`、`book_list` 里每本书的密文都在。

### 13.3 备份

```bash
# make kek 已顺手生成 secrets/backup.pass；也可自行替换成更强的口令
make backup            # 容器在跑 → 一次性容器内执行；否则用本地二进制
# backup=/data/backups/novelstack-20261126-121500.nsb
# sha256=…  size_bytes=…  books=2 segments=37 duration_ms=210
make backup-verify     # 读 .sha256 伴生文件 + 完整试解包
# ok sha256=… size_bytes=… books=2 segments=37 files=6
```

- 口令只从 `--passphrase-file` 或 `NOVELSTACK_BACKUP_PASSPHRASE` 读取，**不接受命令行参数**
  （避免 `ps`/history 泄漏）；容器场景用管道喂 `--passphrase-file /dev/stdin`。
- 输出同时写 `<pkg>.sha256` 伴生文件；`backup_runs` 表与审计 `backup_created` 各记一条。
- 空书库可以备份（`books=0`，演练会以 `no_book_to_verify` 记录）；**有元数据但密文缺失**会直接报
  `ErrMissingCiphertext` 并列出 `orphan_files`，不生成「看起来成功」的包。
- 备份目录默认 `<data>/backups`，随 `./data` 落在宿主；`*.nsb` 已 gitignore，但它仍是敏感文件，介质要加密。

**KEK 离线备份（人工，不写进任何自动化）**：把 `secrets/kek.v1`（以及预生成的 `kek.v2`）
按「打印成纸 / 写入硬件密钥 / 放离线介质」三选二保存，并在**另一台设备**上验证能读出来。
包 + 口令都在机器上时，能恢复的只有「别人也能恢复」的备份。

### 13.4 恢复演练（一条命令）

```bash
make restore-drill DRILL_PASSWORD='<登录口令>'
# 或：NOVELSTACK_DRILL_PASSWORD=... make restore-drill
```

内部固定七步，任一步失败即非零退出：

1. 读伴生 `.sha256` 并校验整包（可选 `--checksum` 指定期望值）；
2. 解包到**临时目录**（`os.MkdirTemp`，与生产数据目录无关）；
3. 复核 `db_sha256` + `integrity_check` + 每本书密文存在；
4. 用备份内的数据库 + 备份的 KEK 起一个**临时实例**（随机空闲端口、独立数据目录、
   `TTS_PROVIDER=off`、`MEILI_URL=` 空、`LEGADO_ENABLED=false`，不碰生产数据）；
5. 用备份库里的账号口令登录（cookie jar，`/api/login`）；
6. 下载一本书的明文，与备份清单里记录的明文 SHA-256 比对；
7. 关停临时实例、删除临时目录，追加演练记录。

记录落到 `<data>/backups/drill-log.jsonl`（每行一条）并写 `drill_runs` 表：

```json
{"ts":"2026-11-26T12:16:02Z","package":"novelstack-20261126-121500.nsb",
 "package_sha256":"b4e6a504…","book_id":"7663d36488c5df76…",
 "expected_sha256":"affc7c3b…","actual_sha256":"affc7c3b…",
 "result":"ok","reason":"","duration_ms":1840}
```

失败时 `result":"failed"`、`reason` 取 `extract_failed / verify_failed / launch_failed /
login_failed / download_failed / sha_mismatch / no_book_to_verify`，命令退出码 1。
演练只用备份里的数据，因此**错误口令 / 篡改包一定失败，且不会写回生产目录**
（`Extract` 在出错时删除已解出的条目）。

真实恢复（不是演练）的步骤：`make down` → 备份 `./data` 整目录 →
`novelstack backup verify` 校验包 → 解包后用 `data/app.db` + `data/ciphertext` 覆盖 →
`make up`。KEK 必须是当时那一版（或用 `KEK_OLD_PATHS` 双密钥启动）。

### 13.5 KEK 轮换

轮换把「每本书的 DEK」和「TTS 音频片段的包密钥」全部从旧主密钥重包一遍。三件事必须同时成立：

1. **幂等**：按 `kek_version` 跳过已完成的行，重跑安全；
2. **可中断**：逐本一个事务，进程被杀后用同一命令重跑即可继续；
3. **窗口内可读**：服务同时加载新旧 KEK，轮换期间读旧行和新行都不失败。

文件与版本约定：

```
secrets/kek.v1   当前（= compose secret /run/secrets/kek_v1）
secrets/kek.v2   make kek 时预生成的下一版（/run/secrets/kek_v2）
轮换完成后：KEK_PATH=/run/secrets/kek_v2  KEK_VERSION=2
            KEK_OLD_PATHS=1=/run/secrets/kek_v1   ← 旧版本只读加载，代码永不删除它
```

操作顺序（**先开窗口，再轮换，最后收口**）：

```bash
# 1) 开双密钥窗口：.env 改成
#    KEK_PATH=/run/secrets/kek_v1
#    KEK_VERSION=1
#    KEK_OLD_PATHS=2=/run/secrets/kek_v2
docker compose up -d            # 此刻服务能读 v1 与 v2 两种行

# 2) 轮换（一次性容器，幂等；建议低峰执行，窗口内避免上传/朗读等新写入）
make kek-rotate
# ==> 轮换 v1 → v2（幂等，可重跑）
# rotated from=v1 to=v2 books=2 skipped=0 segments=37 completed=true

# 3) 抽样校验：按 kek_version 分组，每组随机抽 3 本解密到底
make kek-verify

# 4) 收口：.env 改成 KEK_PATH=/run/secrets/kek_v2、KEK_VERSION=2、
#    KEK_OLD_PATHS=1=/run/secrets/kek_v1 → docker compose up -d
#    观察 24h（日志无 decrypt 失败）后，再把 kek.v1 归档到离线介质
```

细节与坑：

- 轮换目标是 `books.wrapped_dek`（更新 `kek_version`）与 `data/tts-cache/*.nsa`（格式 v2 头里的
  `keyVersion` 字节）；`tts_segments.kek_version` 同步更新，避免控制台分布与实际文件不一致。
- 遇到「第三种版本」直接报错退出（说明有并发写入或历史遗留），不会盲改。
- 轮换只对**已存在**的行生效；窗口内新上传的书用 `KEK_VERSION` 指定的版本写入，因此**务必在窗口期暂停上传**，
  否则新书会落在 v1、需要再跑一次轮换（命令幂等，再跑一次即可）。
- 旧 KEK 由人工归档；`kekrotate` 里没有任何删除密钥的代码路径。
- 音频缓存 v2 头部多一个 `keyVersion` 字节；v1 旧文件仍可读（缺省按 v1 处理）。缺对应 KEK 版本的
  音频文件按**篡改**处理（返回 409，绝不返回可疑音频）。

### 13.6 只读运营控制台 `/admin`

Vue 3 + Vite 构建成静态资源，经 `//go:embed all:dist` 打进同一个 Go 二进制，
由现有 `net/http` 服务在 `/admin` 下提供；**没有 Node 运行时、没有 SSR、没有额外进程**。

```bash
make web        # pnpm --dir web build → internal/adminui/dist
make build      # 产物被 go:embed 内嵌
```

界面（实机截图，容器 + Caddy + mock TTS，KEK 已完成 v1→v2 轮换）：

| 概览 | 加密健康度 |
|---|---|
| （截图略，本地 docs/screenshots/ 保留） | （截图略，本地 docs/screenshots/ 保留） |

| 缓存管理（二次确认） | 审计（运维动作留痕） |
|---|---|
| （截图略，本地 docs/screenshots/ 保留） | （截图略，本地 docs/screenshots/ 保留） |

| TTS 朗读 | 二次确认弹窗 |
|---|---|
| （截图略，本地 docs/screenshots/ 保留） | （截图略，本地 docs/screenshots/ 保留） |

| 页面 | 内容 |
|---|---|
| 概览 | 书籍数/已删、密文字节与文件数、密文漂移、TTS 片段数与体积、索引漂移、最近一次备份（时间+校验和+结果）、最近演练 |
| 加密健康度 | 当前 KEK 版本、已加载版本列表、各版本书籍分布、最近一次轮换记录、24h/累计解密失败数 |
| TTS | provider 名称与健康、缓存命中率、最近窗口合成次数/成功率、合成延迟 P50/P95、朗读轮次与命中 |
| 搜索 | 后端/索引/健康、文档数与漂移、元数据检索 P50/P95、客户端覆盖度（acked/total） |
| 审计 | 最近 100 条，可按 action 前缀过滤；只显示摘要、来源 IP、结果，**不含正文/原始查询词** |
| 缓存管理 | 总量/片段数/按书占用、手动清理（二次确认 + 审计）、按上限/天数淘汰（可 dry-run） |

接口（全部要求 `scope=api` 的登录会话）：

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/admin`, `/admin/{page}` | SPA 外壳；未登录 303 → `/admin/login` |
| GET | `/admin/login` | 独立的最小登录页（内联 HTML + `fetch /api/login`），**不依赖前端构建产物** |
| GET | `/admin/assets/{path...}` | 内嵌静态资源（`Cache-Control: public, max-age=3600`，内容哈希命名） |
| GET | `/api/admin/dashboard` | 单次请求返回全部指标；任一块失败只降级该块（带 `health/error`），不整体 500 |
| POST | `/api/admin/cache/clear` | `{"book_id":"…"}` 或 `{"all":true}`，必须带 `"confirm":true`；写审计 `cache_cleared` |
| POST | `/api/admin/cache/prune` | `{"max_bytes":…,"keep_days":…,"dry_run":true,"confirm":true}`；写审计 `cache_pruned` |
| GET | `/api/admin/audit` | 支持 `action`（前缀，如 `backup_`）、`result`、`before_id`、`limit`（≤200） |

安全声明（也是测试断言）：

- 控制台侧没有解密调用：`dashboard` 包只做 SQL 聚合与文件大小统计，响应 JSON 里没有
  `content/plaintext/body/dek/secret/query` 字段（`dashboard_test.go`）。
- 审计表里没有正文：`audit_logs` 只有 `action/result/summary`，搜索日志只存 `q_len` 与 HMAC 指纹前缀。
- 静态资源可匿名获取（不含数据），但 `/api/admin/*` 与 SPA 外壳都要会话；未登录访问 `/admin` 是 303 到登录页，
  直接打 `/api/admin/dashboard` 是 401。

### 13.7 批量导入

```bash
cp ~/novels/*.txt imports/          # compose 把 ./imports 只读挂载为 /imports
make import
# imported 夜航船.txt -> 7663d36488c5df76…
# skipped  夜航船-副本.txt (duplicate sha256)
# scanned=3 imported=2 skipped=1 failed=0 dry_run=false
```

- `make import` 在容器模式下把 `./imports`（只读挂载）当输入目录；本地模式用 `IMPORT_DIR=...`。
- 复用上传的加密封存流程（`books.storeEncrypted`）：分块加密、目录流式扫描、元数据索引投递、
  审计动作 `bulk_import`；**不支持 EPUB**（避免引入转换依赖与解析攻击面）。
- 幂等键是**明文 SHA-256**：同内容文件（哪怕改了名）第二次出现标记 `skipped`，
  同批次内的重复也按顺序只收第一份；`FindBySHA256` 会同时看已删除书籍，避免「删了又被导回来」。
- 失败原因写审计（`bulk_import` + `result=error` + `reason=…`），只把文件名与原因打到 stdout，
  不把正文写日志。
- `--dry-run` 只扫描 + 查重，用来先看会新增多少。

### 13.8 缓存生命周期

`tts_segments` 迁移 v4 新增 `last_access TEXT`（回填 `COALESCE(last_hit_at, created_at)`）与
`kek_version INTEGER`，并建 `idx_tts_segments_access`。访问时间不是每片都写库：

- `MarkAccess` 只把 key 放进内存待写集合，`Speak` 每 8 段或结束时 `FlushAccess` 批量
  `UPDATE … WHERE cache_key IN (…)`（每条语句最多 400 个 key），把写放大压到 1/8 以下；
- 流式播放期间对整章片段做**引用计数**（`acquireKeys/release`），正在被读的片段即使命中淘汰策略也不删；
- 淘汰另外保留 `CACHE_PROTECT_MINUTES`（默认 5 分钟）的最近访问保护窗口，防止「刚播完就被删 → 用户回听 404」。

策略与入口：

| 方式 | 命令 / 接口 | 说明 |
|---|---|---|
| 按总大小上限 | `make cache-prune CACHE_MAX_MB=512 DRY_RUN=0` / `POST /api/admin/cache/prune` | 超上限时按 `last_access` 从旧到新删，直到回到上限以下 |
| 按保留天数 | `make cache-prune CACHE_KEEP_DAYS=30 DRY_RUN=0` | 删除 `last_access` 早于 N 天的片段 |
| 手工清理 | `make cache-clear BOOK_ID=…` / `ALL=1`，或控制台缓存页 | 二次确认；只受引用计数保护（不受 5 分钟窗口限制，否则刚播完就清不掉） |

**默认不做自动淘汰**：私人站写入量小，自动删除只会带来「回听 404」的坏体验；上限/天数只在被显式触发时生效
（控制台不传参时回落到 `CACHE_MAX_MB` / `CACHE_KEEP_DAYS`）。清理会同时删文件与 `tts_segments` 行；
已被删除书籍留下的孤儿行在「全量清理」时一并回收。

### 13.9 第四阶段配置项

| 变量 | 默认 | 说明 |
|---|---|---|
| `KEK_PATH` | `/run/secrets/kek_v1` | 当前 KEK 的只读文件（compose secret） |
| `KEK_VERSION` | `1` | 新写入使用的版本（1..255），写入 `books.kek_version` / 音频头 |
| `KEK_OLD_PATHS` | 空 | 轮换窗口期只读加载的旧版本，`1=/run/secrets/kek_v1,2=…` |
| `CACHE_PROTECT_MINUTES` | `5` | 淘汰保护窗口（1..1440） |
| `CACHE_MAX_MB` | `512`（模板值） | 缓存上限；`0` = 不按大小淘汰 |
| `CACHE_KEEP_DAYS` | `30`（模板值） | 保留天数；`0` = 不按时间淘汰 |

CLI 还接受 `--data-dir`（默认 `$DATA_DIR` 或 `/data`）、`--json`，便于脚本消费。

### 13.10 新增审计动作

| action | 触发 | 摘要内容 |
|---|---|---|
| `backup_created` | `backup create` | 包名、SHA-256、大小、书籍/片段数 |
| `kek_rotated` | `kek rotate` | from/to 版本、书籍数、片段数、是否完成 |
| `cache_cleared` | 控制台/CLI 清缓存 | 范围（book/all）、删除数、释放字节、受保护数 |
| `cache_pruned` | 控制台/CLI 淘汰 | 策略参数、删除数、前后体积 |
| `bulk_import` | 批量导入（每本一条） | 文件名、book_id、结果、失败原因 |

演练结果不写 `audit_logs` 而写 `drill_runs` + JSONL（属于运维例行动作，避免淹没审计流）。

### 13.11 第四阶段测试与验收

```bash
go vet ./... && go test ./...
go test ./internal/backup/ -v        # 包格式 / 创建 / 解包 / 演练
go test ./internal/kekrotate/ -v     # 轮换 / 幂等 / 断点续跑 / 抽样校验
go test ./internal/dashboard/ -v     # 聚合 + 无明文
go test ./internal/server/ -run Admin -v
go test ./internal/books/ -run Import -v
go test ./internal/tts/ -run Cache -v
```

| 验收标准 | 覆盖位置 |
|---|---|
| A 加密往返；错口令失败；任一字节篡改失败 | `backup/format_test.go`（含逐偏移篡改与截断） |
| A 备份→演练→明文 SHA 一致；失败不留半成品 | `backup/create_test.go`、`backup/drill_test.go` |
| A 空库可备份；有元数据无密文必须报错 | `TestCreateEmptyLibrary`、`TestCreateMissingCiphertext` |
| B 新 KEK 可解、旧 KEK 解不开、`kek_version` 正确 | `kekrotate/rotate_test.go` |
| B 中断后重跑一致；重复执行幂等 | `TestRotateInterruptedResume`、`TestRotateIdempotent` |
| C 静态资源随二进制发布；未登录 → 登录页/401 | `server/admin_test.go` |
| C dashboard 无正文/查询词/密钥字段 | `dashboard/dashboard_test.go`、`server/admin_test.go` |
| C 清缓存需登录、体积下降、写审计 | `server/admin_test.go` |
| D 3 个文件含 1 个重复 → 导入 2、跳过 1；二次运行全跳过 | `books/bulk_test.go` |
| D 超上限淘汰最旧、保留最近访问、跳过在用片段 | `tts/admin_test.go` |
| 容器内可执行 | `make backup` / `make restore-drill` / `make kek-rotate`（`docker compose run --rm` 一次性容器） |

容器级验收（本机实测；沙箱环境不发布宿主端口，因此经 docker 内部网络访问）：

```bash
make up
# 宿主端口不可用时，用同一网络的临时容器跑验收（Host 必须是 localhost，Caddy 按站点名签发证书）
CGO_ENABLED=0 go build -o /tmp/acc ./scripts/acceptance
docker run --rm --network novelstack_private -v /tmp/acc:/acc:ro alpine:3.20 /acc \
  --base https://localhost:8443 --dial-addr caddy:8443 --sni localhost \
  --tls-skip-verify --username admin --password '<口令>'

docker compose exec -T app /app/novelstack help | head -5   # 子命令随镜像发布
make backup && make backup-verify                            # 一次性容器内执行
make restore-drill DRILL_PASSWORD='<口令>'                   # 临时实例，533ms 完成
make kek-rotate && make kek-verify                           # books=2 segments=2 → 全部 ok
make import && make cache-prune && make cache-clear ALL=1
```

### 13.12 已知限制（第四阶段）

1. **备份没有增量**：每次都是完整包（书库体量下几十 MB 起）。`gzip` 可开，但没有块级去重。
2. 备份包口令丢失 = 包作废（Argon2id 参数写在头部，无法暴力缩短）；KEK 丢失 = **所有**备份一起作废。
   两者都不是技术问题，是流程问题，所以 README 只写人工步骤、不写「自动托管」。
3. 演练验证一本书（默认第一本，可用 `--book` 指定）。这是抽样而非全量：全量校验等同完整恢复，代价太大。
4. 轮换期间若仍有上传，新书会落在旧版本，需要再跑一次（幂等）；要绝对干净就暂停写入。
5. 控制台只读 + 清缓存，**不能**上传/删除/改元数据；这些仍走 `/api/*`（有鉴权与审计）。
6. 控制台前端产物在仓库内（`internal/adminui/dist`），改前端后必须 `make web` 才能进二进制；
   Dockerfile 在缺失时直接构建失败，避免发布没有界面的镜像。
7. 缓存 `last_access` 依赖进程内批量提交：进程被 kill 时最多丢失最近约 8 个片段的访问时间戳，
   只影响淘汰顺序，不影响可读性。
8. 导入只处理 `.txt`，编码按 UTF-8 处理（GBK 需先转码）；不做 EPUB/MOBI。
9. 运行镜像默认 `gcr.io/distroless/static-debian12:nonroot`；若网络拉不到 gcr.io（或需要内网镜像），
   用 `RUNTIME_IMAGE=<你的镜像> docker compose up -d --build` 覆盖即可（`make up` 同样生效），
   不要为此改 Dockerfile。

---

## 14. 第五阶段：移动端 App（路线 B）

一句话：**服务端新增的 10 个端点没有一个返回正文明文**；App 拿到的是「元数据 + 密文包 +
被设备公钥包装的 DEK」，明文只在设备内产生，落盘也只落在 SQLCipher 里。

### 14.1 技术选型（未替换）

| 层 | 选择 | 说明 |
|---|---|---|
| 语言/UI | Kotlin Multiplatform + Compose Multiplatform 1.7 | `shared` 放业务与 UI，`androidApp` 只做壳 |
| 架构 | Clean-ish + Koin + Ktor + SQLDelight | 与 IReader 骨架同构；`AppState` 单入口，UI 只读状态 |
| 本地库 | SQLCipher（`net.zetetic:sqlcipher-android`）+ FTS5 | 通过 `SupportOpenHelperFactory` 注入口令 |
| 密钥 | Android Keystore（RSA-2048 + AES-256-GCM），私钥不可导出 | 库里没有明文密钥，也不写 SharedPreferences |
| 目标平台 | Android 已实现并构建通过；iOS 保留 target 与 actual 占位 | 第五阶段只验收 Android |

### 14.2 密钥层级：三层，谁包谁

```
Android Keystore（硬件-backed，私钥永不导出）
├── alias novelstack-device-rsa-v1   RSA-2048 / OAEP-SHA256
│     ├── 解开 manifest / prefetch 里的 wrapped_dek  → 书籍 DEK(32B) → 解 NSCH 章节包
│     └── 解开音频段里的 wrapped_dek                → 音频 DEK(32B) → 解 NSAC 片段
└── alias novelstack-local-aes-v1    AES-256-GCM
      └── 加密 filesDir/reader.key 中的 32 字节随机口令（格式 [ivLen][iv][ct]）
            └── SQLCipher 用它打开 reader.db
                  （books / chapters 明文缓存 / chapters_fts / progress / kv）
```

推论（也是验收点）：

- **库密钥不在代码里**：`reader.key` 只是被 Keystore 包起来的随机数，换机/清 Keystore 即失效。
- **服务端拿不到 DEK**：`wrapped_dek` 只能用设备私钥解，服务端只存 `public_key_pem`。
- **device token 存在 `kv` 表**（SQLCipher 内），不在 SharedPreferences/DataStore/日志里。
- 明文出现的位置只有三处：`reader.db`（加密页）、进程内存、Compose 文本。音频明文**只**在内存。

### 14.3 新增服务端接口

| 方法/路径 | 鉴权 | 返回 | 说明 |
|---|---|---|---|
| `POST /api/device/token` | 无（用户名密码 **或** 配对码） | `token`(仅此一次) + `device` | `nsd_` 前缀；服务端只存 SHA-256 |
| `POST /api/admin/pairing` | session | `nsp_...` 短码 | 10 分钟、一次性 |
| `GET /api/admin/devices` | session | 列表 + 有效/撤销计数 | 只有 `token_prefix`，没有明文令牌 |
| `DELETE /api/admin/devices/{id}` | session | `{ok:true}` | 幂等；撤销后该令牌立即 401 |
| `GET /api/sync/manifest?since=<cursor>` | session **或** device | 书籍元数据 + 章节 ETag + `wrapped_dek` | `since=0` 全量；`reset_required` 表示游标越界 |
| `PUT /api/books/{id}/progress` | session 或 device | 最新进度 | 最后写入胜；`percent` 为 0..1 的比例 |
| `GET /api/books/{id}/progress` | session 或 device | 进度或 404 | 404 = 没读过，客户端从头开始 |
| `POST /api/books/{id}/chapters/prefetch` | session 或 device | 每章 `ciphertext`(NSCH, base64)+`etag` | **限流**；单次 ≤ `PREFETCH_MAX_CHAPTERS` |
| `GET /api/books/{id}/chapters/{i}/audio` | session 或 device | 片段元信息 + `wrapped_dek` | 不返回音频字节 |
| `POST /api/books/{id}/chapters/{i}/audio/prepare` | session 或 device | 合成缺失片段（加密落盘） | 独立小额度限流 |
| `GET /api/tts/segments/{key}` | session 或 device | **NSAC 文件原样字节** | 需拥有该书；他人 key → 404 |

约定：

- 章节密文包 `NSCH`：`28B 头 + nonce(12) + ct`，AAD = `"novelstack-mobile-chapter-v1|" + bookID + "|" + header[0:16]`，
  头部每个字节都被认证（改 index/plainLen/flags 都会解密失败）。
- 音频沿用第二阶段的 NSAC v2：`ct_offset/ct_len` 指向文件内的密文区间，AAD = `"novelstack-tts-audio-v1|" + key`。
- 没有任何 `/api/mobile/*` 的明文捷径；老的 `/chapters/{i}/plain` 仍然只给浏览器会话用。

### 14.4 双凭证：device token ≠ 浏览器 session

| | 浏览器 session | device token |
|---|---|---|
| 载体 | `HttpOnly` Cookie | `Authorization: Bearer nsd_...` |
| 生命周期 | 滑动过期，登出即废 | 长期（`DEVICE_TOKEN_TTL_DAYS`，默认 180 天），可单设备撤销 |
| 能访问 `/api/admin/*`、`/admin`、上传/删除 | ✅ | ❌ **403** |
| 能同步/预取/报进度/取音频 | ✅ | ✅ |
| 撤销方式 | `/api/logout` | 控制台「设备与配对」或 `DELETE /api/admin/devices/{id}` |

实现要点：`requireAuth` 先看 Cookie 再看 `Bearer nsd_`；`requireSession` 遇到 `nsd_` 前缀**直接 403**
（不是 401/303，避免客户端误以为要重新登录）。撤销设备不影响浏览器会话，反之亦然——两侧都有测试。

### 14.5 配对流程

```text
控制台 /admin → 设备与配对 → 「生成配对码」
   ↓ 显示 nsp_xxxxxxxx（10 分钟内有效，只能用一次）
手机 App → 服务端填 http://10.0.2.2:8080 → 选「一次性配对码」→ 粘贴 → 配对
   ↓ POST /api/device/token {pairing_code, device_name, public_key}
App 得到 nsd_...（写入 SQLCipher 的 kv 表），此后所有请求带 Bearer
```

也可以直接用「用户名 + 密码」换令牌（`POST /api/device/token`），适合首次自助绑定；
两条路径都会记录 `public_key`，没有公钥的设备只能同步元数据、解不开章节。

> 控制台「设备与配对」：有效/已撤销计数、设备列表（**只显示 `nsd_` 前缀**，库里只有 SHA-256）、
> 一键撤销。撤销一台设备不影响其他设备与浏览器登录。
>
> （截图略，本地 docs/screenshots/ 保留）

### 14.6 同步与离线缓存

```text
同步（手动点「同步」或启动时）
  cursor = kv["cursor"] ?: 0
  GET /api/sync/manifest?since=cursor
    ├─ reset_required=true → 清空 books/chapters，cursor=0 重来
    ├─ books[]  → upsert books + upsertChapterMeta（标题/ETag，不覆盖已缓存正文）
    ├─ deleted[]→ deleteBook + deleteChapters
    └─ cursor   → kv["cursor"]

打开书
  本地有该章？→ 直接渲染（离线可用）
  没有？      → prefetch([index]) → unwrapDEK → ChapterCipher.open → 写 chapters(+FTS)
                → 顺手预取后 3 章（prefetchAhead）

本地搜索
  chapters_fts MATCH（trigram）→ snippet() 高亮 → 点结果 openAt(bookId, chapter)
  降级链：trigram → unicode61 → LIKE（SQLCipher 未编 FTS5 时）
```

- 游标是 `books.sync_seq`（单调整数），不是时间戳：同一毫秒多次写入也不会漏。
- 删除的书只在增量里出现，全量里没有 → 客户端用「全量时清空」保证一致。
- 缓存写入与 FTS 写入在同一个事务里，正文与索引不会错位。

### 14.7 阅读进度与冲突策略

- 客户端每翻页/滚动节流写本地 `progress`（`updated_at`），**800ms 防抖**后 `PUT` 上报，
  成功后 `markProgressSynced`；离线时留在 `selectUnsyncedProgress`，下次同步补报。
- 服务端 `reading_progress` 以 `(owner_id, book_id)` 为主键，**最后写入胜**（UPSERT），
  记录 `device_id` 与 `updated_at`，控制台「最近阅读」直接读它。
- 冲突策略是**明确选择的**：单用户多设备同时读同一本时，后到的写入覆盖先到的，
  不弹窗、不合并——因为「读到哪」没有可合并语义，弹窗比覆盖更烦。要避免就用同一台设备续读。
- `GET` 无记录返回 404，客户端据此从第 0 章开始，而不是把 404 当错误。
- 反向续读：本机 `progress` 为空（新设备/重装/刚清除数据）时，`openBook` 会异步 `GET` 服务端进度，
  落到对应章节并写回本地；用户已经开始翻页就不再抢方向。取不到（离线/404）静默降级为第 0 章。

### 14.8 预取与限流

| 限制 | 默认 | 配置 | 触发后的行为 |
|---|---|---|---|
| 单次请求章节数 | 8 | `PREFETCH_MAX_CHAPTERS` (1..50) | 400，提示上限 |
| 单次响应字节 | 8 MB | `PREFETCH_MAX_MB` (1..64) | 提前截断，`truncated=true` |
| 每分钟预取次数 | 40 | `PREFETCH_PER_MINUTE` (1..600) | 429 + `Retry-After` |
| 音频合成 | `max(2, 每分钟/4)` = 10/min | 同上派生 | 429 + `Retry-After` |

- 限流按**凭证**分桶：`d:<deviceID>` 与 `s:<username>` 互不影响（撤销一台设备不会连累浏览器）。
- 令牌桶（`internal/mobile/limiter.go`），支持注入时钟做单测；空桶后按补充速率等待。
- 预取只按显式 `indexes` 取，**没有「整本下载」接口**。

### 14.9 App 内朗读：解密只在内存

```text
POST .../chapters/{i}/audio/prepare     合成缺失片段（服务端加密落盘，返回数量）
GET  .../chapters/{i}/audio             每段：key / ct_offset / ct_len / audio_nonce / wrapped_dek
GET  /api/tts/segments/{key}            整段 NSAC 文件字节（密文）
   ↓ keystore.unwrapDEK(wrapped_dek) → 音频 DEK
   ↓ AES-GCM(open, ct=file[off:off+len], aad="novelstack-tts-audio-v1|"+key)
   ↓ parseWav(16-bit PCM) → AudioTrack MODE_STREAM 顺序播放
```

- 暂停/继续用 `AudioTrack.pause()/play()`，语速用 `PlaybackParams.setSpeed()`（0.5×–2.0×）。
- 页面上滑走 / `MainActivity.onStop` → 立即 `stop()` 并释放 `AudioTrack`。
- **App 私有目录里不会出现 `.wav`**：解密后的 PCM 只存在于 `ByteArray` 与音频缓冲区；
  每一段用完即 `zeroBytes`。

### 14.10 客户端文件（新增）

```
android/
├── settings.gradle.kts / build.gradle.kts / gradle.properties / local.properties
├── gradle/libs.versions.toml            版本目录（AGP 8.7.3 / Kotlin 2.0.21 / Ktor 3.0.3）
├── gradlew + gradle/wrapper/            Gradle 8.11.1 wrapper
├── shared/
│   ├── build.gradle.kts                 KMP + Android library + SQLDelight(sqlite-3-38 方言)
│   └── src/
│       ├── commonMain/sqldelight/…/ReaderDatabase.sq     books/chapters/progress/kv
│       ├── commonMain/kotlin/…/
│       │   ├── model/Models.kt          DTO（与 §14.3 一一对应）
│       │   ├── net/NovelApi.kt          Ktor 客户端；normalizeServer；无日志插件
│       │   ├── crypto/AesGcm.kt         expect object（AES-256-GCM）
│       │   ├── crypto/ChapterCipher.kt  NSCH 解包（AAD 与服务端逐字节一致）
│       │   ├── db/RawDb.kt              FTS5 运行时 DDL/DML 的 raw 执行器
│       │   ├── db/ReaderRepository.kt   FTS 降级初始化、章节缓存、搜索、进度、wipe
│       │   ├── tts/Wav.kt               PCM16 WAV 解析
│       │   ├── tts/TtsController.kt     prepare→manifest→取密文→解内存→AudioTrack
│       │   ├── app/AppState.kt          登录/同步/预取/阅读/进度/搜索/清除
│       │   ├── di/Modules.kt            Koin
│       │   └── ui/                      App/Login/Library/Reader/Search/Settings/Theme
│       ├── androidMain/kotlin/…/
│       │   ├── platform/AndroidDeviceKeyStore.kt   Keystore RSA+AES、reader.key、wipe
│       │   ├── platform/AndroidAudioSink.kt        AudioTrack 流式播放
│       │   ├── data/AndroidReaderStore.kt          SQLCipher + AndroidSqliteDriver
│       │   ├── crypto/AesGcm.android.kt            JCA 实现
│       │   ├── net/HttpClient.android.kt           OkHttp 引擎（不装日志插件）
│       │   └── di/AndroidModules.kt
│       ├── iosMain/kotlin/…/crypto/AesGcm.ios.kt   actual 占位（明确抛「未实现」）
│       └── commonTest/kotlin/…                    章节互操作/WAV/地址规范化
└── androidApp/
    ├── build.gradle.kts                 applicationId com.novelstack.reader(.debug)
    └── src/main/
        ├── AndroidManifest.xml          allowBackup=false、无明文流量、configChanges
        ├── kotlin/…/ReaderApplication.kt  Koin + AppState 装配
        ├── kotlin/…/MainActivity.kt       Compose 宿主；onStop 停止朗读
        ├── res/xml/network_security_config.xml  仅 10.0.2.2 / localhost / 127.0.0.1 允许明文
        ├── res/xml/data_extraction_rules.xml    禁止云备份/换机迁移
        └── proguard-rules.pro            Ktor/OkHttp/SQLDelight/SQLCipher/Koin/序列化
```

### 14.11 构建与在模拟器上运行

前置：JDK 17+、Android SDK（platform-35 + build-tools 35.0.0）。SDK 位置二选一：
`export ANDROID_HOME=...`，或写 `android/local.properties` 的 `sdk.dir=...`。

```bash
make android      # = cd android && ./gradlew :androidApp:assembleDebug
# 产物：android/androidApp/build/outputs/apk/debug/androidApp-debug.apk（debug 包名带 .debug 后缀）
make android-test # = ./gradlew :shared:testDebugUnitTest（16 个用例）

# release（R8 + 资源压缩）也已验证可过；产物未签名，正式分发需自备 signingConfig
cd android && ./gradlew :androidApp:assembleRelease
# 产物：android/androidApp/build/outputs/apk/release/androidApp-release-unsigned.apk（约 24 MB，
#      debug 包约 33 MB；两者都已确认包含最新代码，R8 未误删序列化/反射入口）
```

网络受限（公司镜像/无法直连 dl.google.com）时：

```bash
NOVELSTACK_MAVEN_MIRROR=https://<你的 maven 镜像>/ make android
```

跑起来（本机服务端用明文 HTTP，模拟器经 `10.0.2.2` 访问宿主机）：

```bash
# 1) 起服务端（无 Caddy，监听 :8080）
export DATA_DIR=./data KEK_PATH=secrets/kek.v1 ADMIN_INITIAL_PASSWORD='<口令>'
go run . serve

# 2) 起模拟器
$ANDROID_HOME/emulator/emulator -avd Pixel_7_API_35 -no-snapshot &

# 3) 安装
adb install -r android/androidApp/build/outputs/apk/debug/androidApp-debug.apk

# 4) 控制台生成配对码（浏览器 https://localhost:8443/admin → 设备与配对），
#    App 服务端填 10.0.2.2:8080 → 选「一次性配对码」→ 粘贴 → 配对
```

> 走 Caddy（HTTPS 自签）时，模拟器会因证书不受信而握手失败；本阶段的做法是**开发用明文 8080**，
> 生产用受信证书的域名（`normalizeServer` 对公网域名自动补 `https://`）。明文放行范围写死在
> `network_security_config.xml`，只有 `10.0.2.2 / localhost / 127.0.0.1`。

### 14.12 配置项与审计

| 变量 | 默认 | 范围 | 说明 |
|---|---|---|---|
| `PREFETCH_MAX_CHAPTERS` | `8` | 1..50 | 单次预取章节上限 |
| `PREFETCH_PER_MINUTE` | `40` | 1..600 | 预取令牌桶速率（每凭证） |
| `PREFETCH_MAX_MB` | `8` | 1..64 | 单次预取响应字节上限 |
| `DEVICE_TOKEN_TTL_DAYS` | `180` | — | 设备令牌有效期（过期即 401） |

| action | 触发 | 摘要 |
|---|---|---|
| `device_token_created` | 换发令牌 | 设备 id、名称、是否带公钥 |
| `device_token_denied` | 换发失败/限流 | 原因（bad credentials / invalid pairing code / ratelimited） |
| `pairing_created` | 生成配对码 | 有效期秒数 |
| `device_revoked` | 撤销设备 | 设备 id |
| `sync_manifest` | 拉清单 | since、书籍数、删除数、是否全量 |
| `progress_updated` | 上报进度 | chapter / offset / percent |
| `chapter_prefetch` | 预取 | 章节数、字节数、是否截断 / `ratelimited` |
| `tts_audio_prepare` | 合成音频 | 书、章、片段数 |

### 14.13 IReader 适配点（骨架复用与替换）

按「在 IReader 骨架上定制」的要求，复用其分层与依赖注入，替换掉一切会碰明文的部分：

| IReader 骨架 | 本项目落地 | 具体改动 |
|---|---|---|
| `:app` + `:core:*` 多模块 | `androidApp` + `shared`（KMP） | 砍到两模块：`commonMain` 放业务与 UI，`androidMain` 放平台实现 |
| Koin 依赖注入 | `di/Modules.kt`、`di/AndroidModules.kt` | 保留；把 DataSource/Repository 绑定换成 `NovelApi` + `ReaderRepository` |
| Ktor 客户端 | `net/NovelApi.kt` | **不装 Logging 插件**（否则 body/令牌进日志）；`expectSuccess=false` 手动抛 `ApiException`；统一 Bearer |
| SQLDelight | `ReaderDatabase.sq` | 表结构重写；`chapters_fts` 是运行时 raw SQL（SQLCipher 编译差异），不进 schema |
| Room / 明文 SQLite | `AndroidReaderStore` + SQLCipher | `SupportOpenHelperFactory(口令)`；口令来自 Keystore 包装的 `reader.key` |
| 阅读器 Composable | `ui/ReaderScreen.kt` | 保留翻页手感：`HorizontalPager` 滑动 + 纵向滚动两种；字体/行距/背景/语速存 `kv` |
| 音频播放器 | `tts/TtsController.kt` + `AndroidAudioSink` | 由「HTTP 流式播放」改为「取 NSAC 密文 → 内存解密 → AudioTrack 写入」，禁止落盘 |
| 书源 / 网络书 / WebView 登录 | 删除 | 单用户只连本站；登录改为用户名密码或一次性配对码 |
| 明文缓存目录 | 删除 | 只用 SQLCipher 库 + 内存；`cacheDir` 仅放 Compose/OkHttp 的常规临时文件 |

### 14.14 测试与验收

```bash
go vet ./... && go test ./...                      # 含 devices / chapterpkg / mobile / server
cd android && ./gradlew :shared:testDebugUnitTest  # 章节互操作、WAV、地址规范化
ADMIN_INITIAL_PASSWORD=... make accept             # 端到端（含第五阶段 10 项）
```

| 验收标准 | 覆盖位置 |
|---|---|
| 换发/使用/撤销→401、撤销幂等 | `server/device_test.go:TestDeviceTokenLifecycle`、`devices/devices_test.go` |
| device token → `/admin`、`/api/admin/*`、上传删除 **403** | `TestDeviceTokenLifecycle`、`server/admin_test.go`、验收脚本 |
| 浏览器会话与设备令牌互不影响 | `TestDeviceTokenLifecycle`（撤销后 session 仍 200） |
| 增量游标正确 + 空结果 + 越界 `reset_required` | `mobile/mobile_test.go`、`TestSyncManifestAndPrefetch`、验收脚本 |
| 预取只返回密文且设备可解 | `TestSyncManifestAndPrefetch`（`chapterpkg.Open` 还原正文） |
| 预取限流 429 + `Retry-After`、分桶互不影响 | `TestPrefetchRateLimit`、验收脚本 |
| 进度最后写入胜、控制台可见 | `TestProgressVisibleOnDashboard`、验收脚本 |
| 音频段是密文、设备解包可播放、他人 key 404 | `TestAudioCipherPath`、验收脚本 |
| 客户端与服务端密文格式互操作 | `ChapterCipherTest.opensServerFixture`（Go 生成的固定向量） |
| 日志/响应无明文、无 DEK、无原始令牌 | 各 handler 测试断言 + dashboard 无明文用例 |

**Android 手工清单**（需在真机/模拟器执行；沙箱无 `/dev/kvm`，未在此环境跑过）：

1. 首次启动 → 填服务端 + 配对码 → 进入书库（`kv` 里有 `nsd_` 令牌）。
2. 点「同步」→ 书架出现书籍，章节数正确。
3. 打开书 → 正文按字体/行距/背景渲染；翻页或滚动均可续读。
4. 点「缓存 3 章」→ 目录里已缓存章节带圆点。
5. 开飞行模式 → 仍可阅读已缓存章节；搜索能命中并跳转。
6. 点「朗读」→ 出声；`adb shell run-as com.novelstack.reader.debug ls -R files cache` **无 `.wav`**。
7. 控制台 `/admin` 首页「移动端」区块出现该书进度。
8. 设置 → 「退出并清除本地数据」→ 重启 App 回到登录页，必须重新配对。

### 14.15 已知限制（第五阶段）

1. **iOS 未实现**：`iosMain` 只有 `AesGcm` 的占位 actual（调用即抛异常），Xcode 侧未接 Keystore/Keychain。
   本阶段只验收 Android。
2. **没有真机/模拟器实测记录**：构建环境无 `/dev/kvm`，本次只做到「APK 构建成功 + 单元测试通过 +
   服务端端到端脚本通过」，上面的手工清单需要在你的机器上走一遍。
3. **进度是最后写入胜**：多设备同时读同一本会互相覆盖，不合并、不提示（单用户场景的显式取舍）。
4. **离线不含朗读**：音频不做本地缓存，播放需要在线；离线只保证阅读与检索。
5. **预取是手动的**：打开书时自动 3 章 + 按钮手动 3 章，没有后台任务/定时同步（避免耗电与流量不可控）。
6. **本地搜索覆盖范围 = 已缓存章节**：未缓存的章节搜不到（这正是「明文不出设备」的代价）。
7. **不抗 root**：SQLCipher 保护静态文件，但 root 设备上的运行中进程内存仍可被读取；不承诺 tamper-proof。
8. **局域网明文需手工放行**：默认只允许 `10.0.2.2 / localhost / 127.0.0.1` 走 http；
   要用 `192.168.x.x` 调试得自己往 `network_security_config.xml` 加域名，或给它配证书走 https。
9. **撤销设备 ≠ 清空设备**：撤销只让令牌失效，已经缓存在手机上的明文（SQLCipher 内）仍在；
   要彻底清除必须用 App 内的「退出并清除本地数据」。
10. **预取按整章 base64 传输**：单章很大时内存峰值高（服务端已有 8MB/次、8 章/次上限）。

## 15. 第六阶段：最小运维加固与发布准备

**核心判断**：云厂商云盘已经做了物理冗余，所以本阶段**不做**异地副本、不做定时恢复演练
（改成每季度手动一次）。只覆盖云厂商救不了的三件事：**KEK 离线备份提醒**、**误操作防护**、
**自动备份保留策略**；再加 Android release 签名配置，让你能长期覆盖安装。仍然只有单用户。

### 15.1 实现计划与文件树

```
新增
internal/backup/scheduler.go          time.Ticker 每日调度 + 命名校验 + 保留删除 + 审计
internal/backup/scheduler_test.go     注入时钟：到点触发 / 最旧先删 / 失败不动已有包
internal/ops/health.go                三类红色条件 + KEK 黄色提醒 + webhook（3 次尝试）
internal/ops/health_test.go           条件矩阵 + 请求体无敏感字段 + 重试次数
internal/server/ops_test.go           手动备份端点 → dashboard → 失败 → 横幅 → webhook
web/src/components/HealthBanner.vue   只读健康横幅（红色告警 / 黄色提醒 / 绿色正常）
android/keystore.properties.example   release 签名模板（不含真实口令）

修改
internal/config/config.go             AUTO_BACKUP_* / BACKUP_RETAIN_DAYS / BACKUP_DIR /
                                      BACKUP_PASSPHRASE_FILE / BACKUP_ALERT_WEBHOOK /
                                      KEK_OFFLINE_MARKER / ALERT_COOLDOWN_MINUTES
internal/db/db.go                     migration v6：health_signals（偏差首次观测时间）
internal/dashboard/dashboard.go       Response.health（调用 ops.Evaluate）+ backups_dir_bytes
internal/server/{server,admin}.go     POST /api/admin/backup/run + webhook 异步投递（session-only）
internal/app/app.go / internal/cli/serve.go  装配并启动/关闭调度器
web/src/views/Overview.vue            顶部挂 HealthBanner，去重旧的备份文字提醒
docker-compose.yml                    ./backups 独立挂载到 /data/backups
Makefile / .env.example               backup-now / health-check / 新变量
android/androidApp/build.gradle.kts   signingConfigs.release（文件缺失时回退未签名）
.gitignore                            *.jks / *.keystore / keystore.properties
```

### 15.2 A. 每日自动备份（复用第四阶段逻辑）

内置调度器在 Go 主程序里跑，**不引入 cron 库**：`time.Ticker` 每分钟巡检，命中配置的
`时:分` 且当天没跑过才执行；容器在 03:00 之后重启会补跑一次（启动时检查 `backup_runs`
里最近成功记录的日期）。执行体就是第四阶段的 `backup.Create`，包格式、Argon2id 参数、
VACUUM INTO 一致性快照、快照内清除 session/书源令牌等**全部不变**。

| 变量 | 默认 | 范围 | 说明 |
|---|---|---|---|
| `AUTO_BACKUP` | `true` | bool | 关掉后仍可用 `make backup` / 控制台手动触发 |
| `AUTO_BACKUP_HOUR` / `AUTO_BACKUP_MINUTE` | `3` / `0` | 0..23 / 0..59 | 容器本地时区（默认 UTC） |
| `BACKUP_RETAIN_DAYS` | `7` | 1..365 | 保留最近 N 天，超出删最旧 |
| `BACKUP_DIR` | `<DATA_DIR>/backups` | 路径 | compose 里固定 `/data/backups`，挂独立卷 |
| `BACKUP_PASSPHRASE_FILE` | 空 | 路径 | 推荐只读 secret 文件；留空读 `NOVELSTACK_BACKUP_PASSPHRASE` |

最简启用方式：在 `.env`（已 gitignore）写 `NOVELSTACK_BACKUP_PASSPHRASE=<至少 12 字符>`；
更稳妥的方式是把口令写进只读文件并挂载，再设 `BACKUP_PASSPHRASE_FILE=/run/secrets/...`。
两种方式的口令都与主 KEK 无关，且绝不进入备份包。若 `AUTO_BACKUP=true` 但启动时尚无
可用口令，stdout 会输出一条固定文本的 `WARNING: ...`（不含口令、口令文件或错误细节），
自动备份调度器保持禁用；用 `make backup` 或控制台手动备份不受影响，配上口令重启即生效。

- 包名：`novelstack-YYYYmmdd-HHMMSS-<sha256前8位>.nsb` + 同名 `.sha256` 伴生文件；
  校验和在包生成后回填，同步更新 `backup_runs.path` 与审计。
- 保留策略**只删**匹配 `^novelstack-(\d{8})-(\d{6})-([0-9a-f]{8})\.nsb$` 的文件，
  按包名日期判断；手工备份、临时文件、其他命名一律不碰。失败**不删任何包**。
- 每次结果写 `backup_scheduled`（`sha256` 完整值、`size_bytes`、`duration_ms`、
  `deleted`）；失败写 `backup_failed`（`reason=...`），不记明文、不记口令。
- 控制台「运营概览 → 最近备份」直接显示这些记录；不需要新页面。dashboard 另返回
  `backups_dir_bytes`（递归统计 `backups/` 目录内普通文件大小，含 `.nsb`、`.sha256`
  与演练日志；只读元数据，不打开包内容），在「最近备份」卡片以「目录占用」展示。

手动触发（session-only，设备令牌 403）：

```bash
ADMIN_INITIAL_PASSWORD='...' make backup-now     # = POST /api/admin/backup/run
ADMIN_INITIAL_PASSWORD='...' make health-check   # 打印 health JSON
# 或直接：
curl -c /tmp/c -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"..."}' https://localhost:8443/api/login
curl -b /tmp/c -X POST https://localhost:8443/api/admin/backup/run
```

每季度人工演练一次（本阶段不做定时演练）：

```bash
make backup-list && make restore-drill PACKAGE=backups/<包名>
```

### 15.3 B. 控制台健康横幅与 webhook

`/api/admin/dashboard` 增加 `health` 字段；概览页顶部 `<HealthBanner>` 只读展示，不新增
任何解密能力。判定条件：

| 级别 | 条件 |
|---|---|
| **红 bad** | 最近一次自动备份失败（审计 `backup_failed` 晚于最近成功记录） |
| **红 bad** | 超过 48 小时没有成功备份记录（含从未备份） |
| **红 bad** | 最近 24 小时 `decrypt_failed` / `book_download_tamper` / `tts_cache_tamper` 计数 > 0 |
| **红 bad** | `\|索引文档数 - 书籍数\| > 0` 且持续超过 1 小时（双向判定，多/少都告警；首次观测时间写 `health_signals`，跨重启不丢，摘要中区分方向） |
| **黄 warn** | 没有 KEK 离线备份确认标记（不阻塞启动） |
| 绿 ok | 以上都不成立 |

索引偏差摘要会写明方向，例如「元数据索引文档数比书籍数少 5，已持续超过 1 小时」；
偏差归零即清除计时器，下一次偏差重新计时。

> 控制台顶部健康横幅（黄色为 KEK 离线备份提醒；红色为三类运维告警）：
>
> （截图略，本地 docs/screenshots/ 保留）

可选 webhook：设置 `BACKUP_ALERT_WEBHOOK` 后，告警**从无到有**时异步 POST 一条 JSON：

```json
{
  "time": "2026-11-26T09:00:00Z",
  "level": "bad",
  "type": "backup_failed",
  "summary": "最近一次自动备份失败：请查看审计 backup_failed，并确认云盘可写与备份口令有效。"
}
```

- `type` 取值：`backup_failed` / `backup_stale` / `integrity_tamper` / `index_drift` /
  `kek_offline_backup`。摘要由**固定模板 + 计数**生成，不含正文、查询词、token、KEK/DEK、
  审计 detail 或 webhook URL（URL 可能带 bot token）。
- 同类告警 `ALERT_COOLDOWN_MINUTES`（默认 60）内只发一次；告警消失后清除去重，下次立即发。
- 投递失败重试 2 次（共 3 次）后放弃并写 `health_webhook` 审计（`attempts=3`）；
  **没有消息队列**，进程重启不会补发。企业微信/钉钉/Telegram Bot 的接入由你自己接 webhook 地址。

### 15.4 C. KEK 离线备份（刻意不自动化）

启动时会检查 `KEK_OFFLINE_MARKER`（默认 KEK 同目录下的 `.kek-offline-backup-ok`，
compose 示例为 `/data/.kek-offline-backup-ok`）。**标记只是「我确认做过了」的开关**，
不参与任何加密，也不阻塞启动；没有它只会显示黄色提醒。

建议流程：

1. `make kek` 后立刻把 `secrets/kek.v1` 与 `secrets/kek.v2` **打印出来或存入硬件密钥/
   离线介质**，并核对抄录是否完整；这是唯一能解开所有历史备份的东西。
2. **KEK 永不放进任何备份包**，也不进 webhook、日志、审计 detail、git 仓库。
   丢失 KEK = 所有书籍与备份永久不可读，云厂商也救不了。
3. 确认离线副本可用后：`touch data/.kek-offline-backup-ok`（宿主机，对应容器内
   `/data/.kek-offline-backup-ok`），黄色提醒消失。
4. **每次 KEK 轮换后重新做一次离线备份**，再把标记时间戳更新一遍。

代码里**没有**「导出 KEK」的命令或接口——这是刻意的安全边界，不是遗漏。

### 15.5 D. Android release 签名

```bash
# 1) 生成一次，永久保存；丢失后无法覆盖安装同名应用（只能换新包名重装、数据不迁移）
keytool -genkeypair -v -keystore secrets/novelstack-release.jks \
  -alias novelstack -keyalg RSA -keysize 2048 -validity 10000 \
  -storepass '<强口令>' -keypass '<强口令>' -dname 'CN=NovelStack'

# 2) 复制模板并填写（文件与 .jks 都已被 .gitignore 忽略）
cp android/keystore.properties.example android/keystore.properties
#   storeFile=../secrets/novelstack-release.jks
#   storePassword / keyPassword / keyAlias 按上面填写

# 3) 构建；未填 keystore.properties 时仍是未签名包，不阻塞 CI
cd android && ./gradlew :androidApp:assembleRelease
# 有配置：androidApp/build/outputs/apk/release/androidApp-release.apk（已签名）
# 无配置：.../androidApp-release-unsigned.apk
/opt/android-sdk/build-tools/35.0.0/apksigner verify --verbose <apk>
```

`keystore.properties` 与 `.jks` **不进仓库、不进日志、不进备份包**；口令只在本机
Gradle 进程里使用。建议把 keystore + 口令一起存进密码管理器或硬件密钥，和 KEK 分开存放。

### 15.6 测试与验收

```bash
go vet ./... && go test ./...                  # 含 scheduler / ops / server 集成用例
ADMIN_INITIAL_PASSWORD='...' make accept       # 端到端（含「立即备份 → 控制台可见」）
cd android && ./gradlew :androidApp:assembleDebug :shared:testDebugUnitTest
```

| 验收项 | 覆盖位置 |
|---|---|
| 注入时钟到点触发、同日不重复、保留策略删最旧且只删合规命名 | `internal/backup/scheduler_test.go` |
| 备份失败写 `backup_failed`，且不删除任何已有包 | `TestSchedulerFailureDoesNotDeleteAnything` |
| 三类红色条件 + KEK 黄色提醒 + 偏差持续 1 小时 | `internal/ops/health_test.go` |
| 索引偏差双向判定（多/少均告警，摘要区分方向，归零重新计时） | `TestEvaluateNegativeIndexDrift` |
| 启动缺口令时的 stdout WARNING 固定且不含口令 | `internal/app/app_test.go` |
| `backups_dir_bytes` 递归统计、控制台可见、备份后端到端断言 | `internal/dashboard/dashboard_test.go` / `internal/server/ops_test.go` / `scripts/acceptance` |
| webhook 请求体只有 4 个固定字段、无敏感串；失败共 3 次并写审计 | `TestNotifyWebhookBodyAndRetry` |
| 手动备份端点：session 200 / device token 403 / dashboard 可见 | `internal/server/ops_test.go` |
| 失败场景（备份目录不可写）→ 横幅变红 → webhook 收到 JSON | `TestFailureTurnsBannerRedAndSendsWebhook` |
| release 构建：无 keystore.properties 未签名；有配置后 `apksigner verify` 通过 | 本地已实测两种路径 |

**手工验收**（云主机上）：改 `.env` 开启自动备份与 webhook → `make up` →
`make backup-now` → 控制台概览看到「系统健康」绿条与新备份 → 临时把备份卷设为只读再
`make backup-now` → 横幅变红且 webhook 收到 JSON → 恢复权限 → `touch data/.kek-offline-backup-ok`
→ 黄色提醒消失。

### 15.7 已知限制（第六阶段）

1. **不做异地副本**：云盘冗余是前提；本阶段不复制到第二个厂商/区域。
2. **不做定时恢复演练**：改为每季度手动 `make restore-drill`，不会自动验证备份可恢复。
3. **webhook 无重试队列**：失败只重试 2 次并写审计，进程重启不补发；要可靠告警请接一个
   消息队列/告警平台（超出本阶段范围）。
4. **调度精度是分钟级 + 补跑**：容器在触发时刻前后短暂停机可能补跑或顺延，不保证秒级。
5. **保留策略按包名日期而非 `backup_runs.ts`**：人工改过文件名的包不会被自动清理（安全优先）。
6. **健康横幅「索引偏差」只报绝对值偏差**：多索引与漏索引都会告警，但无法区分是书籍被误删还是索引未同步，仍需人工核对搜索页覆盖度表。
7. **KEK 标记只是人工确认**：无法验证你是否真的做了离线备份，它只消除提醒，不提供任何保护。
8. **无签名配置时 release 包未签名**：不能覆盖安装已签名的同名应用；请按 §15.5 配置后再发布。

---

## 16. 基建与测试（P0–P5）

本轮只做基建加固，不新增业务功能、不改任何 API/数据库 schema/依赖版本。

### 16.1 P0 敏感文件清单与入库

仓库历史上只有一条 `Initial commit`（仅 12 字节 README），**确认历史干净，无需 `git filter-repo` / BFG**。
`.gitignore` 必须覆盖下列路径（可用 `git check-ignore -v <path>` 或 `git status --ignored` 核对）：

| 类别 | 路径/模式 |
|---|---|
| 密钥与口令 | `secrets/kek.v1`、`secrets/kek.v2`、`secrets/kek.*`、`backups.pass`、`*.pass`、`secrets/*.key` |
| 环境配置 | `.env`（保留 `.env.example`）、`data/`、`backups/` |
| 备份与导入 | `*.nsb`、`*.nsb.sha256`、`drill-log.jsonl`、`imports/*.txt`（保留 `.gitkeep`） |
| 签名材料 | `android/keystore.properties`、`*.jks`、`*.keystore` |
| 构建产物 | 根 `dist/`、`node_modules/`、`.gradle/`、`.kotlin/`、`build/`、`*.hprof`、`local.properties`、根 `acceptance` / `novelstack` 二进制 |
| 其他 | `demo-out/`、`*.db` / WAL / SHM、`*.enc`、`*.log` |

> 注意：`internal/adminui/dist/` 是 `//go:embed` 的前端产物，**必须入库**。
> 因此 `.gitignore` 用根锚定 `/dist/`，并显式 `!/internal/adminui/dist/`；
> `.dockerignore` 同样用 `/dist`，不能用裸 `dist`。

推送后 GitHub 仓库文件树应可见：`go.mod`、`go.sum`、`main.go`、`internal/`、
`clientcore/`、`cmd/`、`scripts/`、`android/`、`web/`、`docs/`、`Caddyfile`、
`Dockerfile`、`docker-compose.yml`、`Makefile`、`.env.example`、`.gitignore`。

### 16.2 依赖与工具链锁定（P1）

- `go.mod` 增加 `toolchain go1.22.12`，固定到已完成全量验证的 Go 版本。
  历史上曾因未锁定工具链导致依赖漂移到需要 go1.26，现已禁止。
- 所有 Go 命令通过 `GOFLAGS=-mod=readonly` 运行（Makefile 已 `export`，CI 的 go job 也显式设置）：
  `go build` / `go test` / `go vet` 绝不隐式改写 `go.mod` / `go.sum`。
- Android **已使用 Gradle version catalog**（`android/gradle/libs.versions.toml`），
  两个模块均通过 `alias(libs.plugins.*)` 与 `libs.*` 引用，无硬编码依赖版本；
  唯一字面量 `versionName = "0.1.0"` 是应用版本而非依赖版本。本轮未做迁移，也未改动任何版本号。
- CI 的代理地址通过 repository variables 注入：
  `GO_MIRROR` → `GOPROXY`，`GRADLE_MIRROR` → `NOVELSTACK_MAVEN_MIRROR`；未设置时走官方源。

**依赖升级流程**（必须按顺序执行）：

1. 升级前：全量测试 + race 回归（`make test-all && make test-race`）全绿，记录当前版本号。
2. 显式解除只读后升级：`GOFLAGS= go get <module>@<version>` 或 `GOFLAGS= go mod tidy`。
3. 升级后：再次跑 `make test-all && make test-race`，并跑 `go vet ./...`、前端构建与 Android debug 构建。
4. 在提交信息/README 变更记录中写明「升级了什么、从哪个版本到哪个版本、为什么升」。
5. Android 依赖只改 `libs.versions.toml` 的 `[versions]`，不直接在 `build.gradle.kts` 写版本；
   升级前后用 `cd android && ./gradlew :shared:dependencies` 对比输出确认版本变化符合预期。

### 16.3 测试分层与并发安全（P2 / P3）

| 目标 | 命令 | 适用范围 |
|---|---|---|
| 最快反馈 | `make test-short` / `go test -short ./...` | 单测 + 不需要 Docker / Meilisearch 的测试；`internal/server` 的完整 HTTP 栈用例自动跳过 |
| 全量回归 | `make test-all` / `go test ./...` | 含完整 HTTP 栈、备份调度、恢复演练、TTS 等所有用例 |
| 并发回归 | `make test-race` / `go test -race ./...` | 全量 + 竞态检测；需 CGO 与 gcc |

实测耗时（本机，Go 1.22.12，`-count=1`）：

| 命令 | 耗时 |
|---|---|
| `go test -short ./...` | **5.6 s** |
| `go test ./...` | **12.5 s** |

- 完整栈集成测试统一在 `internal/server/main_test.go` 通过 `requireFullStack(t)` 判断
  `testing.Short()` 并跳过；只读用例通过 `sharedFullStack(t)` + `sync.Once` 复用一份
  SQLite + KEK + httptest 栈，由 `TestMain` 统一释放，避免每个测试函数重建。
- `-race` 覆盖的并发路径：`internal/mobile`（令牌桶）、`internal/tts`（朗读引用计数）、
  `internal/ttscache`（并发 Put/Get/Verify/Stats）、`internal/backup`（调度器启停）、
  `internal/server`（控制台轮询 handler）。
- `-race` 实测暴露并修复了调度器的**真实缺陷**：`Close()` 将 `s.stop` 置为 `nil` 后，
  goroutine 仍在读共享字段 `s.stop`，导致停止信号永远收不到（死锁）；同时 `s.ticker`
  在锁外读写。修复方式是 `lifeMu` 串行化 Start/Close、goroutine 捕获局部 `stop`/`ticker`，
  而不是给所有业务逻辑套大锁。

### 16.4 最小 CI（P4）

`.github/workflows/ci.yml` 只有三个 job，不引入 matrix / 缓存 / 覆盖率上传：

| job | 做什么 | 不做什么 |
|---|---|---|
| `go` | `go vet ./...`、`go test -race -short ./...`、`gofmt -l .` 必须为空 | 不跑全量集成（留给本地 `make test-all`） |
| `android` | `./gradlew :shared:testDebugUnitTest :androidApp:assembleDebug` | 不跑 release、不跑 lint |
| `docker` | `docker build -t novelstack-ci .` | 不跑 compose up |

> `gradle-wrapper.jar` 与 `gradle-wrapper.properties` 均已入库（`.gitattributes` 标记 `*.jar binary`，
> 避免换行转换损坏），CI 直接 `./gradlew` 即可。同目录的 `gradle-wrapper.jar.b64` 是同一个 jar 的
> base64 文本副本（早期通过 API 推送时二进制受限留下的备份），仅供人工校验，CI 不再依赖它。
> 版本仍是 8.11.1，未升级。
>
> **已知约束**：通过 GitHub API 推送的文件统一为 `0644`，会丢失 `android/gradlew` 的
> 可执行位，导致 CI 中 `./gradlew` 以 **exit code 126** 失败（android job 6s 即挂，
> go/docker job 不受影响）。仓库内该文件当前为 `100755`；CI 的
> `Ensure Gradle wrapper executable` 步骤仍幂等地 `chmod +x`，本地推送与 API 推送两条路径都能跑通。

workflow 语法可用 PyYAML 解析校验；`GOFLAGS=-mod=readonly` 在 go job 的 env 中显式设置。

### 16.5 Docker 构建卫生（P5）

`.dockerignore` 排除 `.git`、`.github`、根 `data/`、`secrets/`、`backups/`、`imports/`、
根 `dist/`、`android/`、`demo-out/`、`*.nsb`、`*.md`（`!README.md` 例外）等；
根锚定保证 `internal/adminui/dist` 仍能进入构建上下文。
Dockerfile 的运行时镜像为 `gcr.io/distroless/static-debian12:nonroot`（可用
`RUNTIME_IMAGE` build-arg 覆盖），入口点 `/app/novelstack`、非 root 用户与 secret 挂载方式均未改动。

## 17. 第七阶段：Android 播放体验生产化

本阶段把第五阶段「能出声」的朗读能力升级为可长时间后台运行、可系统级控制、可定时、可续听的生产级播放链路。**不改动服务端与 EPUB 解析**；服务端仍只返回密文与元数据，解密在客户端内存完成。

### 17.1 架构选型：为什么是 AudioTrack + MediaSession，而不是 ExoPlayer

| 维度 | 本方案（AudioTrack + MediaSessionCompat + 前台服务） | ExoPlayer / Media3 |
|---|---|---|
| 输入形态 | `TtsController` 已在内存中产出连续 16-bit PCM，直接 `write()` 到 AudioTrack | 需要 DataSource/MediaSource 封装，缓存与渐进式下载引入落盘审计面 |
| 落盘风险 | 无缓存、无临时文件；音频字节生命周期止于内存 | 默认 `SimpleCache` 会把媒体写入应用目录，需额外加固 |
| 控制面 | MediaSessionCompat + MediaStyle 通知已覆盖播放/暂停/切章/锁屏 | 能力更强，但本项目不需要 DASH/HLS/DRM |
| 依赖面 | 仅 `androidx.media:1.7.0` | 引入 media3-exoplayer 及传递依赖 |
| 结论 | 契合「密文传输、内存解密、绝不落盘」的安全边界，代码量小 | 能力过剩，且增加缓存落盘审计负担 |

### 17.2 数据流与安全边界

```
服务端 NSAC 密文片段（X-TTS-Stream: wav-segments）
   │  HTTPS + 设备令牌
   ▼
TtsController：RSA 解包音频 DEK ──> AES-GCM open（AAD 绑定片段 key）
   │  解密结果仅在内存
   ▼
WavSegmentStream：逐段剥离 RIFF/fmt/data 头 ──> 连续 PCM（仅 16-bit PCM）
   │  audioKey / ct / wav / pcm 用完即 zeroBytes
   ▼
AndroidAudioSink（AudioTrack MODE_STREAM） ──> out.write(pcm)
```

- 播放层无任何 `.wav/.pcm/.mp3` 文件写入调用，无音频日志输出。
- 通知栏与 MediaSession 元数据**只含书名与章节标题**（`PlaybackMetadata`），不含正文片段。
- 唯一的本地文件写入仍是 `AndroidDeviceKeyStore` 的密钥/数据库包装，与音频无关。

### 17.3 核心组件（commonMain 纯逻辑）

| 文件 | 职责 |
|---|---|
| `tts/PlaybackModels.kt` | `PlaybackStatus`、`SleepMode`（15/30/60 分钟 + 播完当前章）、`PlaybackMetadata`、`PlaybackEvent`、`PlaybackSignal` |
| `tts/WavSegmentStream.kt` | 增量消费多个各自带 RIFF 头的 WAV 片段，输出连续 PCM；校验 16-bit / 8k–48k / 1–2 声道、跨段格式一致；单段上限 16 MiB 防 OOM；奇数字节 padding 对齐 |
| `tts/SleepTimer.kt` | 注入 `Clock`，`tick()` 计时到点、`onChapterEnded()` 章节模式触发；`remainingMs()` 供 UI 倒计时 |
| `tts/PlaybackStateMachine.kt` | IDLE→PREPARING→PLAYING/PAUSED→STOPPED/ERROR 集中流转，含音频焦点/耳机拔出事件 |
| `tts/TtsController.kt` | 驱动状态机、断点续听（`resumeSegment`）、变速（0.5x–2.0x）、章节导航、定时关闭、优雅停止信号 |

### 17.4 Android 平台层

| 文件 | 职责 |
|---|---|
| `playback/PlaybackService.kt` | 前台服务 + `MediaSessionCompat` + `MediaStyle` 通知；音频焦点（`AudioFocusRequest`）、`ACTION_AUDIO_BECOMING_NOISY` 耳机拔出暂停；状态机信号驱动通知与 `stopSelf()` |
| `AndroidManifest.xml` | `FOREGROUND_SERVICE`、`FOREGROUND_SERVICE_MEDIA_PLAYBACK`、`POST_NOTIFICATIONS` 权限；service 声明 `android:foregroundServiceType="mediaPlayback"` |
| `MainActivity.kt` | 请求通知权限；设置 `AppState.onPlaybackStarted` 钩子拉起前台服务；**不再**在 `onStop` 停止朗读 |
| `app/AppState.kt` | 注入 metadata provider 与章节导航；`ttsStart()` 统一入口（先拉服务再播放） |
| `ui/ReaderScreen.kt` | `PlaybackPanel`：播放/暂停、上一章/下一章、语速、定时关闭与倒计时，仅展示元数据 |
| `gradle/libs.versions.toml` + `androidApp/build.gradle.kts` | 新增 `androidx-media 1.7.0` |

> **Android 14（API 34）适配要点**：媒体前台服务必须声明 `type=mediaPlayback`，且
> `startForeground(id, notification, FOREGROUND_SERVICE_TYPE_MEDIA_PLAYBACK)` 必须传类型标志，
> 否则抛 `SecurityException` 崩溃；同时需要 `POST_NOTIFICATIONS` 运行时授权（Android 13+）。
> 这两点已分别在服务与 Manifest/Activity 中落实。

### 17.5 测试与构建验证（2026-11-26 实测）

| 项目 | 命令 | 结果 |
|---|---|---|
| Go 全量回归 | `go test ./...` | 全绿（server 16.4s / backup 6.0s / tts 3.0s …） |
| 静态检查 | `go vet ./...`、`gofmt -l .` | 通过 / 无输出 |
| 播放纯逻辑单测 | `:shared:testDebugUnitTest` | 新增 22 个用例全绿：状态机 5、定时 5、WAV 片段流 7、WAV 解析 5 |
| Android debug | `:androidApp:assembleDebug` | 成功，`androidApp-debug.apk` 34 MB |
| Android release | `:androidApp:assembleRelease` | 成功，`androidApp-release-unsigned.apk` 24 MB；R8 `mapping.txt` 36 MB、seeds/baselineProfiles 齐全，未误删序列化/反射入口 |

> 复验备注：本次重跑时 Gradle 8.11.1 的 wrapper zip 缓存为空，`services.gradle.org`
> 重定向到 GitHub 后 10s 超时；上述 APK/测试产物来自此前成功构建，产物时间戳与当前代码一致。

### 17.6 真机验收清单

1. 配对后进入阅读器点「朗读」，确认出声、UI 显示章节标题与进度。
2. 切后台 / 锁屏，确认继续播放，通知栏出现 MediaStyle 控制（播放/暂停、上一章、下一章）。
3. 通知栏与锁屏分别测试播放、暂停、上一章、下一章，状态与 UI 同步。
4. 播放中拔出耳机（`ACTION_AUDIO_BECOMING_NOISY`），确认自动暂停；其他 App 抢音频焦点确认暂停，焦点恢复后继续。
5. 定时关闭分别设 15/30/60 分钟与「播完当前章」，确认到点/播完后停止前台服务、通知消失。
6. 断点续听：播放若干片段后暂停并杀进程，重启后从断点片段继续（进度按片段粒度）。
7. 语速 0.5x–2.0x 调节，确认即时生效。
8. Android 14 真机确认无 `SecurityException`（前台服务类型正确）。
9. **安全验证**：`adb shell run-as com.novelstack.reader.debug ls -R files cache`（debug 包）或检查应用数据目录，**确认无任何 `.wav` / `.pcm` / `.mp3` 文件**；日志中不出现音频字节。
10. 拒绝通知权限后确认 App 不崩溃、仍可朗读（仅无通知栏控制）。

### 17.7 已知限制

1. **片段拼接间隙**：每个 TTS 片段是独立合成的 WAV，直接拼接 PCM；跨段无 crossfade，段边界可能有细微静音或电平突变。若某段采样率/声道数与首段不一致会校验失败并报错（不静默错播）。
2. **断点粒度**：续听以「片段」为单位，不能精确到片段内秒级。
3. **客户端变速**：通过 AudioTrack 播放速率实现，不改文件内容；接近 2.0x 时音质下降，硬限制在 0.5x–2.0x。
4. **音频不缓存**：播放需在线，离线仅保证阅读与检索（延续第五阶段边界）。
5. **进程被杀不自动续播**：前台服务 `START_STICKY` 重建但不重放 intent，播放停止；用户手动续听走断点逻辑。

### 17.8 本阶段文件清单

**新增（commonMain 纯逻辑）**
- `android/shared/src/commonMain/kotlin/com/novelstack/reader/tts/PlaybackModels.kt`
- `android/shared/src/commonMain/kotlin/com/novelstack/reader/tts/WavSegmentStream.kt`
- `android/shared/src/commonMain/kotlin/com/novelstack/reader/tts/SleepTimer.kt`
- `android/shared/src/commonMain/kotlin/com/novelstack/reader/tts/PlaybackStateMachine.kt`

**新增（Android 平台层）**
- `android/androidApp/src/main/kotlin/com/novelstack/reader/playback/PlaybackService.kt`

**新增（单测）**
- `android/shared/src/commonTest/kotlin/com/novelstack/reader/tts/PlaybackStateMachineTest.kt`
- `android/shared/src/commonTest/kotlin/com/novelstack/reader/tts/SleepTimerTest.kt`
- `android/shared/src/commonTest/kotlin/com/novelstack/reader/tts/WavSegmentStreamTest.kt`

**修改**
- `android/shared/src/commonMain/kotlin/com/novelstack/reader/tts/TtsController.kt`（接入状态机/断点/定时/章节导航/元数据）
- `android/shared/src/commonMain/kotlin/com/novelstack/reader/app/AppState.kt`（注入 provider、导航、ttsStart 钩子）
- `android/shared/src/commonMain/kotlin/com/novelstack/reader/ui/ReaderScreen.kt`（PlaybackPanel）
- `android/androidApp/src/main/kotlin/com/novelstack/reader/MainActivity.kt`（通知权限 + 服务钩子）
- `android/androidApp/src/main/AndroidManifest.xml`（权限 + service 声明）
- `android/gradle/libs.versions.toml`、`android/androidApp/build.gradle.kts`（androidx-media 依赖）

## 18. 第八阶段：客户端体验与书库运营

本阶段给客户端补齐「发现—阅读—足迹—收藏—更新」的闭环，同时保持加密边界不变：所有新接口只读元数据与进度，**不请求、不解密、不返回任何正文**。

### 18.1 服务端接口

| 方法 | 路径 | 鉴权 | 说明 |
|---|---|---|---|
| PATCH | `/api/books/{id}/favorite` | device token / session | 请求体 `{"is_favorite":true}`，返回回显状态；未知书 404，缺字段 400 |
| GET | `/api/history` | 同上 | 按 `reading_progress.updated_at` 倒序，join 书名与章节标题；只含元数据与进度 |
| GET | `/api/discover` | 同上 | `recent`（最近 12 本）、`tags`（标签聚合前 20）、`random`（随机 5 本） |
| GET | `/api/app/version` | 同上 | `version` / `changelog` / `mirrors`（镜像数组来自环境变量） |
| GET | `/api/app/version/latest-hash` | 同上 | 最新 APK 的 `sha256`；未配置时 404（客户端据此拒绝安装未校验包） |

**数据库迁移 v7**：新增 `user_bookmarks(owner_id, book_id, is_favorite, updated_at)`。收藏放独立表而不是 `books` 列，原因是收藏是用户偏好，不应污染 `books.sync_seq` 的增量同步语义；取消收藏写 `is_favorite=0` 而非删行，保持状态可判定。

**配置（环境变量注入，不写死）**：

```bash
APP_VERSION=0.2.0
APP_CHANGELOG="新增发现页、足迹、收藏与应用内更新"
APP_MIRRORS=https://mirror-a.example/apk/latest.apk,https://mirror-b.example/apk/latest.apk
APP_APK_SHA256=<构建产物的 sha256 十六进制>
```

`GET /api/books` 与 `/api/books/{id}` 会在响应中附带 `is_favorite`（handler 批量 join 一次，避免 N+1）。

### 18.2 客户端：四 Tab 与发现页

底部导航改为 **书架 / 发现 / 足迹 / 设置**；阅读页全屏不显示底栏。

- `DiscoverScreen`：分类标签流（`FlowRow` + `AssistChip`）、最近更新横向卡片、随机推荐横向卡片与「换一批」（重新拉取 `/api/discover`）。卡片点击直接 `openBook`。
- `HistoryScreen`：列表展示书名、最后章节、进度条、相对时间（刚刚 / N 分钟前 / N 天前），点击 `openAt(bookId, chapterIndex)` 恢复阅读位置。
- 书架顶部加「全部 / 收藏」`FilterChip` 切换；卡片对已收藏书显示 ♥。
- 阅读页右上角 ♥ 切换收藏：**乐观更新本地 SQLCipher → 调 PATCH 确认 → 失败回滚并提示**。

### 18.3 应用内更新：测速 + 哈希校验 + FileProvider

`UpdateManager` 分两层：

1. **commonMain 纯逻辑（可单测）**
   - `Sha256`：纯 Kotlin SHA-256（不依赖平台），用于校验下载内容；
   - `UpdatePlanner.pickFastest`：从（镜像, 耗时）里选最快，忽略失败探测；
   - `UpdatePlanner.isNewer`：点分版本比较；
   - `AppUpdater` 接口。
2. **androidMain 实现 `AndroidAppUpdater`**
   - **测速**：对每个镜像并发发起 `Range: bytes=0-102399`（前 100KB）请求，记录耗时，取最快者；
   - **下载**：从最快镜像下载完整 APK，回调进度；
   - **校验**：`Sha256.matches(bytes, expected)`，不符立即抛错，不落可安装文件；
   - **安装**：写入 `cacheDir/updates/`，经 `FileProvider`（`${applicationId}.fileprovider`）拉起系统安装器（Android 7.0+），并声明 `REQUEST_INSTALL_PACKAGES`。

设置页「检查更新」弹窗展示版本与 changelog；服务端未配置哈希时直接取消下载，避免安装未校验的包。

**发布链路（`.github/workflows/release-apk.yml`）**

打 `v*` tag 触发，四件事必须同时成立才对外分发：

| 环节 | 做法 | 失败时 |
|---|---|---|
| 版本号 | 从 tag 推导：`v0.2.1` → `versionName=0.2.1`、`versionCode=major*10000+minor*100+patch=201` | tag 不符合 `vX.Y.Z` 直接 error |
| 签名 | `secrets.KEYSTORE_B64` 解码到 `build/ci-signing/release.jks`，经 `NOVELSTACK_*` 环境变量传给 `signingConfigs` | 产物仍是 `-unsigned`，`Compute SHA-256` 步骤 error 退出 |
| 校验 | `sha256sum` + `apksigner verify --print-certs` 回验 | 无 apksigner 时降级为 warning |
| 分发 | 逐个镜像 `PUT` 上传，统计成功数 | 全部失败则 error |

产物附带 `apk-manifest.json`（`version` / `sha256` / `mirrors`），直接对应服务端要配的 `APP_VERSION` 与 `APP_APK_SHA256`，避免手抄哈希出错。

> 为什么版本号必须由 tag 注入：`versionName` 若固定写 `0.1.0`，用户装上 0.2.0 后客户端仍自报 0.1.0，`isNewer` 会永远判定「有新版本」，形成升级死循环。

### 18.4 阅读体验

| 能力 | 实现 |
|---|---|
| 翻页动画 | `HorizontalPager`：`slide`（平移，默认）/ `cover`（覆盖：被翻走的页按 `pageOffset` 轻微后退形成层次）/ `scroll`（上下滚动） |
| 主题 | `auto`（跟随系统）/ `light`（白天）/ `dark`（夜间）/ `eye`（护眼黄底）；`isDarkTheme` 与 `readerPaletteFor` 统一映射 |
| 亮度 | 阅读区左右各 30% 区域内上下滑动，按位移调整 `Window.screenBrightness`（平台钩子 `onBrightnessChange`，commonMain 不引用 Android） |
| 章节预读 | `PrefetchGate(0.8f)`：滚动进度 ≥80% 且存在下一章时，静默拉取下一章密文并解密写入 SQLCipher；每章只触发一次 |
| 排版 | `TextIndent(firstLine = 2 字符)` 首行缩进；`LineBreak.Paragraph`；`Kinsoku.apply` 在禁则标点前插入 WORD JOINER（U+2060）近似避头尾 |

> 预读遵循第七阶段的铁律：密文与解密结果只存在于内存与 SQLCipher，不落任何明文文件。

### 18.5 安全边界

- 发现 / 足迹 / 收藏接口的 SQL 只 SELECT 元数据列（`title/author/tags/chapter_count/created_at/percent/chapter_index`），不碰 `storage_path`、`wrapped_dek`、密文文件。
- 集成测试 `TestDiscoverAndHistoryNoPlaintext` 断言响应 JSON 不含 `text/ciphertext/content/storage_path/wrapped_dek/dek_nonce` 等字段。
- 下载安装必须通过 SHA-256 校验 + FileProvider，二者缺一不可。

### 18.6 测试与验收

| 项目 | 结果 |
|---|---|
| Go 服务端集成测试 | `TestDiscoverAndHistoryNoPlaintext`、`TestFavoriteToggle`、`TestAppVersionEndpoints` 全部通过；`go vet` / `gofmt` / `go test ./...` 全绿（19 个包） |
| 客户端单测 | `:shared:testDebugUnitTest` **56 个用例全通过**，本阶段新增 23 个：`Sha256Test`(5) 已知向量、`UpdatePlannerTest`(6) 选最快/失败忽略/版本比较、`PrefetchGateTest`(5) 80% 触发一次、`KinsokuTest`(5) 避头尾、`AppVersionTest`(2) 版本注入格式 |
| 构建 | `:shared:testDebugUnitTest`、`:androidApp:assembleDebug`、`:androidApp:assembleRelease`（含 R8 + lintVital）均 BUILD SUCCESSFUL |
| 签名链路 | 以测试 keystore 注入 `NOVELSTACK_KEYSTORE_B64` 等 4 个环境变量，产出 `androidApp-release-unsigned.apk` 以外的已签名包，`apksigner verify` 通过 |

真机手工验收：发现页刷新与分类、书架「只看收藏」与阅读页收藏联动、足迹点击恢复、翻页动画切换不崩溃、四主题切换、边缘滑动调亮度、弱网下 80% 预读后下一章秒开、弱网下多镜像测速选优并正常唤起安装器。

### 18.7 已知限制

1. **翻页仅平移/覆盖**：未实现仿真（卷曲/翻书）动画；`cover` 是基于 `pageOffset` 的层次位移近似。
2. **避头尾为近似**：Compose 未暴露完整 kinsoku 开关，采用 WORD JOINER 预处理，极端排版下仍可能有少量不理想断行。
3. **收藏本地优先**：单用户单设备以本地 SQLCipher 为准；多设备同时改收藏时以最后一次 PATCH 为准，不做冲突合并。
4. **更新分发需自备镜像与签名**：CI 的 `release-apk.yml` 已支持签名注入（`KEYSTORE_B64` 等 4 个 secrets）与多镜像上传，但 secrets/`vars.APK_MIRRORS` 需由仓库维护者配置；未配置签名时 CI 直接失败而不产出发不出去的 unsigned 包。
5. **亮度作用于窗口**：调节的是当前 Activity 的 `screenBrightness`，退出阅读页后由系统/其他页面接管。

### 18.8 本阶段文件清单

**服务端**
- `internal/db/db.go`（迁移 v7：user_bookmarks）
- `internal/books/bookmarks.go`（收藏读写）
- `internal/books/books.go`（Book.IsFavorite 运行时填充、List/GetMeta 附带收藏）
- `internal/server/library.go`（favorite / history / discover / version / latest-hash）
- `internal/server/server.go`（路由注册）
- `internal/config/config.go`（APP_VERSION / APP_CHANGELOG / APP_MIRRORS / APP_APK_SHA256）
- `internal/server/library_test.go`（集成测试）

**客户端（commonMain）**
- `model/Models.kt`（History/Discover/AppVersion/Favorite 等 DTO）
- `net/NovelApi.kt`（history/discover/setFavorite/appVersion/latestHash）
- `app/AppState.kt`（四 Tab、发现/足迹/收藏/更新/亮度/预读、UpdateUiState）
- `app/AppVersion.kt`（`expect fun appVersionName()`：版本号由构建注入，避免自报版本永久落后）
- `ui/App.kt`（底部导航）、`ui/DiscoverScreen.kt`、`ui/HistoryScreen.kt`、`ui/LibraryScreen.kt`、`ui/SettingsScreen.kt`、`ui/ReaderScreen.kt`、`ui/Theme.kt`
- `update/Sha256.kt`、`update/UpdatePlanner.kt`（AppUpdater 接口）
- `reading/PrefetchGate.kt`、`reading/Kinsoku.kt`
- `sqldelight/.../ReaderDatabase.sq`（books.is_favorite、收藏查询）
- `db/ReaderRepository.kt`（ensureColumns、收藏读写）

**客户端（androidMain / androidApp）**
- `androidMain/.../update/AndroidAppUpdater.kt`（测速 + 下载 + 校验 + FileProvider）
- `androidMain/.../app/AppVersion.android.kt`（读 `BuildConfig.APP_VERSION`；`iosMain` 有对应 actual）
- `androidMain/.../data/AndroidReaderStore.kt`（幂等 ALTER）
- `androidApp/.../ReaderApplication.kt`（注入 AndroidAppUpdater）
- `androidApp/.../MainActivity.kt`（亮度钩子）
- `androidApp/src/main/AndroidManifest.xml`（REQUEST_INSTALL_PACKAGES、FileProvider）
- `androidApp/src/main/res/xml/file_paths.xml`

**测试与 CI**
- `commonTest/.../update/Sha256Test.kt`、`update/UpdatePlannerTest.kt`、`reading/PrefetchGateTest.kt`、`reading/KinsokuTest.kt`、`app/AppVersionTest.kt`
- `.github/workflows/release-apk.yml`（打 tag 构建 + 多镜像上传 + 哈希输出）

## 19. 第九阶段：多机型适配与全链路遥测

本阶段解决两件此前没做的事：**让客户端在国产 ROM 上活下来**，以及**在不碰正文的前提下把运行状况带回来**。前者是保活与合规，后者是可观测性；两者的共同约束是——数据只能描述「发生了什么」，不能描述「读到了什么」。

### 19.1 能力清单

| 类别 | 新增 |
|---|---|
| 系统适配 | Android 13+ 通知权限动态申请、忽略电池优化引导、小米/华为/OPPO/vivo/三星自启动页跳转、网络状态监听与横幅 |
| 前台服务 | `PlaybackService` 保持 `mediaPlayback`；新增 `SyncService` 声明 `dataSync` |
| 屏幕适配 | Edge-to-Edge、挖孔 Insets、阅读页「隐藏刘海」开关、书架随宽度自适应多列（折叠屏展开即两列） |
| 遥测采集 | 全局崩溃、ANR 看门狗、慢章节、TTS 首帧、连续掉帧、预读/搜索统计、接口耗时、冷启动 |
| 遥测可靠性 | 独立 `telemetry.db` 同步落盘、5 分钟同栈合并、100 行堆栈截断、5MB/1000 条 FIFO + 24h 崩溃豁免、7 天本地清理 |
| 上传策略 | WorkManager `UNMETERED` + `requiresBatteryNotLow`、进前台触发、成功即删、失败指数退避 |
| 服务端 | `POST /api/telemetry/batch`（gzip）、`telemetry_events` 明细、小时聚合 P50/P95、90 天清理、5 个控制台查询接口 |
| 控制台 | 「体验调优」大屏：KPI、机型热力图、最慢章节、崩溃堆栈展开 |

### 19.2 文件树（本阶段新增 / 修改）

```
internal/
  db/db.go                              v8：telemetry_events + telemetry_rollups + 3 条索引
  telemetry/
    model.go                            事件白名单、双层清洗、堆栈截断、敏感键剔除
    store.go                            批量写入、小时聚合、90 天清理、控制台查询
    scheduler.go                        每 10 分钟回补最近 3 小时 + 每天一次清理
    model_test.go                       清洗规则用例
    store_test.go                       聚合幂等、索引计划、清理保留聚合
  server/
    telemetry.go                        POST /api/telemetry/batch + 5 个 /api/admin/telemetry/*
    telemetry_test.go                   gzip 解压、清洗落库、越权 403、空库 KPI
  app/app.go                            装配 Store + Scheduler，随 serve 启停

web/src/
  views/Telemetry.vue                   KPI / 热力图 / 慢章节 / 崩溃列表
  api.js  router.js  App.vue            接口封装、路由与侧栏入口

android/shared/src/
  commonMain/
    telemetry/TelemetryEvent.kt         事件模型 + 类型白名单
    telemetry/TelemetrySanitizer.kt     清洗/指纹/截断（纯函数，可单测）
    telemetry/TelemetryPolicy.kt        合并窗口、上限、保留期、批次
    telemetry/TelemetryRepository.kt    同步写入、合并、淘汰、过期清理
    telemetry/Telemetry.kt              record / crash / timed / 上传触发
    telemetry/TelemetryUploader.kt      组包 + gzip + 成功后删除
    app/PlatformCapabilities.kt         NetworkStatus / KeepAliveController / TelemetryControl
    app/AppState.kt                     网络状态、预读门控、慢章节与预读/搜索埋点
    ui/App.kt                           网络横幅、状态栏 Insets
    ui/SettingsScreen.kt                系统保活 + 遥测面板
    ui/ReaderScreen.kt                  隐藏刘海、Insets
    ui/LibraryScreen.kt                 自适应多列
    net/NovelApi.kt                     uploadTelemetry(gzip)
    tts/TtsController.kt                TTS_FIRST_FRAME
  androidMain/
    telemetry/TelemetryDb.android.kt    SQLCipher 打开 telemetry.db
    telemetry/AndroidTelemetrySink.kt   同步落盘 + 上传触发
    telemetry/TelemetryWork.kt          Worker + UNMETERED 约束 + 每日清理
    telemetry/AndroidTelemetryRuntime.kt Worker/服务内的独立装配
    telemetry/Gzip.android.kt           GZIPOutputStream
    platform/NetworkMonitor.kt          NetworkCallback → AppState
    platform/AndroidKeepAlive.kt        电池优化 + 厂商自建 Intent
    net/HttpClient.android.kt           仅记录路径/状态/耗时
  androidUnitTest/
    telemetry/TelemetryRepositoryTest.kt 10 次同异常=1 条 count=10、重启可读、淘汰豁免
    telemetry/TelemetryWorkTest.kt       约束必须是 UNMETERED
  commonTest/
    telemetry/TelemetrySanitizerTest.kt  指纹稳定、敏感键剔除、100 行截断

android/androidApp/src/main/
  kotlin/.../ReaderApplication.kt       遥测装配、崩溃处理、ANR 看门狗、网络监听
  kotlin/.../MainActivity.kt            通知权限、Edge-to-Edge、冷启动与掉帧埋点
  kotlin/.../SyncService.kt             dataSync 前台服务
  AndroidManifest.xml                   FOREGROUND_SERVICE_DATA_SYNC、ACCESS_NETWORK_STATE、SyncService
  proguard-rules.pro                    保留 Worker 类名（反射实例化）
```

### 19.3 Android 13/14 适配与 ROM 保活

**权限**

| 权限 | 用途 | 申请方式 |
|---|---|---|
| `POST_NOTIFICATIONS` | TTS 与同步通知 | 启动时 `ActivityCompat.requestPermissions`（API 33+） |
| `FOREGROUND_SERVICE_MEDIA_PLAYBACK` | 朗读 | Manifest 声明，播放时 `startForeground(type=mediaPlayback)` |
| `FOREGROUND_SERVICE_DATA_SYNC` | 立即上传 | Manifest 声明，`SyncService` 以 `dataSync` 启动 |
| `ACCESS_NETWORK_STATE` | 网络监听 | 安装即授予 |
| `REQUEST_INSTALL_PACKAGES` | 应用内更新 | 安装器拉起时由系统确认 |

**保活引导**（设置 → 系统保活）

1. 「忽略电池优化」→ `ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS`，被 ROM 拦截时退回应用详情页；
2. 「自启动设置」→ 按 `Build.MANUFACTURER` 依次尝试厂商组件，全部 `resolveActivity == null` 时打开应用详情页；
3. 页面直接显示当前机型的菜单路径文案（小米「安全中心 → 自启动」、华为「应用启动管理」等）。

| 厂商 | 首选组件 |
|---|---|
| 小米 / 红米 | `com.miui.securitycenter/...AutoStartManagementActivity` |
| 华为 / 荣耀 | `com.huawei.systemmanager/...StartupNormalAppListActivity` |
| OPPO / realme / 一加 | `com.coloros.safecenter/...StartupAppListActivity`（含 oplus 回退） |
| vivo / iQOO | `com.vivo.permissionmanager/...BgStartUpManagerActivity` |
| 三星 | `com.samsung.android.lool/...BatteryActivity` |

> 跳转失败不会崩：所有 Intent 先 `resolveActivity` 再启动，异常一律吞掉并降级到应用详情。

**网络语义**

`NetworkCallback` 把状态归为三档，直接决定行为：

| 状态 | 横幅 | 预读 | 遥测上传 |
|---|---|---|---|
| `Online`（WiFi/以太网） | 无 | 允许 | 进入前台即尝试 |
| `Metered`（移动网络） | 「已暂停以省流量」 | 拒绝 | 暂缓（WorkManager 约束兜底） |
| `Offline` | 「网络已断开」 | 拒绝 | 暂缓 |

### 19.4 遥测：从采集到落盘再到上传

**采集点**

| 类型 | 触发条件 | 关键字段 |
|---|---|---|
| `CRASH` | 未捕获异常（`Thread.setDefaultUncaughtExceptionHandler`） | 完整堆栈（截断 100 行）、线程名 |
| `ANR` | 主线程 5 秒未响应（1 秒 ping 的看门狗） | 主线程堆栈、阻塞时长 |
| `SLOW_CHAPTER` | 解密 + 渲染 > 500ms | 书名·章节、章节序号、是否缓存 |
| `TTS_FIRST_FRAME` | 点击播放 → 第一块 PCM 写入 AudioTrack | 语速、起始片段 |
| `JANK` | 连续 3 帧 > 32ms | 最差帧耗时、连续帧数 |
| `PRELOAD_STATS` | 手动预取 3 章 / 滚动 80% 自动预读 | 章节数、触发方式、耗时 |
| `SEARCH_STATS` | 本地检索 | 查询长度（非内容）、命中数、耗时 |
| `API` | 每个非遥测请求 | 路径（无 query）、方法、状态码、耗时 |
| `COLD_START` | 进程启动 → 首帧绘制 | 耗时 |

**双层清洗**

客户端 `TelemetrySanitizer` 负责第一道，服务端 `telemetry.Clean` 不信任客户端再做一遍：

1. **类型白名单**：不在 11 种类型内直接丢弃；
2. **堆栈**：超过 100 行或 16KB 截断，附 `(truncated)`；
3. **上下文**：只保留标量；键名含 `token/password/secret/authorization/cookie/ciphertext/key/content/text/body/plain/audio` 整条删除；空值丢弃；最多 16 个键、单值 200 字符；
4. **数值**：`count` 钳到 1..10000，`duration` 钳到 24h，未来时间戳钳到当前时间。

**同步落盘（核心）**

- 独立库 `telemetry.db`（SQLCipher 整库加密），与 `reader.db` **物理隔离**：崩溃写入不会被业务库长事务或锁竞争挡住，「清除遥测」可整库删除；
- `insertSync` 在调用线程直接执行 SQL（事务提交后才返回），崩溃处理链路上**没有协程、没有内存队列**；
- 写入顺序：`insertSync` → `enforceCap` → 返回 → 才交给系统默认处理器杀进程。

**容量与淘汰**

| 规则 | 值 |
|---|---|
| 上限 | 1000 条 或 5MB（`bytes` 列为每行估算体积） |
| 淘汰 | FIFO，每次 50 条；`oldestEvictable` 排除「24 小时内的 CRASH/ANR」 |
| 极端情况 | 全是最新崩溃时允许暂时超限——崩溃证据优先于容量 |
| 本地过期 | 7 天未上传直接删除（每日 Worker） |

**分级上传**

- `Constraints`: `NetworkType.UNMETERED` + `requiresBatteryNotLow(true)`，移动网络下 Worker 根本不会执行；
- 触发点：`MainActivity.onResume`、登录成功、网络恢复；
- 单批 ≤ 200 条且 ≤ 512KB（压缩前），gzip 后 POST `/api/telemetry/batch`；
- 成功立刻按 id 删除，失败 `Result.retry()` 指数退避（初始 30 分钟）；401 直接结束本轮，等重新登录；
- 「立即上传」走 `SyncService`（`dataSync` 前台服务），通知栏可见，同样只在 WiFi 下真正传输。

**服务端**

- `telemetry_events`：`event_time` 为 UTC 毫秒定长文本（字典序=时间序），配 `event_time`、`(kind,event_time)`、`(device_id,event_time)` 三条索引；
- `telemetry_rollups`：`(bucket,kind,name)` 主键，存 `count/p50/p95`；
- 聚合：每 10 分钟回补最近 3 个整点，`ON CONFLICT DO UPDATE` 幂等；百分位在 Go 侧用最近秩法算（SQLite 无 `percentile`）；
- 清理：每天一次删除 90 天前明细，聚合永久保留；
- 入口限制：压缩体 ≤ 2MB、解压后 ≤ 8MB、单批 ≤ 500 条、`DisallowUnknownFields`。

**隐私红线**

| 会上报 | 绝不上报 |
|---|---|
| 事件类型、耗时、状态码、章节序号 | 书籍正文、章节内容片段 |
| 机型、系统版本、App 版本、设备 ID | 音频 PCM/密文、TTS 文本 |
| 崩溃堆栈（可执行路径） | Token、Cookie、Authorization、密钥 |
| 检索耗时与命中数 | 检索关键词原文 |

接口层不读请求体之外的东西，网络埋点不读 body、不读 header、不记 query。

### 19.5 接口

| 方法 | 路径 | 鉴权 | 说明 |
|---|---|---|---|
| POST | `/api/telemetry/batch` | device token / session | gzip 批量上报，返回 `accepted/dropped` |
| GET | `/api/admin/telemetry/overview?days=7` | 仅 session | 活跃设备、崩溃数、API/冷启动/TTS P95 |
| GET | `/api/admin/telemetry/heatmap?days=7` | 仅 session | 机型 × 类型计数（Top 300） |
| GET | `/api/admin/telemetry/slow-chapters?days=7` | 仅 session | 最慢章节 Top 20（均值/P95/峰值） |
| GET | `/api/admin/telemetry/crashes?days=7&limit=50` | 仅 session | 崩溃 + ANR，含堆栈与上下文 |
| GET | `/api/admin/telemetry/series?days=7&kind=API` | 仅 session | 小时聚合序列（趋势图） |

device token 访问控制台接口统一 403（与其它 admin 接口一致），便于客户端区分「凭证无效」与「凭证类型不允许」。

### 19.6 控制台「体验调优」

- **KPI 四卡**：活跃设备（去重）、崩溃/ANR（按 occurrence 合并）、API P95、冷启动 P95；
- **机型热力图**：行=机型（Top 10），列=事件类型（Top 8），透明度按该格计数 / 全局最大值映射；
- **最慢章节**：名称、次数、均值、P95、峰值；
- **崩溃列表**：`×N` 合并标识 + 机型/版本/时间，点击展开完整堆栈与上下文 JSON。

### 19.7 测试与验收

| 项目 | 结果 |
|---|---|
| Go：`go vet` / `gofmt` / `go test ./...` | 全绿（20 个包，含 `internal/telemetry`） |
| Go：gzip 与清洗 | `TestTelemetryBatchAcceptsGzipAndSanitizes`：解压 2 条、正文与 token 未落库、`occurrence=3` 保留 |
| Go：索引 | `TestAggregateQueryUsesIndex`：`EXPLAIN QUERY PLAN` 含 `USING INDEX` |
| Go：聚合 | `TestAggregateHourProducesP50P95`：`[100..500]` → P50=300、P95=500，重跑幂等 |
| Android：清洗与落盘 | `TelemetryRepositoryTest`：连续 10 次同异常 → 1 条 `count=10`；关闭驱动重开仍可读 |
| Android：淘汰 | 1200 条普通事件后条数受控，1 小时前的崩溃未被淘汰 |
| Android：分级策略 | `TelemetryWorkTest`：约束为 `UNMETERED` 且要求电量不低 |
| Android：构建 | `:shared:testDebugUnitTest` **71 个用例 0 失败**（本阶段新增 15 个）、`:androidApp:assembleDebug` 成功 |

真机手工验收（需自备设备）：

1. 小米/华为：设置 → 系统保活 → 「自启动设置」能跳到对应页面；开启后锁屏朗读 10 分钟不中断；
2. 断网 → 顶部出现红色横幅、预读停止；连回 WiFi → 横幅消失、下一章可秒开；
3. 移动网络下触发预读 → 提示「已跳过预取」；
4. 制造一次崩溃 → 重开 App → 设置页「其中崩溃」+1 → 连 WiFi 后条目归零；
5. 控制台「体验调优」能看到该机型、崩溃堆栈与最慢章节。

### 19.8 已知限制

1. **ANR 看门狗是应用内近似**：进程被系统冻结时看门狗自己也停摆，真正的 ANR 仍以系统 `traces` 为准；这里捕捉的是「界面卡死但进程还活着」。
2. **崩溃写入依赖 SQLCipher 可用**：若 Keystore 在崩溃瞬间不可用（极少数机型解锁前），该条会丢失；正常解锁状态下已验证可写。
3. **淘汰是「近似 FIFO」**：按 50 条一批删除，最终条数可能落在 950–1000 之间，不会精确等于 1000。
4. **热力图与 P95 是窗口内精确值**：`overview` 直接扫明细（上限 5 万行），超大数据量下应改为读 `telemetry_rollups`；私有站规模足够。
5. **厂商页面可能变更**：ROM 升级会改组件名，跳转失败时统一降级到应用详情页，不保证每次都能直达开关。
6. **遥测开关是编译期默认开启**：当前未在 UI 提供「完全关闭采集」的开关，只提供「清空本地」；如需合规开关，应在 `Telemetry.enabled` 上接一个设置项。

---

## 20. 第九阶段（整合版）：架构升维与重度阅读体验

前八阶段的功能是「能读、能听、能同步」，这一阶段补的是**让这些能力在长期使用中不出错**：删掉的书不能变成幽灵数据、设备改时间不能污染进度、盗版广告不该被念出来、离线读的内容不该丢。六项能力里有两项（软删除闭环、服务端授时）属于对既有实现的收口，其余四项是新功能。

### 20.1 能力清单

| 类别 | 新增 |
|---|---|
| 数据一致性 | 软删除 + 墓碑下发 + 客户端清理元数据/密文/索引；TTS 缓存按「书籍已删除」立即回收 |
| 时钟治理 | 进度接口忽略请求体里的任何时间字段，只用服务端 UTC；LWW 合并；客户端离线进度弹窗 |
| 正文净化 | `REGEX_PURGE_RULES` 多规则合并扫描、RE2 线性时间、规则指纹参与 ETag |
| TTS 双音色 | 按正则切分旁白/对话，分别用不同 voice 合成；缓存键含音色 |
| 阅读统计 | `reading_daily` 日聚合、`GET /api/stats/reading`、控制台与客户端柱状图 + 日历热力图 |
| 拼音搜索 | TinyPinyin 生成全拼/首字母写入 `books.pinyin`，支持 `xiaoshuo` / `xs` 命中 |
| 离线熔断 | 连续 3 次超时进入离线模式：横幅、只显示已缓存书、未缓存章节置灰、遥测静默堆积 |
| 数据退路 | `POST /api/admin/export`：密码二次验证，导出 TXT / EPUB（含目录）ZIP |

### 20.2 数据一致性：软删除、墓碑与授时

`books.deleted_at` 在第一阶段就存在，但当时只做到「查询过滤」。这一阶段把链路补完整：

1. **只软删，不物理删**：`DELETE /api/books/{id}` 写 `deleted_at` 并推进 `sync_seq`（这样增量同步才会带上它）。密文文件保留，便于误删恢复（当前没有恢复入口，属于已知限制）。
2. **墓碑下发**：`GET /api/sync/manifest?since=N` 的 `deleted: []string` 携带已删除书籍 ID。全量同步（`since=0`）不下发墓碑——客户端本来就要清库重建。
3. **客户端清理**：收到墓碑后 `ReaderRepository.deleteBook()` 依次删除 `chapters`（含缓存的明文/密文正文）、`chapters_fts` 的对应行、`books` 行。三张表在同一个库（SQLCipher 整库加密），删完即无残留。
4. **音频缓存回收**：`PruneCache` 开头先执行 `purgeDeletedBookSegments`——`tts_segments JOIN books WHERE deleted_at IS NOT NULL`，把对应音频文件与索引行删掉，并计入 `removed_deleted`。这一步**不受容量/时间策略影响**（零策略也会执行），服务端每 6 小时跑一次，启动时也跑一次。

授时方面：

- `PUT /api/books/{id}/progress` 的请求体只接受 `chapter_index / char_offset / percent / device_id / read_seconds`。即使客户端多传 `updated_at`，JSON 解码会直接忽略，服务端一律用 `time.Now().UTC()` 写入。
- 合并策略是 LWW（`ON CONFLICT(owner_id, book_id) DO UPDATE`），没有向量时钟——单用户多设备场景下够用，代价是「两台设备同时读不同章」会互相覆盖。
- 客户端在打开书籍且**本地存在未同步进度**时，会拉一次云端进度比较：若云端章节索引更小（说明离线期间读得更远），弹出对话框：

```
检测到离线进度
本机停在第 12 章（共 40 章），云端记录在第 5 章。
离线期间读得更远，是否跳转到本地进度？
[跳到第 12 章]  [留在云端进度]
```

选择「留在云端」会把本地记录回退到云端位置并加载该章，避免下次再弹。

### 20.3 正文净化（去广告）

配置示例（三种写法都支持，推荐换行分隔——正则里的逗号不会被误当分隔符）：

```bash
REGEX_PURGE_RULES='请记住本站[^\n]*
https?://[a-z0-9.-]*novel[^\s]*
本章未完，请点击下一页继续阅读'
```

也支持 JSON 数组与 `;;` 分隔（便于塞进单行 env）。

实现要点（`internal/purge`）：

| 关注点 | 做法 |
|---|---|
| 速度 | 所有规则编译后合并成一条 `(?:r1)|(?:r2)|...`，**一次扫描**完成；无规则时直接返回原串（零拷贝） |
| 安全 | Go regexp 是 RE2，最坏线性时间，不存在回溯爆炸；规则数 ≤ 64、单条 ≤ 512B、合并 ≤ 16KB，超限启动失败 |
| 排版 | 删句后残留的连续空行折叠为最多一个空行 |
| 可观测 | 启动日志只打印条数，不打印规则内容与命中文本 |

生效位置（都在「解密之后、输出之前」）：

- `GET /api/books/{id}/chapters/{i}/plain`（浏览器直读）
- `POST /api/books/{id}/chapters/prefetch` 的**密封包**——客户端本地缓存的就是净化后的文本
- TTS 朗读（广告句不会被念出来）
- 数据导出

**缓存一致性**：规则指纹（合并表达式的 SHA-256 前 12 位）参与章节 ETag 计算。运维改了规则并重启后，客户端 `If-None-Match` 不再命中，会自动重新拉取整章——不需要手动清缓存。

### 20.4 TTS 旁白 / 对话双音色

```bash
TTS_VOICE=narrator-voice          # 旁白（原有配置，作为默认 voice）
TTS_DIALOGUE_VOICE=dialogue-voice # 对话；留空则关闭双音色
TTS_DIALOGUE_PATTERN='[“"「『]([^”"」』\n]{1,400})[”"」』]'  # 必须含捕获组
```

切分规则（`internal/tts/dialogue.go`）：

1. 用 `DialoguePattern` 在章节里找出所有对话；
2. 捕获组 1 的内容标为「对话」，其余部分标为「旁白」（引号本身丢弃，不朗读）；
3. 相邻同类型段落合并，再各自按原逻辑做 ≤100 字的等长切分；
4. 每个片段按自己的音色合成，`CacheKey(provider, voice, speed, format, text)` 因此天然区分——同一句话在两种音色下是两条缓存，不会互相覆盖。

输出仍是「WAV 片段序列」（`X-TTS-Stream: wav-segments`），客户端按 RIFF 头切分逐段播放，因此不同音色的采样率/声道差异不会破坏播放。请求可用 `?dialogue_voice=` 覆盖配置的音色。

### 20.5 阅读统计

迁移 v9 新增：

```sql
CREATE TABLE reading_daily (
    owner_id TEXT NOT NULL,
    day      TEXT NOT NULL,   -- 服务端时区日历日（默认 Asia/Shanghai）
    seconds  INTEGER NOT NULL DEFAULT 0,
    chapters INTEGER NOT NULL DEFAULT 0,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (owner_id, day)
);
```

时长从哪来：客户端在 `reportProgress` 里带上 `read_seconds`（两次上报之间的活跃时长，单次封顶 120 秒、服务端再封顶 600 秒）。这是估算而非精确计时——翻页很快时可能少算几秒，但不会把「挂在后台」的时间算进去（阅读页打开时才开始计时）。

`GET /api/stats/reading` 返回：

```json
{
  "today_seconds": 1260, "total_seconds": 98400,
  "streak_days": 5, "best_streak": 11, "total_days": 37,
  "last_read_at": "2026-11-27T09:12:33.412Z",
  "days": [{ "day": "2026-08-30", "seconds": 0, "chapters": 0 }, "... 共 90 天"]
}
```

- 「今天还没读」不算断签：当前连续天数从今天起算，今天为 0 则从昨天起算。
- 日历窗口固定 90 天且补齐空白天，前端直接画图，不做日期补全。
- 控制台「阅读统计」页：4 个 KPI + 30 天柱状图 + 90 天热力图（纯 CSS/div，不引图表库）。
- 客户端：设置 → 阅读统计，Compose 手绘柱状图与热力图（无第三方图表依赖）。

### 20.6 拼音与模糊搜索

- Android 端用 TinyPinyin（约 250KB）把书名与作者转成**全拼**（`xiaoshuo`）与**首字母**（`xs`），写入 `books.pinyin` / `books.pinyin_initials`（后加列，`ensureColumns()` 幂等 ALTER，升级后首次启动 `rebuildPinyin()` 全量回填）。
- 查询是纯拉丁字母且长度 ≥ 2 时，在 FTS 之前先做一次 `pinyin LIKE '%q%'`，命中返回**书籍级结果**（`chapterIndex = -1`，UI 显示「打开这本书」）。
- 中文正文仍是 FTS5 trigram 子串匹配（错别字/漏字不会命中，这是 trigram 的固有限制，已在已知限制中说明）；FTS 不可用时回退 `LIKE`。

### 20.7 离线降级熔断

`NetworkCircuit`（进程级单例）只统计**超时**：

```kotlin
连续 3 次超时 → offline = true
任意一次成功 → 立即恢复
4xx/5xx 不计（服务器活着，只是这个请求失败）
```

进入离线模式后：

| 位置 | 行为 |
|---|---|
| 顶部 | 红色横幅「服务器连接失败，已进入离线模式」+ 重试按钮（重置计数并 sync） |
| 书架 | 只显示「至少有一章缓存」的书，并标注「离线模式：仅显示已缓存书籍（3/12）」 |
| 目录 | 未缓存章节置灰、不可点击；「下一章」在目标章未缓存时禁用 |
| 错误提示 | `friendly()` 统一改成离线文案，不弹错误框 |
| 遥测 | 继续本地落盘，WorkManager 仍受 UNMETENRED 约束，不会偷跑流量 |

**顺带修掉的一个真 bug**：`RawDb.bind` 把参数索引写成 `i + 1`，而 SqlDelight 的 `SqlPreparedStatement` 约定是 **0-based**（Android 驱动内部 `index + 1`，JDBC 驱动直接透传给 sqlite-jdbc 的 0-based API）。结果是所有带 ≥2 个参数的 raw SQL 都绑定错位，JVM 上直接数组越界。拼音索引测试把这个潜伏问题炸了出来，现已修正——FTS 写入、按书删除缓存等路径因此变正确。

### 20.8 防锁定数据导出

```
POST /api/admin/export
{ "password": "<当前管理员密码>", "book_ids": ["..."], "format": "txt" | "epub" }
```

- 仅浏览器会话（`requireSession`）可调用，且必须再次输入**当前账户密码**；错误按 IP 限流（复用登录限流器），统一返回 401，不区分「密码错」与「用户不存在」。
- 响应是**流式 ZIP**：`Content-Type: application/zip`，边解密边写，不落地中间明文文件。
- 单本失败（解密失败/文件缺失）只跳过该书并记审计，不中断整包。
- 文件名用 `SafeFileName` 清洗：去掉 `/ \ : * ? " < > |`、控制字符、前后点号，限长 80 字符；重名追加 `(2)`。
- EPUB 是最小合规结构：`mimetype`（必须是第一个且 STORED）+ `META-INF/container.xml` + `content.opf` + `nav.xhtml` + `toc.ncx`（兼容老阅读器）+ 每章一个 XHTML；所有文本经 XML 转义。
- 导出内容同样应用净化规则，与在线阅读看到的一致。

### 20.9 文件树（本阶段新增 / 修改）

```
internal/
  purge/purge.go                        规则编译、合并扫描、空行折叠、指纹
  purge/purge_test.go                   删广告/不改正文/指纹/非法规则/长文本
  tts/dialogue.go                       旁白-对话切分（捕获组语义）
  tts/dialogue_test.go                  切分、相邻合并、双音色缓存键
  tts/service.go                        seg.voice、buildSegments 双音色、synthesize 用片段音色
  tts/admin.go                          PruneCache 前置软删除回收 + RemovedDeleted
  stats/stats.go                        AddDaily / Overview / streaks
  stats/stats_test.go                   累加、封顶、连续天数、断签
  export/export.go                      TXT、EPUB、SafeFileName、EntryName
  export/export_test.go                 EPUB 结构、转义、路径穿越、重名
  server/stats.go                       GET /api/stats/reading
  server/export.go                      导出预览与流式导出
  server/stage9_test.go                 授时、净化、导出、墓碑、缓存回收
  server/device.go                      进度接收 read_seconds + 审计
  server/server.go                      Deps.Stats、三条新路由
  books/books.go                        Service.Purge / PurgeVariant / PurgeText
  books/chapters.go                     ChapterETag 带指纹、ChapterPlain 净化
  books/device.go                       prefetch 密封前净化
  mobile/manifest.go                    BuildManifestWithVariant（ETag 指纹）
  config/config.go                      REGEX_PURGE_RULES / TTS_DIALOGUE_* / READING_TZ / EXPORT_MAX_BOOKS
  db/db.go                              v9：reading_daily
  app/app.go                           purge 引擎、stats 服务、每 6 小时音频回收

web/src/
  views/Stats.vue                       阅读统计 + 数据导出
  api.js  router.js  App.vue            getReadingStats / downloadExport / 路由与侧栏

android/shared/src/
  commonMain/
    search/Pinyin.kt                    PinyinProvider 接口 + None
    net/OfflineCircuit.kt               超时计数与离线状态
    db/ReaderRepository.kt              pinyin 列、updatePinyin/rebuildPinyin、拼音命中
    db/RawDb.kt                         修正 0-based 参数绑定
    model/Models.kt                     ReadingStats / ReadingDay / read_seconds
    net/NovelApi.kt                     readingStats()
    app/AppState.kt                     offline / cachedIds / 离线进度弹窗 / 阅读计时
    ui/App.kt                           离线横幅、Stats 路由
    ui/StatsScreen.kt                   KPI + 柱状图 + 热力图
    ui/LibraryScreen.kt                 离线过滤与计数
    ui/ReaderScreen.kt                  目录置灰、下一章禁用、离线进度对话框
    ui/SettingsScreen.kt                阅读统计入口
  androidMain/
    search/Pinyin.android.kt            TinyPinyin 实现
    data/AndroidReaderStore.kt          注入 TinyPinyinProvider + 回填
    net/HttpClient.android.kt           拦截器接入熔断器
  commonTest/net/OfflineCircuitTest.kt  3 次超时、成功重置、非超时不计
  androidUnitTest/db/PinyinSearchTest.kt 全拼/首字母/作者/子串/非拉丁
```

### 20.10 测试与验收

| 项目 | 结果 |
|---|---|
| Go `gofmt` / `go vet` | 无差异、无告警 |
| Go `go test ./...` | 20 个包全绿（server 17.1s） |
| 净化 | `TestApplyRemovesAds`（广告与 URL 删除且正文保留）、`TestApplyCollapsesBlankLines`、`TestManyRulesStillLinear`（40 条规则 × 20 万字） |
| 双音色 | `TestSplitDialogueSeparatesNarration`、`TestBuildSegmentsUsesDifferentVoices`、`TestBuildSegmentsKeepsOrderAndText`（引号丢弃但字符不丢） |
| 统计 | `TestProgressIgnoresClientClock`：请求体带 `updated_at: 2000-01-01`，返回的仍是服务器当前时间；`today_seconds ≥ 120`；日历窗口 90 天 |
| 净化接口 | `TestPurgeRemovesAdsFromChapterPlain`：`/plain` 不含「请记住本站」，正文完整，无多余空行 |
| 导出 | `TestExportRequiresPasswordAndProducesZip`：错密码 401；正确密码返回 ZIP，内容已净化；`TestExportRejectsUnknownFormat` |
| 软删除 | `TestSoftDeleteSendsTombstone`：增量 manifest 命中 `deleted`，书籍不在增量列表，`/plain` 404 |
| 缓存回收 | `TestSoftDeleteReclaimsAudioCache`：软删后零策略 `PruneCache` 回收全部片段，`tts_segments` 归零 |
| Android 单测 | `:shared:testDebugUnitTest` **82 个用例 0 失败**（本阶段新增 11：拼音 6、熔断 5） |
| Android 构建 | `:androidApp:assembleDebug` 成功 |

真机手工验收（需自备设备）：

1. 控制台删除一本书 → 客户端「同步」→ 书架该书消失，进搜索搜不到它的章节；
2. 把设备时间改到 2000 年 → 翻页触发上报 → 控制台足迹里该书仍排在最前（`updated_at` 是服务器时间）；
3. 配置 `REGEX_PURGE_RULES` 后上传含「请记住本站」的 txt → 阅读页该句消失，且首次打开会自动重拉（ETag 变了）；
4. 配置 `TTS_DIALOGUE_VOICE` 后朗读一段带引号对话 → 对话部分音色不同；
5. 搜索框输入 `xiaoshuo` 与 `xs` → 都能命中《小说》；
6. 拔网线/关服务器 → 连续操作 3 次 → 顶部出现离线横幅，未缓存章节变灰；恢复后点「重试」横幅消失；
7. 控制台「阅读统计」→ 导出 EPUB → 用任意阅读器打开，目录与章节完整。

### 20.11 已知限制

1. **没有「恢复已删除书籍」入口**：软删除只保证数据不丢，恢复需要手工改 `deleted_at`。设计上留了空间，但本阶段没做 UI。
2. **LWW 会覆盖**：两台设备同时读不同章时，后上报的一方胜出，落后的进度不保留。私有单用户场景可接受。
3. **阅读时长是估算**：来自进度上报的时间差（单次 ≤120s），不是精确的前台计时；长时间停留在一页不翻页时，只在下次上报时补计一次。
4. **拼音多音字取首读音**：`重庆` 会得到 `zhongqing`，这是 TinyPinyin 字表级方案的固有限制；书名场景影响很小。
5. **错别字不模糊**：trigram 是子串匹配，`小説`（日文汉字）不会命中 `小说`。真正的编辑距离模糊需要额外索引，未做。
6. **净化正则仍可能误伤**：例如规则写成 `第.*章` 会把正文里的「第一章节」也删掉。建议先用 `DryRun` 思路小范围验证——当前没有预览接口，只能改配置后重启观察。
7. **导出是同步请求**：整库导出期间占用一个 HTTP 连接与一把解密锁，大库（>200 本）建议分批；`EXPORT_MAX_BOOKS` 默认 200 会直接拒绝超量请求。
8. **双音色不跨段混读**：一个片段只会用一种音色。若对话与旁白在同一句里（「他说：」+ 引号内容被切在同一 100 字块内），切分仍按引号边界处理，不会出现半句换声。

---

## 21. 第十阶段：网络适配与动态根地址

本阶段解决「换服务器要重装 App」「无公网无法配对」「多端时钟/地址各自写死」三类落地痛点。
客户端不再硬编码任何 API 域名；服务端在局域网内可被自动发现，并用 6 位短码换取设备凭证。

### 21.1 能力清单

| 编号 | 能力 | 落点 |
|---|---|---|
| E1 | 动态根地址单例 `ApiEndpointManager` | 客户端 commonMain + Android SharedPreferences |
| E1 | 手动改域名 / 剪贴板 JSON·Base64 导入 | 设置页「服务器设置」 |
| E1 | 双节点探测：local/healthz 500ms 优先，不通降级公网 | OkHttp 拦截器 + TTL 缓存 |
| E2 | mDNS `_novelstack._tcp` + UDP 广播发现 | 服务端 `internal/lan`；客户端 NsdManager |
| E2 | 6 位短码配对（IP 绑定 + 一次性 + 限流） | `POST /api/lan/pair`；控制台二维码 |
| E2 | 内网网段明文 / 自签证书豁免 | `network_security_config.xml` |

### 21.2 动态根地址：不再有硬编码域名

- `ApiEndpointManager`（commonMain 全局单例）持有 `primaryUrl`（公网）+ `localUrl`（内网）+ `deviceToken`，持久化到 `EndpointStore`（Android = SharedPreferences）。
- `NovelApi` 所有请求都经 `currentBaseUrl()` 取地址，登录页默认值也来自管理器——代码里不再出现固定的生产 API 域名。
- **剪贴板导入**：解析 `{"url":"...","local_url":"...","token":"..."}` 或其 Base64，一键原子切换地址与凭证（避免「换了服务器还带旧 token」引发一串 401）。`exportConfig()` 可导出 Base64 给其它设备。
- **双节点降级**：OkHttp 拦截器在每次请求前探测 `local_url/healthz`（连接/读写均 500ms 超时）。通 → 改写本次请求 host/port 到内网；不通 → 走公网。探测结果带 10 秒 TTL 缓存，避免每请求都等待；引导类请求（登录、LAN 配对）不改写，防止连错服务器。

### 21.3 局域网发现（mDNS + UDP 兜底）

- 服务端 `internal/lan` 用标准库实现两条发现通道，不引入第三方依赖：
  1. **mDNS**：组播 `224.0.0.251:5353` 应答 `_novelstack._tcp.`，返回 PTR/SRV/TXT/A，NsdManager 可直接发现；
  2. **UDP 广播**：监听 `LAN_BROADCAST_PORT`（默认 5359），收到 `{"type":"probe"}` 回 `{"type":"answer",host,port,endpoint}`，应对国内 ROM 屏蔽组播的情况。
- 只广播「地址 + 端口 + 服务名」，不含任何书籍数据或凭证。
- 客户端 `AndroidLanDiscovery` 用 NsdManager 解析服务属性，同时发 UDP 探测兜底，两条结果合并去重。
- 无可用内网地址（如纯 loopback 容器）时自动关闭发现服务，不阻断启动。

### 21.4 6 位短码配对（低熵凭证的防护）

现有一次性配对码是 `nsp_`+base64（64 位熵）。6 位数字只有约 20 位熵，**不能直接复用**，因此新增独立表 `lan_pair_codes`（迁移 v10）与四重防护：

1. **绑定来源 IP**：生成与兑换必须来自同一 IP，短码被截屏后异地使用无效；
2. **短 TTL**：默认 5 分钟（`LAN_PAIR_TTL_MINUTES`）；
3. **一次性**：兑换成功后原子置 `used_at`，重放返回 401；
4. **每 IP 限流**：`LAN_PAIR_PER_MINUTE`（默认 8）滑动窗口。

数据库只存短码 SHA-256（带域分隔前缀），明文只在生成响应里出现一次。兑换成功后由 `devices.Store.Create` 签发与浏览器会话无关的长期 device token，后续权限与现有设备完全一致（只能读密文/元数据，不能访问 `/api/admin/*`）。

**配对链路**：控制台「设备与配对 → 生成局域网码」→ 得到 6 位码 + 可扫码二维码（载荷 `{"v":1,"endpoint":"...","code":"..."}`，二维码由前端零依赖 `qrcode-generator` 本地生成，图像不上传）→ App「服务器设置 → 扫配对二维码」或「扫描局域网 + 手动输入 6 位码」→ 换取 token → 自动把发现到的内网地址设为 `localUrl`。

### 21.5 内网明文与自签证书豁免

Android 的 `network-security-config` 的 `<domain>` 不支持 CIDR 网段，而内网 IP 由 DHCP 动态分配、无法枚举。因此采用**全局允许明文 + 信任用户 CA**：

- `cleartextTrafficPermitted="true"`：允许 `http://192.168.x.x` 直连；
- `<certificates src="user"/>`：信任用户在系统里安装的自签根证书；
- 公网地址仍由 `NovelApi.normalizeServer` 强制补 `https://`，明文只发生在用户显式输入 `http://` 内网地址时；
- 正文始终是密文、token 只在内存与本地配置，不因明文传输而泄漏正文。

若部署环境要求严格禁明文，把 `cleartextTrafficPermitted` 改回 `false` 并删除 `user` 证书源即可（局域网 http 直连随之失效）。

### 21.6 文件树（本阶段新增 / 修改）

```
internal/lan/
  pair.go            # 6 位短码：生成/兑换/IP 绑定/一次性/限流
  ratelimit.go       # 每 IP 滑动窗口限流
  dns.go             # 最小 DNS 编解码（A/PTR/SRV/TXT）
  discovery.go       # mDNS 组播 + UDP 广播发现服务
  *_test.go          # 短码、限流、DNS 应答单测
internal/db/db.go                    # 迁移 v10：lan_pair_codes
internal/config/config.go            # LAN_* 配置与校验
internal/server/lan.go               # /api/admin/lan/pairing、/api/lan/pair、/api/lan/info
internal/server/server.go            # 路由 + Deps.LANPairs/Discovery
internal/app/app.go                  # 装配 + 生命周期（启动/关闭发现）
android/shared/src/commonMain/.../net/
  EndpointStore.kt                   # 配置存储抽象
  ApiEndpointManager.kt              # 动态根地址单例
android/shared/src/androidMain/.../net/
  AndroidEndpointStore.kt            # SharedPreferences 实现
  HttpClient.android.kt              # 500ms 探测 + host 改写拦截器
android/shared/src/androidMain/.../platform/
  AndroidLanDiscovery.kt             # NsdManager + UDP 兜底
android/shared/src/commonMain/.../
  app/LanDiscoveryController.kt
  model/Models.kt                    # LanPairingPayload/LanInfo/LanService/LanPairRequest
  ui/ServerSettingsScreen.kt         # 服务器设置页
android/androidApp/.../
  MainActivity.kt                    # ZXing 扫码回调
  ReaderApplication.kt               # 注入 manager/discovery
  AndroidManifest.xml                # 组播/相机权限
  res/xml/network_security_config.xml# 内网明文豁免
android/gradle/libs.versions.toml    # zxing 4.3.0
web/src/views/Devices.vue            # 局域网码 + 二维码
web/src/vendor/qr*.js                # 零依赖 QR 生成
.env.example / README.md
```

### 21.7 测试与验收

自动化：
- `go test ./...`：`internal/lan`（短码一次性、IP 绑定、过期、限流、DNS 应答）与 `internal/server/stage10_test.go`（生成→兑换→重放失败、坏码拒绝、info 无鉴权）。
- `:shared:testDebugUnitTest`：`ApiEndpointManagerTest` 9 例（双节点选择、TTL 缓存、JSON/Base64 导入、导出往返、LAN 载荷解析、地址规范化）。
- `:androidApp:assembleRelease`（R8）、`npx vite build`。

真机手工验收：
1. 控制台「生成局域网码」→ 二维码出现；手机连同一 WiFi，App「服务器设置 → 扫配对二维码」→ 成功进入书库；
2. 用 6 位码在 A 手机配对后，B 手机用同码应失败（一次性）；把码给不同 IP 的设备应失败（IP 绑定）；
3. 设置里把主地址改成另一台服务器的 `http://192.168.x.x:8080` → 保存 → 同步走新地址；关掉那台服务器 → 自动降级回公网；
4. 剪贴板导入 Base64 配置 → 一键切到另一台服务器（含 token，无需重新配对）；
5. 无公网环境：关掉公网出口，仅靠局域网发现 + 6 位码完成首次配对与同步。

### 21.8 已知限制

1. **局域网 IP 动态变化**：DHCP 重分配后需重新生成配对码并发现；发现列表是即时的，不持久化。
2. **mDNS 依赖系统支持**：部分定制 ROM 屏蔽组播，此时靠 UDP 广播兜底；两者都不可用时只能手动输入地址。
3. **明文是全局放行**：为了覆盖动态内网 IP，`network_security_config` 对**所有**域名放开明文；公网地址靠 `https://` 前缀约束，但恶意中间人若能把地址改成 `http://` 仍有风险——请勿把 App 装到不可信环境。
4. **6 位码仍有理论暴力空间**：IP 绑定 + 每 IP 每分钟 8 次 + 5 分钟有效，把同网段爆破成本抬高，但私有部署仍建议优先用长配对码。
5. **ZXing 扫码需相机权限**：用户拒绝后只能手动输入 6 位码或剪贴板导入。
6. **探测 TTL 内地址不刷新**：10 秒内内网恢复/断开不会立刻反映到下一次请求，需等 TTL 过期或手动改地址。

## 22. 第十一 + 十二阶段（合并）：苹果风重构、前后台分离、Web 阅读器与多端同步

这一阶段做的是「同一个站点，两种身份，三种屏幕」。运营控制台留在 `/admin`，普通阅读拆到 `/`；
视觉上全端统一到苹果的连续曲率（G3）与纯色层级；数据面把 WebSocket 换成前台轮询 + 位置优先合并，
让两台设备同时读同一本书时，谁都不会把对方往回拽。

### 22.1 能力清单

| 编号 | 能力 | 落点 |
|---|---|---|
| A1 | G3 连续曲率（禁直角/禁 G2 圆角） | Android `RoundedPolygon.toPath()`；Web 九宫格 `mask-box-image` |
| A2 | 纯色层级：`#F2F2F7` 页面 / `#FFFFFF` 卡片 / `0.5px` 内描边 | `AppleTokens`、`styles.css :root` |
| A3 | 胶囊搜索框 + 分组内嵌列表 + 长按多选 | `CapsuleSearchField`、`GroupedList`、`LibraryScreen` |
| A4 | 弹簧动效（`dampingRatio=0.75`）、hover 上浮、按压缩放、禁方角水波纹 | `AppleUI.kt`、`.card.hoverable` |
| B1 | 断点 1440 / 1279 / 1023 / 767，手机 1 列 | `styles.css` 媒体查询 |
| B2 | 移动端表格降级为卡片列表（`td::before{content:attr(data-label)}`） | 9 个表格页统一加 `data-label` |
| B3 | `viewport-fit=cover` + 一次性 grant 免密进 WebView | `index.html`、`/api/device/web-grant` |
| C1 | 用户端与运营端彻底分离 | `/`(书架) `/discover` `/history` `/settings` `/read/:id`；`/admin/*` |
| C2 | 去掉 WebSocket，前台 5 分钟轮询 + 切前后台立即同步 | `UserShell.vue`、`AppState.startForegroundSync()` |
| C3 | 位置优先合并（章 → 字符偏移取最远） | `mobile.Position.Ahead()`、`MergeProgress` |
| D1 | Web Crypto AES-GCM + 每标签页 RSA 密钥对 + Worker 解密 | `crypto/keys.js`、`crypto/decrypt.worker.js` |
| D2 | Service Worker + IndexedDB 缓存密文，离线可读 | `public/sw.js`、`offline/idb.js` |
| D3 | 苹果排版 + 原生 `speechSynthesis` 朗读 | `ReaderView.vue` |
| E1 | 路由懒加载、ECharts 按需、首屏内联骨架 | `router.js`、`ReadingChart.vue`、`index.html` |
| E2 | Data-label 卡片化后无横向滚动 | 390 / 834 / 1440 三档实测 `scrollWidth == clientWidth` |

### 22.2 G3 曲率：两次踩坑之后的正确做法

「苹果风」和「圆角卡片」的差别只有一件事：圆角的曲率是否连续。`border-radius` 画的是圆弧，
圆弧与直边的交界处曲率从 1/r 突变到 0，放大后能看出「接缝」；iOS 用的是超椭圆
`|x|ⁿ + |y|ⁿ = 1`（n ≈ 4.2），交界处曲率为 0，所以过渡是「化开」的。

**Android** 用 `androidx.graphics:graphics-shapes`：

```kotlin
// androidMain/ui/Squircle.android.kt
val rounded = RoundedPolygon.rectangle(
    width = size.width, height = size.height,
    rounding = CornerRounding(radius = r, smoothing = smoothing.coerceIn(0f, 1f)),
    centerX = size.width / 2f, centerY = size.height / 2f,
)
return Outline.Generic(rounded.toPath().asComposePath())
```

`smoothing = 0.6f` 是 Material 3 expressive 推荐的连续度；`Outline.Generic` 直接喂给 `Shape`，
`SquircleCard` / `Surface` / `Card` 都能用，不依赖 Material 的 `RoundedCornerShape`。
iOS 侧没有等价 API，`Squircle.ios.kt` 退化为 `RoundedCornerShape(radius)`（形状正确、曲率不连续）。

**Web** 走 CSS mask，但**不能用 `mask-size: 100% 100%`**——这是第一版实现，直接把 100×100 的
超椭圆拉伸到元素尺寸，结果 90px 高的卡片被拉成胶囊、320px 高的卡片变成巨角。圆角半径必须与
元素尺寸解耦，因此改用九宫格（9-slice）：

```css
/* 源图 64×64、角半径 16；slice=16 让四个角等比缩放、四条边无限拉伸 */
--sq9: url("data:image/svg+xml;utf8,<svg width='64' height='64' …>");

.g3, .card::before, .btn, .modal, .brand .mark, .nav a, .bookcard .cover, .tag {
  --sqr: 12px;                          /* 视觉圆角，各组件自己覆盖 */
  border-radius: var(--sqr);            /* 兜底：不支持 mask-box-image 的引擎退化成 G2 */
  -webkit-mask-box-image: var(--sq9) 16 fill / var(--sqr) stretch;
  mask-border: var(--sq9) 16 fill / var(--sqr) stretch;
}
.card::before, .modal, .tbl tr::before { --sqr: 16px; }   /* 大面 */
.brand .mark, .bookcard .cover, .nav a  { --sqr: 10px; }   /* 小面 */
.tag                                     { --sqr: 7px; }    /* 微面 */
```

再补两条踩过的坑，留给后来人：

1. **超椭圆的参数化**是 `u = r·cos^(2/n)θ`、`v = r·sin^(2/n)θ`，写成 `r(1−cos^(2/n)θ)` 得到的是
   **内凹**角（画出来是一朵风车），因为那等价于「从切点往圆心方向收」。
2. 九个切片里，角部切片必须按**顺时针**接续：右下 `θ:0→π/2`、左下 `π/2→0`、左上 `0→π/2`、
   右上 `π/2→0`。方向错了不会报错，只会得到一个自交的路径。

### 22.3 纯色层级，不做毛玻璃

| 角色 | 值 | 说明 |
|---|---|---|
| 页面底色 | `#F2F2F7` | `body` / `--bg` |
| 卡片 | `#FFFFFF` | 只靠 0.5px 内描边 `rgba(60,60,67,.12)` 与页面区分 |
| 主文本 | `#1C1C1E` | 标题 600 / 正文 400 |
| 次文本 | `#8E8E93` | 说明、单位、占位 |
| 强调 | `#007AFF` | 链接、主按钮、选中文字 |
| 危险 | `#FF3B30` | 删除、解封 |
| 选中底色 | `#E6F0FF` | 列表选中行 / 导航当前项 |

半透明一律换成纯色：侧栏 `#FAFAFC`、底栏 `#FFFFFF`——半透明叠在页面上，观感就是毛玻璃，
且滚动时文字会透出来。阴影只保留两种：hover 时的 `drop-shadow(0 4px 12px rgba(0,0,0,.05))`，
以及按压缩放 `scale(.98)`。

### 22.4 前后台分离与路由

服务端 `internal/server/web.go` 注册用户端路由，与 `/admin` 共用同一份内嵌 SPA：

| 路径 | 归属 | 未登录行为 |
|---|---|---|
| `/` `/library` `/discover` `/history` `/settings` `/read/{id}` | 用户端 | 303 → `/login?next=<原路径>` |
| `/login` | 登录页（用户端默认回跳 `/library`） | 已登录则 303 → `/library` |
| `/admin` `/admin/*` `/admin/login` | 运营端 | 303 → `/login?next=/admin` |
| `/assets/*` | 构建产物（公开，不含数据） | — |

登录页是 `loginpage.html` 单文件内嵌模板，`serveLoginPage` 按回跳地址替换 `__NEXT__` 与 `__AREA__`
（控制台 / 私人阅读站），因此两个入口不会出现「标题写着控制台、其实是书架」的自相矛盾。
前端 `router.js` 全量路由懒加载，`/admin` 下 10 个子页各自成 chunk。

### 22.5 一次性 grant：WebView 免密登录

Android 设置页「打开 Web 控制台」不能把长期口令塞进 URL（会进历史、进日志、被 WebView 记住），
所以走一次性票据：

```
POST /api/device/web-grant   (Bearer 设备令牌)
  → {"grant":"<256bit base64url>","url":"https://host/login?grant=…","expires_at":…}
GET  /login?grant=…          （页面自动 POST 下一跳）
POST /api/login/grant        {"grant":"…"}
  → Set-Cookie: 会话 Cookie（HttpOnly + SameSite=Strict）302 到 /admin
```

- 60 秒有效、**只能兑换一次**、最多同时存 64 张，与登录共用同一个 IP 限流器；
- 页面侧 `safeNext()` 用 `new URL(v, location.origin)` 校验同源，挡掉 `//evil.com` 与反斜杠变体；
- 票据不落库、不写日志，兑换即删除。

### 22.6 多端同步：位置优先，不做 last-write-wins

同一本书在两台设备上，A 读到第 30 章、B 昨天读到第 12 章今天随手翻回去看一眼——如果按「最后写入
获胜」，A 会被拽回第 12 章。因此服务端合并规则改为**位置优先**：

```go
// internal/mobile/syncbatch.go
type Position struct{ ChapterIndex int; CharOffset int }

// Ahead 先比章，章相同再比章内字符偏移；完全相等返回 false（幂等）
func (p Position) Ahead(o Position) bool

// 单本路径与批量路径共用同一判定
if !(Position{p.ChapterIndex, p.CharOffset}).Ahead(Position{cur.ChapterIndex, cur.CharOffset}) {
    return cur, nil   // 就地返回已有进度，不写库
}
```

- `POST /api/sync/progress` 批量上限 200 条，返回 `applied`（真正改写了库的条数）与 `stored`（并入后的最终位置）；
- `GET /api/sync/progress?since=<cursor>` 增量拉取，`cursor` 单调递增，客户端只需保存最后一次；
- 不同书的进度互不影响，按各自更新时间排序；
- 客户端 `AppState.pullProgress()` 在**前台**每 5 分钟跑一次，`ON_START`/`ON_STOP` 立即触发一次并在后台停止计时器——
  没有 WebSocket，也就没有重连风暴与心跳耗电。

### 22.7 Web 阅读器：密钥不过网、不落盘

| 环节 | 做法 |
|---|---|
| 密钥交换 | 每个标签页生成一对 RSA-OAEP-256 密钥，公钥（SPKI PEM）登记给服务端换 DEK |
| 私钥 | 只存 `sessionStorage` 的 JWK；**关闭标签页即清除** |
| DEK | 只在内存变量里；不写 localStorage / IndexedDB / Cookie |
| 解密 | `crypto/decrypt.worker.js` Web Crypto AES-GCM，NSCH 包格式与移动端一致（AAD 绑定 bookID + 头 16 字节） |
| 缓存 | IndexedDB 只存**密文**，离线时先取密文再解密 |
| 外壳 | Service Worker 只缓存 app shell；`/api/` 永远走网络；navigate 请求网络优先、失败回退缓存 |

结果：关掉标签页，DEK 随内存消失；重开需要重新登录换 DEK，已缓存的密文在没有 DEK 的情况下等同噪音。
这是刻意的取舍——离线可读，但离线不等于「密钥永久驻留浏览器」。

### 22.8 响应式与表格降级

断点：`≥1440` 4 列 / `1280–1439` 3 列 / `1024–1279` 3 列 / `768–1023` 2 列（侧栏转顶部横条）/
`<768` 1 列。手机端每张卡片 16px 外边距，首屏 `padding: 16px`。

表格降级不用两份模板，纯 CSS：

```css
@media (max-width: 767px) {
  .tbl, .tbl tbody { display: block; }
  .tbl thead { display: none; }
  .tbl tr { display: block; margin-bottom: 10px; position: relative; }  /* 每行一张卡 */
  .tbl tr::before { /* 同一套 --sq9 蒙版画卡片底 */ }
  .tbl td { display: flex; justify-content: space-between; text-align: right; }
  .tbl td::before { content: attr(data-label); color: var(--secondary); }  /* 字段名 */
  .card:has(> .tbl) { padding: 0; }            /* 避免「卡片套卡片」 */
  .card:has(> .tbl)::before { content: none; }
}
```

配套改动：`Audit` / `Devices` / `Cache` / `Search` / `Security` / `TTS` / `Telemetry` /
`Overview` / `Blocklist` 九个页面的每个 `<td>` 都补了 `data-label`（黑名单页原本是 `div` 拼的行，
本阶段改成真正的 `<table>`，桌面端才有对齐的列）。

### 22.9 性能

- **路由级懒加载**：`/admin/audit` 等 10 个子页 + 用户端 5 个视图各自成 chunk，首屏只加载 `vue` + `index`。
- **ECharts 按需**：`echarts/core` + `BarChart` + `GridComponent`/`TooltipComponent` + `CanvasRenderer`，
  且只在 `/admin/stats` 动态 `import()`。构建产物里 echarts 被拆成 `charts` / `components` / `Axis` /
  `graphic` / `renderers` 五个按需 chunk（合计 ~880 KB），**首屏一个都不加载**。
- **首屏骨架**：`index.html` 内联三段灰条 + 三张卡片占位（~600 B），Vue 挂载后由真实内容替换，
  避免白屏；骨架不引外部 CSS，因此不受构建产物体积影响。
- **Compose**：`LazyColumn` 全部带 `key`，列表过滤走 `derivedStateOf`，避免每次重组重算。

### 22.10 文件树（本阶段新增 / 修改）

```
web/src/
├── styles.css                 ★ 重写：G3 九宫格蒙版、色彩令牌、断点、表格降级、阅读器、骨架
├── router.js                  ★ 用户端/运营端分流 + 全量懒加载
├── main.js                    ★ 注册 Service Worker
├── App.vue                    ★ 只剩 <router-view/>
├── userApi.js  user/store.js  ★ 用户端 API 与状态（5 分钟轮询、visibilitychange）
├── crypto/keys.js             ★ 每标签页 RSA 密钥对，私钥仅 sessionStorage
├── crypto/nsch.js             ★ NSCH 包解析（与移动端同格式）
├── crypto/decrypt.worker.js   ★ Web Crypto AES-GCM 解密（离开主线程）
├── offline/idb.js             ★ 密文缓存（IndexedDB）
├── components/ReadingChart.vue ★ ECharts 按需 + ResizeObserver
└── views/
    ├── AdminShell.vue         ★ 导航改 /admin/*，退出走 location.replace
    └── user/                  ★ UserShell / ShelfView / DiscoverView / HistoryView /
                                 UserSettingsView / ReaderView
web/public/sw.js               ★ app shell 缓存策略
web/index.html                 ★ viewport-fit、theme-color、内联骨架
web/vite.config.js             ★ base '/'、manualChunks、/api 代理

internal/
├── server/web.go              ★ 用户端路由 + 登录页渲染 + 公共静态资源
├── server/loginpage.html      ★ 统一登录页（G3 蒙版、同源校验、grant 自动兑换）
├── server/sync.go             ★ 批量同步 / grant / Web 书库 / Web 章节
├── auth/grant.go              ★ 一次性票据（60s、单次、上限 64）
├── mobile/syncbatch.go        ★ Position.Ahead / MergeProgress / ListProgress
├── mobile/manifest.go         ★ PutProgress 改位置优先
├── audit/audit.go             ★ 写失败日志带上原因
└── server/stage12_test.go     ★ 合并语义、未知书拒绝、grant 单次、Web 解密全链路

android/shared/src/
├── commonMain/.../ui/Squircle.kt          ★ expect 超椭圆 Shape
├── androidMain/.../ui/Squircle.android.kt ★ RoundedPolygon → Outline.Generic
├── iosMain/.../ui/Squircle.ios.kt         ★ RoundedCornerShape 兜底
├── commonMain/.../ui/Theme.kt             ★ 重写：AppleTokens / AppleTypography
├── commonMain/.../ui/components/AppleUI.kt ★ 弹簧、卡片、可按压、分组列表、胶囊搜索、复选框、
│                                            多选头、底部操作栏
├── commonMain/.../ui/LibraryScreen.kt      ★ 分组列表 + 长按多选 + 批量移除
├── commonMain/.../ui/SettingsScreen.kt     ★ 9 个分组 + Web 控制台入口
├── commonMain/.../ui/WebConsoleScreen.kt   ★ expect PlatformWebView
├── androidMain/.../ui/WebConsoleScreen.android.kt ★ WebView 加固（禁文件、禁混合内容）
└── commonMain/.../app/AppState.kt          ★ 前台轮询、removeLocalBooks、Screen.WebConsole
```

### 22.11 验收

自动化：

| 项 | 结果 |
|---|---|
| `go test ./...` | 23 个包 **全部 ok**（新增 `stage12_test.go` 4 例：位置合并、未知书拒绝、grant 只能用一次、Web 书库→解 DEK→解章节） |
| `:shared:testDebugUnitTest` | **99 个用例 0 失败** |
| `:androidApp:assembleDebug` | 成功（APK 正常产出） |
| `pnpm build` | 成功；33 个 chunk，首屏无 echarts |

浏览器实测（Chromium，`document.documentElement.scrollWidth == clientWidth`）：

| 宽度 | 布局 | 横向溢出 |
|---|---|---|
| 390 × 844 | 1 列；表格降级为卡片；底栏固定 | `0` |
| 834 × 1000 | 2 列；侧栏转顶部横条 | `0` |
| 1440 × 900 | 侧栏 + 4 列 | `0` |

人工验收清单：

1. 所有卡片/按钮/输入框/弹窗的角都是连续曲率，放大到 4× 看不到「圆弧接直边」的接缝；
2. hover 上浮 2px、按下缩到 0.98，无方角水波纹；
3. 手机端 Admin 每个表格页都是一列卡片、字段名在左、值在右，没有横向滚动条；
4. 两台设备读同一本书，A 读到第 30 章、B 翻回第 12 章，5 分钟内 A 仍是第 30 章；
5. 关掉 Web 标签页再打开，离线缓存的书仍需要重新登录才能读（说明 DEK 已随内存释放）；
6. 断网后刷新阅读页，Service Worker 仍能从缓存起壳、从 IndexedDB 取密文并解密。

### 22.12 已知限制

1. **`mask-border` 只有 `-webkit-` 前缀可用**：Firefox 尚未实现，退化路径是 `border-radius`
   （形状对、曲率不连续）。要全引擎一致，只能等 `corner-shape: squircle` 铺开。
2. **首屏骨架仍是普通圆角**：骨架内联在 `index.html` 里，若要把 900 B 的 `--sq9` 也塞进关键路径，
   收益（~200 ms 的视觉一致）不抵成本，因此保留 `border-radius: 16px`。
3. **Web 端「离线」是有条件的**：DEK 在内存里，关标签页即失效；离线只能读到「本次会话内已解密过的章节」，
   重新打开需要联网换 DEK 后才能解已缓存的密文。
4. **轮询有延迟**：前台 5 分钟一次，切前后台立即触发。两台设备同时阅读时，最坏 5 分钟才看到对方的最新位置；
   要更实时就得回到 WebSocket / SSE，与本阶段「去掉长连接」的目标冲突。
5. **只能前进，不能回退**：位置优先合并意味着「主动跳回前面的章节」不会同步出去。这是刻意的取舍
   （否则任何一次误触都会污染其它设备），需要回退时请在目标设备上手动跳章后再等一次同步。
6. **grant 单次**：同一个免密链接在两个标签页同时打开，只有一个能登录，另一个显示「票据已失效」。
7. **IndexedDB 受浏览器配额限制**：长书全量缓存可能触发配额（通常为磁盘可用空间的百分比），
   超出后写入失败会静默跳过，只影响离线范围。
8. **移动端 Admin 顶部导航横向滚动**：条目多时需要左右滑动，未做折叠菜单。

## 23. 第十三阶段：新手引导与响应式收口

前十二阶段解决的是「能不能用」，这一阶段解决的是「第一次打开会不会用、在电脑上看着像不像话」。
需求给了五条硬性要求和四个引导模块（G1~G4），除此之外顺手修掉了两个会直接毁掉首访体验的
缺陷——它们不是新功能，而是被这次的真实浏览器跑测抓出来的。

### 23.1 五条硬性要求

| 要求 | 落地方式 | 验收口径 |
|---|---|---|
| 响应式 | `<768px` 单列；`≥1024px` 236px 白色侧栏 + 主区多列（书架两列网格）；复杂表格用 `@media` 降级为卡片 | 390 / 834 / 1440 三档 `scrollWidth - clientWidth == 0` |
| 危险操作隔离 | 删掉用户端底部「清空本地缓存」大红按钮，改成「我的」里的普通文字行，点击弹 iOS 底部确认菜单 | 设置页 DOM 里不存在 `button.danger` 大按钮；确认菜单需二次点击 |
| 色彩 | 侧栏 `#FFFFFF`、主区 `#F2F2F7`、卡片 `#FFFFFF`；不做毛玻璃 | 取样像素与令牌一致 |
| 严格 Squircle | Web 用九宫格 `mask-box-image`（`--sq9`），Android 用 `RoundedPolygon(rounding, smoothing=0.6f)`；卡片/按钮/输入框一律不走 `border-radius` 单独成形 | 4× 放大截角，直角边长度与 12px/16px 档位吻合 |
| 状态标签 | 低饱和底 + 高饱和字 + `border-radius: 999px` 胶囊 | ok / warn / bad / muted 四档色值成对出现 |

**为什么 pill 反而不用遮罩**：胶囊的两端是半圆，`r = h/2` 本身就是曲率连续的上限；
套九宫格遮罩会把半圆拉成超椭圆，看起来「不圆」。所以 `.pill/.tag` 明确写了
`-webkit-mask-box-image: none`，这是全站唯一豁免 G3 的形状。

### 23.2 G1 首启欢迎页

三页横滑（`scroll-snap-type: x mandatory`），每页「细线条插画 + 编号 + 大标题 + 两行说明」：

- 01 数据绝对私密：信封加密，密钥只在设备上解开；
- 02 沉浸阅读：滑动翻页与听书；
- 03 多端同步：按「最远位置」合并。

底部固定主按钮，末页文案变「开始使用」；右上角常驻「跳过」。完成后写标记：
Web `localStorage['novelstack.onboarded.v1'] = '1'`，Android `DataStore` 布尔位。
桌面端补了 ←/→/Esc 键，并把脚部按钮收进 `max-width: 420px`——否则 1440 宽下按钮会拉成一条横幅。

插画是手写 SVG / Compose `Canvas`，只有 `stroke`，没有 fill 和阴影：抄设计图里的「极简细线条」，
也避免为三张图引入位图资源与多倍图。

### 23.3 G2 蒙层高亮

一整层 `rgba(0,0,0,.6)`，用 `clip-path: path()` 挖洞。关键在填充规则：

```
path("M0 0 H{vw} V{vh} H0 Z  <内圈逆时针圆角矩形>")
```

外框顺时针、内圈逆时针，nonzero 规则下两块相互抵消，洞是真的透明（能直接看到并点到底下的组件），
而不是「再盖一块黑的」。`polygon()` 做不到这一点，因为它不闭合内圈。

气泡是白色 G3 卡片 + 独立小三角（放在气泡**外面**做兄弟节点，否则 `top/left` 相对气泡计算）；
下方放不下时自动翻到目标上方，左右按视口 `clamp`。书架三步（搜索 → 书卡 → 发现页），
发现页一步；点「跳过」或空白处走 Spring 淡出，并记 `novelstack.coach.v1`。

### 23.4 G3 空状态

书架空 → 双层书架线稿 +「书架空空如也」+ 蓝字按钮跳发现页；足迹空 → 表盘线稿 +「还没有阅读记录」
+ 跳书架。`.empty-state` 用 `min-height: 56vh` 落在视觉中心，而不是贴着顶部留一大片空白。
搜不到结果复用同一个组件换个说法（「没有匹配『xx』的书」+ 清空搜索）。

### 23.5 G4 使用指南

设置 → 使用指南，四条 FAQ：TXT 导入与编码嗅探、音色与语速、局域网/配对码、多端进度合并逻辑。
折叠面板用 `grid-template-rows: 0fr → 1fr` 做高度动画（不用 `max-height` 猜数值，也没有
`height: auto` 不可动画的问题），同一时刻只展开一条。桌面端整块限宽 720px，
否则一行 40 多个汉字，回扫距离太长。

### 23.6 跑测抓到的三个缺陷

1. **刷新即白屏（严重）**：`ensureDeviceKeys()` 恢复会话密钥时以 `extractable=false` 导入私钥，
   随后 `exportKey('spki', publicKey)` 抛 `key is not extractable`，`boot()` 失败 → 页面只剩一条红字。
   修复：公钥单独以 JWK 存 sessionStorage、以可导出方式重新导入；私钥仍不可导出。
   顺手删掉「从私钥反推公钥」的死代码——它只能在可导出私钥时工作，是个陷阱。
2. **欢迎页背后藏着蒙层**：`boot()` 把 `loading` 置回 false 时 watcher 先跑了一轮，
   此时还没判断要不要弹欢迎页，于是 `coachOpen` 先变 true。修复：加 `booted` 闸门，
   并在 `openCoach()` 里再挡一次 `showOnboard`。
3. **桌面确认菜单贴在屏幕最底**：底部贴边是移动端手势语义，鼠标场景下像「漏了一截」。
   `@media (min-width:768px)` 改为居中浮层（360px 宽 + scale 入场）。

### 23.7 验收

```bash
go build ./... && go test ./...        # 全绿
cd web && pnpm build                   # 产物写入 internal/adminui/dist
cd android && ./gradlew :shared:testDebugUnitTest :androidApp:assembleDebug   # BUILD SUCCESSFUL
```

浏览器侧用 Playwright 打真实构建产物（`/api/web/library` 打桩返回三本书），逐屏截图核对：

| 场景 | 结论 |
|---|---|
| 390 首访 | 欢迎页三屏 → 书架蒙层 3 步 → 书架，`localStorage` 两个键均写入 |
| 390 刷新 | 密钥从 sessionStorage 恢复，书架正常，无错误条（缺陷 1 的回归点） |
| 390 设置 | 「清空本地缓存」为普通行，点击弹底部菜单，取消可用 |
| 390 指南 | 四条 FAQ 正常展开/收起 |
| 390/834/1440 | `scrollWidth - clientWidth` 均为 `0` |
| 1440 书架 | 236px 白侧栏 + 两列书卡网格；欢迎页按钮收窄到 420px |

Android 侧仍只能编译 + 单测验证，动效（Pager 回弹、Blur 镂空的 Spring 淡出）需真机确认。

### 23.8 文件

**Web**

- `src/user/guide.js`（新）：引导状态读写
- `src/components/Onboarding.vue`、`CoachMarks.vue`、`ActionSheet.vue`、`EmptyState.vue`（新）
- `src/views/user/GuideView.vue`（新）、`UserSettingsView.vue`（重写）、`ShelfView.vue`（重写）、
  `UserShell.vue`（重写引导编排）、`HistoryView.vue` / `DiscoverView.vue`（换空态）
- `src/crypto/keys.js`：公钥 JWK 随私钥一起存，修「刷新即白屏」
- `src/styles.css`：G3 基元扩到 `.sheet-*`，新增欢迎页 / 蒙层 / 空态 / FAQ / 侧栏 / 网格段落

**Android**

- `shared/.../data/AppPreferences.kt`（新）：DataStore 读写 `onboarded`
- `ui/OnboardingScreen.kt`、`ui/GuideScreen.kt`、`ui/components/CoachMarks.kt`、
  `ui/components/EmptyState.kt`、`ui/components/IosActionSheet.kt`（新）
- `ui/components/AppleUI.kt`：`ApplePrimaryButton` + `ApplePill`（四档色调）
- `app/AppState.kt`、`ReaderApplication.kt`、`ui/App.kt`、`SettingsScreen.kt`、`LibraryScreen.kt`、`HistoryScreen.kt`

**客户端分发**：`GET /download/novelstack.apk` 是唯一不鉴权的固定下载口，路径只从
`APP_APK_FILE` 读（不接受请求参数，避免任意文件读取），支持 Range 供 Android 更新器测速；
配合 `APP_MIRRORS` + `APP_APK_SHA256`，客户端先测速选镜像、下载后按 SHA-256 校验再安装。

### 23.9 已知限制

1. **Android 动效未真机验收**：沙箱里没有模拟器，`HorizontalPager`、`BlendMode.Clear` 镂空、
   Spring 淡出只验证到「编译通过 + 单测通过」。
2. **蒙层只认 CSS 选择器**：目标是 `document.querySelector('.search')` 这类硬编码选择器，
   改布局时要同步改 `STEPS`；找不到元素时该步自动跳过（不会指空气），但也不会报错提示。
3. **引导状态按浏览器计**：换浏览器/换设备会再看一遍，这是刻意选择（属于 UX 偏好，不做服务端同步）；
   「清空本地缓存」不会清掉它，避免用户刚清完缓存又被引导糊一脸。
4. **两列书卡在 1024~1280 偏挤**：封面 44px + 两行文字在 460px 卡宽里刚好，再窄会截断书名，
   所以断点定在 1024 而不是 834。

## 24. 管理端书籍上传入口

前十三阶段传书只能走 CLI（`novelstack import --dir`）或直接 `curl -F`，管理端 `/admin` 是刻意做的
只读控制台。公网预览暴露出一个真实问题：拿到链接的人没有沙箱 shell，看到空书库却无从下手。
这一节加一个网页上传入口，**只加 UI，不动服务端的安全边界**。

### 24.1 落在哪里

管理端新增「书库 → 书籍管理」页：顶部「上传书籍」蓝按钮 + 书籍列表；点按钮弹窗，支持
拖拽或点选 `.txt`，可填书名/作者/标签，书名留空时自动取去扩展名的文件名。列表展示章节数、
大小、索引状态与导入时间，`<768px` 复用 `data-label` 降级为垂直卡片。

### 24.2 三条不能破的约束

1. **字段顺序**：服务端 `Upload()` 用 `MultipartReader` 流式消费，file part 必须在遍历到的当下
   加密，因此要求 `title` 先于 `file`。前端 `FormData.append` 严格按
   `title → author → tags → description → file` 顺序；顺序错服务端直接 400。
2. **格式只认 .txt**：`encryptPart()` 用 `filepath.Ext(name) != ".txt"` 挡掉其它类型（415）。
   服务端刻意不解析 EPUB（避免引入转换依赖与解析攻击面），前端因此也只放行 `.txt`，
   选到其它格式给 Toast 提示——**没有**为了「支持 EPUB」去放宽服务端边界。
3. **加密仍在服务端**：明文 → DEK 分块加密走 `io.TeeReader(p, scanner)` +
   `encryptCounting(..., f, dek, id, chunkSize)`，明文只过管道不落盘，密文直接写随机文件名，
   失败清理半成品。前端用 XHR 只把 `File` 原样投递，不读正文、不做客户端加密。

### 24.3 进度与反馈

- 用 `XMLHttpRequest.upload.onprogress` 驱动进度条（`fetch` 没有上传进度事件），
  百分比与文件名同屏显示；进度条走 `width .18s cubic-bezier` 平滑过渡，不用转圈。
- 前端拦截 `>50MB`（Toast 深色胶囊提示），与发布配置 `MAX_UPLOAD_MB=50` 对齐——
  否则会出现「前端放行、服务端 413」的不一致。服务端本就按 1MiB 分块，提到 50MB 不会 OOM。
- 成功：进度条转绿 + 对勾 + 「上传成功，已加入书库」，并自动刷新列表；
  失败：红色 iOS 风格错误条带服务端原因，按钮变「重试」。

### 24.4 验收

`pnpm build` 后对真实构建产物打桩 API，Playwright 逐场景截图核对：

| 场景 | 结论 |
|---|---|
| 列表 / 弹窗 / 拖拽区 | 正常，侧栏「书籍管理」高亮 |
| 60MB 文件 | Toast 拦截，文件未被接受，按钮保持禁用 |
| .txt 上传成功 | 书名预填，进度条转绿 + 对勾，按钮变「完成」 |
| 服务端 413 | 红色错误条显示 `upload too large`，按钮变「重试」 |
| 390px 移动端 | 表格降级为卡片，弹窗完整可见，`scrollWidth-clientWidth == 0` |

Go 全量测试保持全绿（本轮未改服务端逻辑，仅前端 + 配置）。
