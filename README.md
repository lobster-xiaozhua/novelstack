# novelstack —— 私人小说阅读站（第一 ~ 六阶段）

单用户、默认对外屏蔽的私人站点。

| 阶段 | 能力 |
|---|---|
| 一 | 认证（Argon2id + session）→ 信封加密落盘（AES-256-GCM）→ 解密读取 → 审计 |
| 二 | 章节切分 → 句子级分片 → 外部 TTS 合成 → 片段级加密音频缓存 → 流式朗读 |
| 三 | **分层搜索**（服务端只索引元数据 / 正文全文搜索在客户端本地）+ **Legado 书源网关** + ETag 章节同步 + 覆盖度指标 |
| 四 | **加密备份包 + 一键恢复演练** · **KEK 轮换** · **只读运营控制台 `/admin`**（go:embed 内嵌 Vue）· **批量导入 + 缓存生命周期** |
| 五 | **移动端 App（KMP + Compose）**：设备令牌/配对码 · 增量同步 · 加密章节预取 · SQLCipher + FTS5 离线库 · 设备内解密朗读 · 一键清除 |
| 六 | **最小运维加固**：每日自动备份 + 保留策略 · 健康横幅 + 可选 webhook · KEK 离线备份提醒 · Android release 签名模板 |

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

> Gradle wrapper 的二进制 jar 在仓库中以 `android/gradle/wrapper/gradle-wrapper.jar.b64`
> 文本副本保存，CI 先用 `base64 -d` 还原再运行 `./gradlew`；版本仍是 8.11.1，未升级。
| `docker` | `docker build -t novelstack-ci .` | 不跑 compose up |

workflow 语法可用 PyYAML 解析校验；`GOFLAGS=-mod=readonly` 在 go job 的 env 中显式设置。

### 16.5 Docker 构建卫生（P5）

`.dockerignore` 排除 `.git`、`.github`、根 `data/`、`secrets/`、`backups/`、`imports/`、
根 `dist/`、`android/`、`demo-out/`、`*.nsb`、`*.md`（`!README.md` 例外）等；
根锚定保证 `internal/adminui/dist` 仍能进入构建上下文。
Dockerfile 的运行时镜像为 `gcr.io/distroless/static-debian12:nonroot`（可用
`RUNTIME_IMAGE` build-arg 覆盖），入口点 `/app/novelstack`、非 root 用户与 secret 挂载方式均未改动。
