# RelayChat 开发细节

面向接手/继续开发的工程师。记录**代码里看不出来的东西**：为什么这么设计、踩过什么坑、哪些约束不能破。

部署流程见 `deploy-khmer-ai-cs/`，不在这里重复。

---

## 一、先搞清几件容易被误导的事

### 1. 这个项目没有真正的多租户

`tenant_id` 在 Go 代码里出现 **0 次**。

**一个「租户」= 一行 `users`**。证据链：

- 迁移 021 的注释原文：「tenant-scoped settings … become per-user (**user_id**)」
- `tenant_overview` 是个对 `users` 做 rollup 的**视图**，不是表
- `listTenants` 的真实 SQL：`SELECT ... FROM tenant_overview WHERE role <> 'platform_admin'`（`tenant_overview` 是 027 建的 users+billing rollup 视图，语义上就是 users 表）
- `tenantDetail(w, r, userID int32)` —— 租户 ID 就是用户 ID

所以 platform-admin 那个「租户管理」页面，本质是**用户管理 + 计费**。

子账号靠 `agent_teams(owner_user_id, agent_user_id)`，**一对一独占**：一个用户只能属于一个 owner 的团队。这条约束是 `addTeamAgent` 强制的，因为 `agent_teams` 同时充当了所有「外来 user_id」handler 的租户边界（见 `userInCallerTenant`）—— 如果允许一个用户被两个商家认领，A 商家就能借这条链管理 B 商家的账号。

### 2. Row-Level Security：061 起有兜底，但尚未上膛

057 及之前 `CREATE POLICY` 出现 **0 次**。隔离完全靠应用层手写 `WHERE user_id = $N`。

**这意味着任何一处漏写 WHERE 就是跨租户泄漏。**

`chat_messages` 和 `knowledge_chunks` 连 owner 列都没有，要靠 join `sessions` / `knowledge_documents` 绕上去 —— 漏写的风险面比表面上更大。

061 给这两张表加了 RLS 兜底策略（`app.user_id` GUC 门控、GUC 未设时 fail-open），
语义、失效条件与后续收严路径见「十」。**在应用侧还没有任何代码设置该 GUC 之前，
它不改变任何运行行为——不要把「有 RLS」误读成「有强制隔离」**，应用层纪律照旧。

### 3. 技术栈的三个常见误记

| 记成 | 实际 |
|---|---|
| Gin | **stdlib `net/http` ServeMux**。`GIN_MODE` 只是残留的 config 字段 |
| sqlx | **pgx/v5** |
| Radix UI | **Base UI**（`@base-ui/react`）+ shadcn `base-nova` |

### 4. 迁移是前向单行道

`schema_migrations(version)` **只记版本号，无校验和、无 down**。

- 已应用的迁移文件**永远不要改内容** —— 改了不会报错，但新库和老库 schema 会分叉
- 向后兼容是硬约束：只能加表/加列，少删

---

## 二、本轮实现（2026-09-12）

### A. 安全与稳定性修复（10 项）

| # | 问题 | 修复 |
|---|---|---|
| 1 | **限流器永久封禁** | `redisstore/redis.go` 每次检查无条件 `Expire` 重置 TTL → 活跃用户窗口永不关闭，越过上限后永久 429。改为 Lua 脚本原子「自增 + 仅在键无 TTL 时补」。配 4 个回归测试（已用「改回旧逻辑」验证过测试确实会挂） |
| 2 | **跨租户越权** | `listUsers` 全局无过滤；`updateUserRole` 可改任意非 platform_admin 用户；`assignSession` 可指派给外租户；`addTeamAgent` 可认领他人用户；`assignRole`/`unassignRole` 无归属校验。全部加租户边界 |
| 3 | **LINE/Zalo 无租户路由** | 解析器取「第一个 active 配置」，第二个商家永远收不到消息。改为按 `channel_identity` 路由 + 自动学习（详见迁移 051） |
| 4 | **静默 mock 降级** | Gemini 任何失败都返回模板回复且 `err=nil`，管道照常投递给真客户。改为「未配置」与「配置了但失败」区分，后者返回错误触发有界重试 |
| 5 | **`EnsureReplyWindow` 丢消息** | 把数据库查询错误当成「无入站上下文」→ 返回终态 `PolicyError` 不重试。改为区分 `pgx.ErrNoRows` 与瞬时错误 |
| 6 | **出站乱序** | `claimOutbound` 缺「同会话存在更早 pending」守卫（入站一直有）。补上 |
| 7 | **通知被静默丢弃** | 计费累加与转人工通知和 LLM 分类共用 8 槽信号量，尖峰时被丢。拆成 `criticalSem`(64, 不丢) 与 `classifySem`(8, 可丢) |
| 8 | **知识编译丢数据** | 先删旧的编译子文档、再调 LLM，失败即丢失旧摘要。改为成功后在单事务里换；并挪出索引 worker（原来会阻塞整个索引队列最长 60 秒） |
| 9 | **成本统计盲区** | 只有 4 处调用 `usage.Record`。加 `gemini.AuxUsageObserver` 钩子覆盖编译/翻译/重排/改写/转写/图片描述；上下文用 `usage.WithUser` 携带租户 |
| 10 | **认证加固** | TOTP 密钥加密存储；`token_version` 会话吊销；重新注册 2FA 需密码；API key 默认一年过期 |

### B. RelayChat 改名

原名「Khmer AI 客服系统」。改名涉及 18 个文件，其中**两处做了平滑过渡**：

**JWT issuer**（`auth.go`）—— 直接改会让所有在线用户当场登出：
```go
JWTIssuer       = "relaychat"      // 新 token
legacyJWTIssuer = "khmer-ai-cs"    // 旧 token 仍接受
```
配了 `TestTokenIssuer` 三个断言。**旧 issuer 的接受逻辑在 24 小时后（一个 JWT 生命周期）可删**。

**WebSocket 子协议**（`realtime.go` + `lib/realtime.ts`）—— 前后端各写一份，不同步改实时功能就断。服务端 `Subprotocols` 列表和 token 提取都认两个值，缓存旧 JS 的浏览器也能正常升级。

**刻意没改的**：Go module 名（74 处 import 语句 / 14 个包路径，零用户收益）、`R2_BUCKET` 默认值（真基础设施名）、`InsecureDefaultJwtSecret`（安全绊线常量，改了就检测不到那个弱密钥）。

### C. Telegram 登录（OIDC）

新增 `internal/api/telegram_sso.go`，复刻 `google_sso.go` 的三段式，**多一层 PKCE (S256)**。

**与 Google 的关键差异**：Telegram 不给邮箱。所以：
- 身份只认 `sub`（数字 id），**绝不认 `@username`** —— 后者可改、可回收，拿它做键就是「改个名字登进别人账号」
- 用户名合成：优先 `@username`，没有或已被占用则退到 `tg_<sub>`
- `email` 放开为可空（迁移 053），**不合成假邮箱**污染用户列表和 CSV 导出

**顺手修的一个坑**：`email` 改可空会让所有 `Scan(&email)` 到非指针 `string` 的地方在遇到 NULL 时炸掉（Postgres 的 NULL 扫进 Go 的 `string` 是运行时错误，不是零值）。排查出 5 处并加了 `COALESCE(email,'')`。其中 `profile_handlers.go` 最阴——它对任何错误都返回 `ErrNotFound("用户不存在")`，Telegram 用户打开个人资料页会看到莫名其妙的报错。

### D. 平台级 Telegram bot

**这是本轮最大的一块**。一个 operator 自有的 bot，与所有租户级 bot 区分开：

| Bot | 归属 | 用途 |
|---|---|---|
| `platform_configs.bot_token` | 商家 | 客服渠道（收发客户消息，**保留**） |
| `telegram_notify_settings.bot_token_enc` | — | **056 起废弃**，恒为 NULL，列保留只为二进制回滚不炸 |
| **`PLATFORM_TELEGRAM_BOT_TOKEN`** | **运营者** | **通知 / 登录 / 告警 / 命令台 / 支持 / 广播** |

**五个功能**：

1. **运维告警** —— 60 秒看门狗（DB/Redis）+ Gemini 配额耗尽。15 分钟去重（同一个 `key` 只报一次），恢复时清除告警键以便再次触发。

2. **商家账号绑定** —— 一键 deep link 取代「去 BotFather 建 bot、复制 token、粘贴、发消息、等轮询发现 chat_id」五步。`bot_token_enc` 为 NULL 表示「走平台 bot」，非 NULL 保持旧的每商家路径。

   ⚠️ **这条双路径已在 056 废除**，见下面 G 节。留下这段是为了说明当时为什么两套并存 —— 以及为什么那是错的。

3. **管理员命令台** —— `/status` `/tenants` `/tenant <id>` `/digest` `/broadcast` `/help`。**两道独立的门**：
   - Telegram user id 白名单（Telegram 自己断言 `from.id`）
   - 若该身份已绑定本地账号，该账号必须仍是启用的 `platform_admin` —— 这道门让「降权」真正生效，不用改白名单重新部署

   跨租户读取全部写 `audit_logs`。

4. **支持收件箱** —— 商家给 bot 发消息落 `platform_support_messages`。未知用户的消息也保留（可能是潜在客户）。

5. **双向中继** —— 商家消息推给运营者，**运营者直接长按那条推送回复**，bot 通过 `reply_to_message.message_id` 查 `platform_support_relay` 映射回给对应商家。响应 Telegram 的原生交互，不需要记 chat id 或输命令。

### E. 一个既有的生产 bug

排查中发现 `/admin/analytics/languages` 和 `/top-queries` 在生产上**每次调用都 500**（首次出现早于本轮任何改动）。

当时的判断是 `($2 || ' days')::interval` 有问题，全部 8 处改为 `make_interval(days => $2)`。

> **⚠️ 事后更正（2026-09-12，在生产库上实测）：当时的根因分析是错的。**
>
> 原判断称「Postgres 没有 `||(integer, text)` 操作符，绑定参数会在 prepare 阶段失败」。实测不成立：
>
> ```sql
> PREPARE b(int)    AS SELECT NOW() - ($1 || ' days')::interval;  -- PREPARE（通过）
> PREPARE c(bigint) AS SELECT NOW() - ($1 || ' days')::interval;  -- PREPARE（通过）
> PREPARE f         AS SELECT NOW() - ($1 || ' days')::interval;  -- PREPARE（通过）
> EXECUTE d(30);                                                  -- 2026-08-13（正确）
> ```
>
> int4、int8、以及不声明类型三种绑定方式**全部可用**。Postgres 从 8.3 起就有
> `anynonarray || text` / `text || anynonarray` 两个操作符，存在的意义正是让
> `整数 || 文本` 这种写法继续可用。
>
> **更正（2026-09-23）：上述「残留 3 处不是 bug」的结论对其中两处是错的。**
>
> `internal/api/tasks.go` 里 `scanSLABreaches` 的两条语句（first_response / resolution）
> **在运行时必然失败**，只是错误被 `if err == nil` 吞掉、不报 500 而已：
>
> ```
> failed to encode args[1]: unable to encode 300 into text format for text (OID 25)
> ```
>
> 机制：`($2 || ' seconds')` 里 Postgres 把参数解析为 **text**，而 pgx 扩展协议无法把
> Go 的 `int` 编码成 text 参数 —— **语句在发送阶段就挂了，从未执行**。所以 SLA 违约
> 扫描自上线起就是空转：不会写 `sla_breaches`，也不会发通知（策略数长期为 0 另有原因，
> 见 030 迁移后的创建接口 bug）。实测换成 `make_interval(secs => $2)` 后同一查询
> 正常返回 24 行；回归测试 `internal/api/sla_scan_test.go` 用「改回旧写法」验证过会挂。
>
> **`psql` 的 `PREPARE` 通过、`internal/sqlcheck` 通过，都不能证明这条语句能跑** ——
> 两者都不做 pgx 那一步参数编码。这正是本节教训的延伸：手测/静态校验骗过你的方式
> 不止一种。`internal/rag/service.go:1032`（`$2` 是 `FormatInt` 后的字符串，
> text||text）不受影响，那一条仍然不是 bug。

教训：**手测 SQL 用字面量会骗你，读到一条看似合理的根因也会。** 任何关于
「这个写法在 Postgres 上行不行」的结论，都该在真库上 prepare 一次再说。

### F. 其他

- **挂件高棉语文案** —— `frontend/src/app/widget/page.tsx` 的 `km` 语言槽曾是中文占位（与 `zh` 相同），已在 8d150e3 换成高棉语（当前为 `ជំនួយការអតិថិជន` 等）。这是流量最高的终端客户界面，文案仍需母语者过一遍
- **`api.ts` 优先级 bug（已修复）** —— 原写法 `a || b + \`...\`` 里 `+` 优先级高于 `||`，`text` 非空时 HTTP 状态码被吞掉，502 网关页原样进 toast；现在显式拼接，代码内有注释（`frontend/src/lib/api.ts:34-40`），修复同样在 8d150e3
- **路由命名空间** —— `/admin/business-hours` 等三个路由是租户自助设置（数据按 `user_id` 自作用域），挂在 `/admin/` 下误导人，且审计中间件会记录每个商家的日程编辑。移到 `/settings/`
- **命名空间清理** —— 清理了改名后残留的一批模式串（`/ready` 响应、TOTP `otpauth://` issuer、Telegram 测试消息等）。仍出现的 `khmer-ai-cs` 字样均为刻意保留：module 名、`R2_BUCKET` 默认值、legacy JWT issuer / WebSocket 子协议、`InsecureDefaultJwtSecret`

### G. Telegram 通知统一到平台 bot（056）

054 引入了平台 bot 一键绑定，但**刻意保留了每商家自带 bot 的旧路径**。两套并存意味着每个商家侧功能都要实现两遍 —— 而第二遍从来没做：内联的 接管/解决 按钮只接在每商家的 `getUpdates` 轮询上，走平台 bot 绑定的商家**按下按钮什么都不会发生**。

现在只剩一条路。

**代码侧的改动**

- **`telegram_notify_settings.bot_token_enc` 不再被读取**。`loadTelegramNotifyUncached` 直接用平台 bot token；平台 bot 没配就没有通知，没有回退
- **`ProcessNotifyCallbacks` 与 `tasks.go` 的 12 秒轮询整体删除**。它遍历 `telegram_notify_settings` 全表，对平台 bot 商家会拿平台 bot token 去 `getUpdates` —— 而平台 bot 注册了 webhook，Telegram 直接拒绝。迁移 054 的注释声称这次改动「移除了每租户轮询」，实际上循环还在，只是对不该跑的那批也在跑
- **按钮回调接进平台 bot webhook**（`handlePlatformCallback`）。顺带发现 webhook 里 `if upd.Message == nil { return }` 会把**所有** callback_query 静默丢弃 —— 这是按钮失效的真正原因
- **`/unlink`** —— 绑定此前是单向门，绑错号/换号/号被盗都无解。带内联确认按钮，因为误输命令就静默切断自己的通知是很糟的发现方式
- **机器人文案本地化**（`bot_texts.go`）—— 此前硬编码中文，而产品是高棉语优先。语言解析顺序：`users.language` 偏好 → Telegram 客户端的 `language_code` → 英语。**运营者命令台保持中文**，那个受众是 RelayChat 团队
- **API 面收窄** —— `PUT /settings/telegram-notify` 只接受三个开关；`/settings/telegram-notify/updates`（拉取会话列表）整个删除；不再有接受 bot token 或任意 chat_id 的端点

**存量商家必须重新绑定一次**，这是数据的性质决定的，不是实现选择：`chat_id` 是**和某个特定 bot 的会话**，只有那个 bot 能寻址。平台 bot 往商家与自己 bot 的 chat_id 发消息会 403（`bot can't initiate conversation with a user`）。没有可迁移的东西。

所以 056 把 `bot_token_enc`、`chat_id` 和 `chat_title` 一起**清空**（前两列还写入了 `COMMENT`），而不是留着。留一个失效的 chat_id 比没有更糟 —— 设置页会显示「已连接」而投递一直静默失败。三个 `notify_*` 是偏好不是凭据，保留。

`bot_token_enc` **列本身不删**（可空，恒 NULL）：部署手册把「二进制回滚」当作受支持的操作，删列会让回滚到上一版时直接报列不存在，而不是优雅降级成「未配置」。

**Khmer 文案是初稿，发布前需要母语者过一遍** —— 写的是「能被看懂」而不是「地道」，而这是商家在手机上读的消息。

### H. SQL 对象引用审计 —— 查出 SLA 创建 100% 失败

`/tenants` 的列名错误暴露出一类**编译器完全看不见**的问题：SQL 字符串里引用的表 / 列 / 类型不存在。构造、vet、单测都不会报，只有真跑到那条查询才炸；如果调用方用 `_ =` 吞掉 error，还会表现为**静默的 0**。

于是写了个一次性工具（`sqlprobe`）做穷举：

1. 用 `go/ast` 解析全部 Go 源码，把字符串字面量按 `+` 拼接**还原成完整 SQL**
2. 逐条拿到**生产库**上 `PREPARE` —— 这正是当初暴露 `/tenants` 的机制
3. 另做一次「标识符差集」：SQL 里出现过的所有标识符 vs 生产库的表名/列名/类型名

结果（574 条候选；一次性审计，需生产库连接才能复现）：

| 项 | 数 |
|---|---|
| 完整语句，prepare 通过 | 448 |
| prepare 失败 | 46 |
| 跳过（拼接残段 / format 串） | 80 |

46 条失败里，44 条是提取器误报（`VALUES (...)` 是 INSERT 被变量切断的尾巴、`DELETE /api/v1/...` 是 HTTP 路由不是 SQL），1 条提取不完整，**1 条是真的**：

> **`upsertSLA`（路由 `POST /api/v1/sla`）里的 `$6::sla_priority` —— 这个类型在全部迁移（截至 057）里从未出现过。**
>
> `sla_policies.priority` 实际是 `varchar DEFAULT 'normal'`，不是 enum。语句在 prepare 阶段就失败，
> 所以 **`POST /api/v1/sla` 从上线起 100% 返回「创建失败」**，一条策略都建不出来
> （生产库 `SELECT COUNT(*) FROM sla_policies` = 0，与此一致）。
>
> 修复：`$6::sla_priority` → `$6::varchar`。已在生产库上实测：prepare 通过，
> 实插一条返回真实 `sla_id`，回滚干净。

**顺带确认**：全仓所有 `::type` 强转里只有 `sla_priority` 指向不存在的类型；其余 4 个自定义类型（`platform_type`、`human_handoff_trigger`、`session_status`、`user_role`）生产库全部存在。

差集那一步另外列出 83 个「库里没有的标识符」，逐个核对后**全部是**单字母表别名、`AS` 输出别名、CTE 名、HTTP 路由片段、Go 变量和环境变量名 —— 无新增问题。

**这一步已经常驻成代码**：`backend-go/internal/sqlcheck/`。它做同样的事，且修掉了首版手工脚本的误报来源 —— 关键是**属于 `+` 拼接链的字面量只作为整条语句上报一次，绝不单独上报**。首版把每一段都单独上报，于是 `INSERT` 的尾巴 `VALUES (...) ON CONFLICT ...` 看起来像独立语句，但没有任何表可解析，prepare 只会报「列不存在」，与源码毫无关系。46 条失败里 44 条是这么来的。

常驻版本的实测结果（一次性，对生产库；没有生产库连接则 skip，数字不可本地复现）：

```
checked 454 statements: 441 prepared, 11 skipped, 0 broken, 2 unparseable
```

2 条无法解析的是**真的残段**（被变量截断），作为提示打印、不判失败。**语法错和对象不存在被区别对待**：只有后者说明源码有问题，前者只说明提取器把残段当成了完整语句 —— 如果不区分，测试会因为噪声被忽略，那比没有测试更糟。

用法见「七、快速排查入口」。

---

## 三、迁移 050–056

| 版本 | 内容 | 注意 |
|---|---|---|
| 050 | 删除冗余 ivfflat 向量索引 | 001 建的，032 上 HNSW 后从未删 → 每个 chunk 写入维护两个索引。**普通 `DROP INDEX`（runner 在事务里跑，`CONCURRENTLY` 不可用），取 ACCESS EXCLUSIVE 锁** |
| 051 | `platform_configs.channel_identity` | LINE/Zalo 的 webhook 路由键。**自动学习**：连接时从 provider API 拉取；迁移前已连接的渠道在第一条**验签通过**的 webhook 上绑定（先验签再绑定——否则攻击者能用伪造的 destination 提前污染） |
| 052 | `users.token_version` + `user_totp.secret` 加宽 | 会话吊销机制。挂在鉴权中间件**已有的那次查询**上，零额外开销 |
| 053 | `users.email` 可空 + `telegram_sub` | Telegram 不给邮箱 |
| 054 | `telegram_notify_settings.bot_token_enc` 可空 + `linked_at` + `notify_announcements`；建 `platform_support_messages` | NULL token = 走平台 bot。**056 已废除这条双路径** |
| 055 | `platform_support_relay` | 中继映射。按 `(admin_chat_id, admin_message_id)` 唯一 —— Telegram 的 message id 只在会话内唯一 |
| 056 | 清空 `bot_token_enc` + `chat_id` + `chat_title`（前两列加注释） | **破坏性**：所有存量商家需重新绑定，见 G 节。列不删以保回滚 |

---

## 四、新增环境变量

### Telegram 登录

```bash
TELEGRAM_LOGIN_ENABLED=true
TELEGRAM_LOGIN_CLIENT_ID=          # BotFather → Login Widget / Web Login
TELEGRAM_LOGIN_CLIENT_SECRET=      # ⚠️ 不是 bot token，是单独发的 secret
TELEGRAM_LOGIN_REDIRECT_URL=https://<域名>/api/v1/auth/telegram/callback
TELEGRAM_LOGIN_FRONTEND_URL=https://<域名>/login
TELEGRAM_LOGIN_ALLOW_SIGNUP=true
TELEGRAM_LOGIN_REQUEST_PHONE=true  # 申请 phone scope，拿 Telegram 已验证手机号
```

### 平台 bot

```bash
PLATFORM_TELEGRAM_BOT_TOKEN=       # 平台凭据，按 JWT secret 级别对待
PLATFORM_TELEGRAM_WEBHOOK_SECRET=  # 随机串，Telegram 回传用于验签
PLATFORM_TELEGRAM_ADMINS=          # 逗号分隔的 Telegram 数字 user id
PLATFORM_TELEGRAM_ALERT_CHAT=      # 告警投递目标，缺省 = 第一个 admin id
PLATFORM_TELEGRAM_BOT_USERNAME=    # @handle（不带 @），用于生成绑定 deep link
```

**BotFather 配置要点**：
- 用 **mini app**（不是聊天窗口）→ 选中 bot → `Login Widget` → **Switch to OpenID Connect Login**
- **Redirect URIs 必须精确匹配**，Telegram 只认注册过的地址
- Client Secret 与 bot token 是**两个不同的东西**

**Webhook 注册**（部署后执行一次）：
```bash
curl -X POST "https://api.telegram.org/bot${PLATFORM_TELEGRAM_BOT_TOKEN}/setWebhook" \
  --data-urlencode "url=https://<域名>/api/v1/webhook/telegram-platform" \
  --data-urlencode "secret_token=${PLATFORM_TELEGRAM_WEBHOOK_SECRET}" \
  --data-urlencode 'allowed_updates=["message","callback_query"]'
```

> ⚠️ **`callback_query` 不能漏。** `allowed_updates` 是白名单，Telegram 只投递列出的类型。
> 只写 `["message"]` 时，内联按钮（转人工的 接管/解决、/unlink 的确认）的按下事件**根本不会到达服务端**
> —— 而且失败是静默的：发送正常、按钮显示正常、按下去毫无反应，服务端连日志都没有。
> 2026-09-12 部署时线上正是漏的，已补。改完用 `getWebhookInfo` 回读确认。

---

## 五、给后来者的几条经验

**1. `ON CONFLICT DO UPDATE` 的陷阱**
`VALUES` 里的值**只在 INSERT 分支生效**。绑定功能第一版就栽在这：`bot_token_enc = NULL` 只写在 VALUES 里，冲突分支没清 —— 结果**已配过自己 bot 的商家（正是这功能最该服务的对象）绑定后毫无变化**，而全新商家是好的。所以它「测试通过、上线失效」。

写完 `ON CONFLICT` 一定要**两条分支都验**。

**2. 手测 SQL 用字面量会骗你**
参数化查询和字面量查询的**类型推断不一样**（见那个 analytics bug）。要验证参数化查询，就得用真实的绑定路径，或者直接看日志里生产环境的实际报错。

**3. 改可空列之前先搜扫描点**
`Scan(&x)` 到非指针 `string` 遇到 NULL 是**运行时错误**，而且经常被 `if err != nil { continue }` 吞掉，表现为「数据静默消失」。

**4. Tailwind v4 的任意值类不生成**
`text-[#2AABEE]` 这类**不会生成 CSS**（项目历史上栽过，commit `8c6ca15`）。品牌色要用注册好的 `@theme` 令牌（`text-brand-telegram` 等，见 `globals.css`）。

**5. 前端 AGENTS.md**
`frontend/AGENTS.md` 要求写前端代码前先读 `node_modules/next/dist/docs/`。Next 16 的破坏性变更集中在 turbopack 配置、`middleware`→`proxy`、`next lint` 迁移。

---

## 六、已知缺口（未实现）

> 本节是 2026-09-12 的原始记录，保留原样。**最新、最全的功能与缺口清单见「十一、功能清单与缺口（2026-10-04 核对）」。**

| 项 | 说明 |
|---|---|
| 前端 13 个零消费者导出 | `streamChat` / `streamWidgetChat` / `chatVoice` / `sendMessage` / `listSessions` 等，是现成的 API 客户端层，非缺陷 |
| `/widget` 自带 SSE 解析器 | 未复用 `streamWidgetChat`；重构客户-facing 聊天路径为零收益换风险 |
| 邮件渠道 | `config.go` 有 SMTP 字段，**零代码** |
| 域名 | 仍是 `cs.wanfanginsulationmaterial.com`（保温材料公司子域）。换域名要同步改：DNS、Cloudflare、nginx、BotFather Redirect URIs、Meta/Google OAuth 回调，**并且已嵌出的挂件会全部失效** |
| 平台 bot 单点 | token 泄漏影响所有商家（旧架构是每商家隔离）。这是换掉每商家自建 bot 的代价 |
| SQL 引用守卫需要 `DATABASE_URL` | `internal/sqlcheck` 已常驻（见 H 节与排查入口），**但只在设了 `DATABASE_URL` 时运行** —— 未设就 skip。常规 `go test ./...` 不会覆盖它，必须显式带着库跑一次，或接进部署流程。这是刻意的：检查只在「已应用迁移的库」上才有意义 |

---

## 七、快速排查入口

```bash
go test ./internal/migrations/     # 迁移对账 + 镜像同步
go test ./internal/redisstore/     # 限流器回归
go test -race ./...                # 竞态

# SQL 引用检查 —— 只在设了 DATABASE_URL 时运行,否则 skip。
# 它把源码里每条 SQL 拿去 PREPARE:只解析不执行,每条都在回滚的事务里,
# 因此可以直接指向生产库。发布前跑一次,能挡住「接口 100% 不可用」那类 bug。
cd backend-go
set -a; . ./.env-go; set +a          # 或 export DATABASE_URL=...
go test ./internal/sqlcheck/ -v

# 本机没有库时,用隧道把生产库映射过来(DSN 里的 5432 换成隧道端口):
#   ssh -f -N -L 15432:127.0.0.1:5432 root@<服务器>
#   DATABASE_URL=$(echo "$DATABASE_URL" | sed 's|:5432/|:15432/|') \
#     go test ./internal/sqlcheck/ -v

# 生产
curl -s http://127.0.0.1:8081/ready
journalctl -u khmer-ai-cs-go -f
psql "$DATABASE_URL" -c "SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 5"
```

**镜像目录**：`backend-go/migrations/` 是供 psql 手查的副本，真源是 `internal/migrations/migrations/`。`TestMirrorDirectoryIsInSync` 会逐字节比对，忘了复制会在 `go test` 就暴露。

---

## 八、高棉语检索修复（2026-09-14，Phase 0+1）

背景：`010` 的 GIN 索引与查询都走 `to_tsvector('simple')`。高棉语没有词间空格 → 一整段
变成一个 token，词法腿召回≈0；`032` 给中文加了 bigram + pg_trgm 兜底，但 `isCJK` 不含
高棉文（U+1780–17FF），所以高棉语连字符级兜底也没有，实际退化成纯 dense。详见本轮核对
（`rag/service.go` 的 `searchLexical` / `searchTrigram` / `Search`）。

本轮改动（无迁移、无重嵌入）：

- 新增 `rag/normalize.go`：`NormalizeText`（NFC、高棉数字→ASCII、ZWSP/ZWNJ/ZWJ/NBSP→空格、
  空白折叠，且幂等）；在摄入端（Upload/Update/index/URL 刷新）和查询端（Search/lexical 腿）调用。
- `cjkBigrams` 泛化为 `lexicalNgrams`：CJK 保持 2-gram，高棉语用 3-gram（pg_trgm 的 GIN 只对
  3 字符以上的 ILIKE 模式有效），过短的 run 回退整段；`Search` 的 gate 由 `hasCJK` 改为
  `hasLexicalScript`（CJK 或高棉文）。
- `Search` 增加 Debug 级分腿计数日志（dense / lexical / trigram）。
- 离线评估统一走 `cmd/rageval` CLI：设 `DATABASE_URL` 与 `-eval` JSON（`{"queries":[{"query":"...","expect":[doc_id,...]}]}`）运行，**租户用 `-user` 指定**（默认 1，但必须给知识库真正的 owner，否则检索全空、整轮变成"没资料时它会怎么答"—— 见 §十三），输出每条腿的 recall@5/@10 与 MRR@10。
  （2026-09-21 安全审计后移除了等价的 `TestRetrievalEval` 测试入口——它读取任意环境变量路径，被判定为路径遍历入口；CLI 是唯一的评估入口。）

已知边界：**存量文档**的 chunk 内容不会自动重新归一化，ZWSP/数字变体场景只能部分受益；
需要完全生效时按文档重建 chunk（只刷词法内容，不重嵌入，可与 Phase 2 一并做）。

后续（未做）：Phase 3 PDF 抽取
质量检查与 Gemini 视觉 OCR 回退 + 图片入库；Phase 4 embedding 换代（BGE-M3 1024 维需新列
回填、HNSW 重建、`RAG_*` 阈值重校准，不要直接 `ALTER COLUMN`）。

### Phase 2（2026-09-14）：分词列 + 独立词法索引（迁移 058）

- 迁移 058：`knowledge_chunks` 增加 `content_seg TEXT` + `content_tsv TSVECTOR`（GIN 索引）。
  列可空，保留回滚窗口；旧行由启动时的后台 backfill 填充——**只刷这两列，不重嵌入**。
- `rag/segment.go`：索引端与查询端共用同一套切分——CJK 2-gram、高棉文 3-gram、普通词保留。
  `SegmentedTSQuery` 把含脚本的查询编译成 `word & (gram | gram | ...)`；纯拉丁查询仍走
  `websearch_to_tsquery`，行为不变。选 3-gram 是因为 pg_trgm 的 GIN 只认 3 字符以上的模式；
  在 PG 17 上实测高棉语组合符号会保留在 token 内，所以原始 n-gram 可以直接进 tsvector。
- `searchLexical`：含脚本时用 `to_tsquery` 匹配 `content_tsv`，并与旧的
  `to_tsvector('simple', content)` 表达式 OR，兼容尚未 backfill 的行。
- 测试：`segment_test.go`（单元）+ `segmented_lexical_test.go`（`RAG_TEST_DSN` 门控，
  真实 PG 上验证高棉语命中、高棉数字查询、旧行回退分支）。
- 说明：这是字符 n-gram 方案，不是语言学分词；将来接 khmer-nltk/CRF 时只需替换
  `runNgrams` 并重建 `content_tsv`，与 embedding 解耦。

### Phase 2.1（2026-09-14）：线上仿真测试发现的三处修复

在真实部署上用 14 篇高棉语知识库 + 41 题仿真跑出来的问题：

- **`searchTrigram` SQL 从上线起就是坏的**：`AND (bool OR bool) > 0` 在 PG 非法
  （`operator does not exist: boolean > integer`），错误在 `Search` 里被静默忽略 →
  CJK/高棉语 ILIKE 兜底腿**从未真正生效**。修掉 `> 0` 后，该腿在 40 题上
  recall@5 从 0% 升到 97%（MRR 0.853）。
- **分词 tsquery 的 AND 语义过严**：`SegmentedTSQuery` 原来把词条组用 `&` 连接，
  自然语言问句里的疑问词（ប៉ុន្មាន / អ្វី / មែនទេ）在文档里不存在 → 3/40 题 0 命中。
  改为平铺 OR（`word | (gram | gram)`），靠 `ts_rank_cd` 覆盖率排序 + RRF/rerank 兜精度：
  lexical 腿 recall@5 85% → 97%，MRR 0.783 → 0.917。
- **`Search` 未按 topK 截断**：dense 领先时会跳过 rerank，fused 里最多 4×topK 的
  chunk 全部进入 grounding prompt——实测平均 11.6 条来源、3092 tokens/轮。加上
  `fused = fused[:topK]` 后：来源 3.4 条、tokens 1486/轮（−52%），单题多跳场景
  由 RRF 排序 + OR 召回补偿。


### Phase 3（2026-09-14）：摄入质量与运行告警

- **图片知识入库**：`.jpg/.jpeg/.png/.webp` 进 `AcceptedExtensions`，上传走
  `gemini.ExtractDocumentText`（逐字转写，禁止翻译/转写高棉文），商家拍照资料可直接入库。
- **PDF 坏提取回退**：`rag.LooksLikeBadExtraction`（<40 runes 或 >5% U+FFFD 替换符）；
  PDF 原生抽取不达标时把原文件交给视觉 OCR，OCR 再失败则保留原文并告警日志。
- **出站失败告警**：最终投递失败（PolicyError 或重试耗尽）走 Redis 15 分钟窗口计数，
  达到 5 条触发 `PlatformAlert`（按平台各一，key 去重）。
- **来源多样性**：`Search` 输出按文档去重（每篇最多 2 chunk）后再截断 topK，
  防止长文档占满 grounding 窗口。
- **编译阈值可调**：`RAG_COMPILE_MIN_RUNES` / `RAG_COMPILE_MAX_RUNES`
  （默认 600/60000；短文档默认跳过编译，也就不进矛盾检测）。
- **CI**：新增 `.github/workflows/ci.yml`（后端 build/vet/test + 前端 build）。
  注意 CI 无数据库，`sqlcheck`、RAG 评估与 DB 门控测试仍会 skip，需按 §七 手工跑。
- **回填吞吐实测**：本机 PG 17，2000 个 chunk 128ms（≈15,666 rows/s），
  百万级约 1 分钟（分批 200，逐行 UPDATE；大库上量前建议按此估算窗口）。

#### OCR 路径实测（2026-09-14 线上）

- 用系统 CoreText 渲染的高棉文 PNG 上传 → Gemini 逐字转写完整（含 KWF-RO-100 / F-RO75 /
  电话），`NormalizeText` 把 OCR 出来的高棉数字归一化为 ASCII；
- 图片型"扫描 PDF"原生提取报 `no extractable text`：该错误同样会触发 OCR 回退
  （修复前只在 err==nil 后判断空文本，会误拒）；回退后检索/回答均验证通过；
- 近重复提示正常（扫描版与图片版相似度 0.948，上传时返回 `similar_docs`）。
- 最终检索指标（生产库全量 16 篇测试文档）：lexical recall@5 97% / MRR 0.917；
  trigram 95% / 0.851；fused 97% / 0.906（本地无 dense key，生产还叠加 dense 腿）。

#### 评估（2026-09-14）：CRF 分词 vs 3-gram（离线对照）

用 khmercut（Rust/Python，MIT，CRF 模型源自 khmer-nltk 系谱）在本地 PG 上把同一
14 篇 KB 分别以 3-gram 与 CRF 词建 `tsvector('simple', ...)`，同一套 40 题、同一
`to_tsquery + ts_rank_cd` 形状：

| 表示 | recall@5 | MRR@10 | lexemes/doc |
|---|---|---|---|
| 3-gram OR（现状） | 40/40 | **0.942** | 170 |
| CRF OR | 39/40 | 0.900 | 47 |
| CRF OR（去疑问词） | 40/40 | 0.908 | 47 |
| CRF AND（去疑问词） | 15/40 | 0.362 | 47 |

结论：CRF 在这套数据上**不提升词法检索**（召回持平、MRR 更低），优势是索引小约 3.3×；
继续使用 3-gram，CRF 仅在需要词级功能（同义词/缺口聚类）或索引体积成为问题时再评估，
且应保留 n-gram 兜底 OOV。工具与复现：`tools/khmer-segmentation-compare/`。

#### 决策记录（2026-09-14）：暂不更换分词与嵌入

1. **词法表示：维持 3-gram（不换 CRF）**。依据：同语料 40 题离线对照中 3-gram OR
   recall@5 40/40、MRR 0.942，CRF OR 39/40、0.900；CRF 的唯一优势是索引 lexeme
   小约 3.3×（47 vs 170/文档），当前生产 KB 只有几十个 chunk，不构成理由。
   重新评估的触发条件：a) 索引体积/写入延迟成为实际瓶颈；b) 需要词级功能
   （同义词表、停用词、缺口聚类）；届时保留 n-gram 兜底 OOV。
2. **Embedding：维持 Gemini 768 维（不换 BGE-M3）**。依据：fused lexical-only 已达
   recall@5 97%/MRR 0.906，尚无证据表明 dense 是瓶颈；BGE-M3 引入 1024 维迁移、
   自托管（服务器 4 vCPU/7.8GB、无 GPU）或第三方依赖，且高棉语效果未验证。
   重新评估的触发条件：用评估集证明失败案例集中在语义改写（dense 漏召），
   届时按 Phase 4 的新列回填方案执行并重校准阈值。
3. **阈值（2026-09-14 实测）**：floor/ratio 保持 0.35/0.75；`RAG_RERANK_SKIP` 从 0.70
   下调为 0.60（本数据上 rerank 有害）；topK=5、来源 800/2000 保持。真实流量分布变化时重测。

#### 关键修复（2026-09-14）：RRF 融合丢失 DenseSim → rerank 误触发

`fuseSearchResults` 合并同一 chunk 的多腿命中时，用跨腿**不可比**的 `Similarity` 决定保留哪份
（trigram 是命中计数 1–8，dense 是 0–1 余弦），于是一个 trigram 命中会覆盖 dense 版本并清空
`DenseSim`。后果：`topDense` 被拉低 → `leaderClear`/`signalsAgree` 失真 → rerank 在本该跳过的
查询上触发；rerank 的 `rerankMin` 过滤又把来源从 5 条削到 1–3 条，同配置两次跑结果还不一致。
修复：融合时保留任一存在的 `DenseSim`（+`fuse_test.go`）。用 `rageval` 在真实 KB 上验证：
修复前管线 3.6 来源、44/45；修复后 skip=0.60 为 45/45、5.00 来源；线上同一查询来源 1→5。

#### 真实商家知识库 + dense 阈值校准（2026-09-14）

- 数据：19 篇长文档（多章节/表格/规格页/生效与过期促销/FAQ/2102-rune 手册多 chunk）+ 9 篇编译
  子文档；45 题评估集（含修好的批发运费冲突题）。
- 分腿（生产库 + 真 Gemini dense）：dense recall@5 45/45、MRR 0.856；lexical 41/45、0.673；
  trigram 43/45、0.809；fused 45/45、MRR 0.911。
- floor/ratio 扫描：安全区到 0.60/0.90（候选 24→10.5，指标不降）；0.70/0.95 崩到 28/45。
  保持默认 `0.35/0.75`（提高只减少 rerank 输入；0.50/0.85 时管线反而略降到 43/45）。
- rerank 扫描：skip=0.60（0 次 rerank）45/45、MRR 0.911、5.00 来源；skip=0.70（11 次）
  44/45、0.904、4.13；skip=0.90（35 次）42/45、0.900、2.76。结论：本数据上 rerank 有害，
  将生产 `RAG_RERANK_SKIP` 从 0.70 下调为 **0.60**（弱查询仍会 rerank）。
- 工具：`backend-go/cmd/rageval`（在服务器上运行，复用 DATABASE_URL 与 model_configs，
  一次嵌入遍历 + 内存门控扫描；`-pipeline` 额外跑含 rerank 的生产路径）。

#### topK / 来源长度实验（2026-09-14）

针对真实 KB 的多跳题 r41（长手册里的型号价格 + 另一篇的区域运费）测了三档：

| 配置 | 全量 45 题 | r41 | 单题 tokens |
|---|---|---|---|
| **topK=5 · 来源 800/表格 2000（默认）** | **44/45 (97.8%)** | 答"查不到"并安全转人工 | 2899（均值） |
| topK=8 · 来源 800/2000 | 未跑全量 | 修复（899+7天） | 3124 → 4644（+49%） |
| topK=8 · 来源 450/表格 1200 | 43/45 (95.6%) | 修复 | 3586 |

topK=8 能补上长文档覆盖，但成本 +49%；缩短每条来源虽控制住 token，却把营业时间、E1 等
**文档后段事实**截掉，反而多丢两题。结论：维持 topK=5 与 800/2000；
`RAG_TOP_K` / `RAG_SOURCE_LIMIT_RUNES` / `RAG_TABLE_LIMIT_RUNES` 保留为可调旋钮（默认即当前值）。
已知局限：长文档跨主题多跳在 5 个来源槽位下可能漏答，模型会安全地转人工；若真实流量中
此类占比高，再评估提高 topK 或按章节检索。

#### 最终阈值决策（2026-09-14）

- `RAG_SIMILARITY_FLOOR=0.35`、`RAG_SIMILARITY_RATIO=0.75`：**保持**（安全区到 0.60/0.90，
  再高在 0.70/0.95 崩到 28/45；提高只减少 rerank 输入）。
- `RAG_RERANK_SKIP=0.60`（原 0.70）：**调整**。本数据上 rerank 单调有害
  （skip 0.60→45/45·0.911·5.0 来源；0.70→44/45·0.904·4.13；0.90→42/45·0.900·2.76），
  弱查询（dense top < 0.60）仍会走 rerank。
- 真实 KB 46 题端到端复测：**44/45（97.8%）**、grounded 100%、平均 4.5s、0 mock、0 error。

## 九、Jev 决策中间层（2026-09-21）

按全局规则「决策点优先用 Jev」落地六个接入点，客户端在 `internal/typesafe`
（单端点 `POST https://api.typesafe.ai/v1/systemone`，无 Go SDK，直连 HTTP；
`TYPESAFE_API_KEY` 缺失时客户端为 nil，所有站点保持旧路径）。

| # | 接入点 | 原语 | 降级 |
|---|---|---|---|
| 1 | 轮次分类 `judgeTurnJev`（管道+widget 共享） | Choice(sentiment/intent/topic)+Noul(escalate) | 快模型 JudgeTurn |
| 2 | RAG rerank `rerankScoresJev` | 每候选一个 Score，单次并行 | Gemini RerankChunks |
| 3 | 入站预路由 `RouteInbound`（widget+管道） | Choice 四路 | 保持全量 grounding |
| 4 | 回复护栏 `GuardReply` Noul×3 | promises_handoff/leaks_sources/unsafe_claim | 仅正则网 |
| 5 | 通知分流 `worthPinging` | Noul（fail-open） | 照旧 ping |
| 6 | topic 打标 → `sessions.tags` 的 `topic:*` | Choice（随 #1 同批） | 不写 tag |

**阈值旋钮（env，默认值均在真实数据上校准过）**：`JEV_RULE_SOLO_MIN=0.90`
（`TurnTriggerFor` 的独立 Noul 安全阀）、`JEV_TURN_ESCALATE_MIN=0.60`
（`judgeTurnJev` 写入 `verdict.Escalate` 的阈值——**与上一个不是同一个 bar**；
本行曾把它俩并写成 `JEV_TURN_ESCALATE_MIN=0.90`，2026-09-28 核对代码后更正）、
`JEV_RULE_CONFIRM_MIN=0.70`（intent/情绪规则需 Noul 确认）、
`JEV_ROUTE_HANDOFF_MIN=0.80`、`JEV_ROUTE_CHITCHAT_MIN=0.80`、`JEV_GUARD_MIN=0.70`、
`JEV_GUARD_HANDOFF_MIN=0.85`、`JEV_NOTIFY_WORTH_MIN=0.30`、`JEV_NOTIFY_BUDGET_MS=2000`
（新消息 ping 的 Jev 预算，超时 fail-open）、`JEV_CONTRADICTION_MIN=0.60`（入库矛盾复核）、
`JEV_TURN_BUDGET_MS=4000`（**Jev** 侧轮次判定预算，`internal/platform/pipeline.go` 的
`turnBudget`；超时即判 Jev 失败、退回快模型）、`GEMINI_JUDGE_BUDGET_MS=10000`
（**快模型回退**侧的判定预算，`internal/gemini/gemini.go` 的 `JudgeTurnBudget`；超时或 JSON
解析失败即判失败、调用方静默降级）。
**这两个不是同一个旋钮**：2026-09-28 加后者时误用了已存在的 `JEV_TURN_BUDGET_MS` 名字，
结果一个变量同时管两侧、默认值 4000 与 10000 打架，已改名。

**校准结论（cmd/jeveval，370 条真实轮次，ground truth=30 分钟内是否产生 handoff）**：
- Jev 0 失败；sentiment 与快模型一致 99.2%、intent 81.1%、最终决策 86.8%（分歧全部是
  快模型多升级 48 条普通产品问题——快模型在过度升级）。
- 旧意图规则层（complaint/refund/legal 自动转人工）套在 Jev 标签上会过触发：每 370 条
  20-26 个假 handoff；加 Noul≥0.70 确认后降到 3。故 Jev 路径用 `TurnTriggerFor`
  （规则需确认 + 高安全阀 + no-KB 规则无条件保留），快模型回退路径仍用 `TurnTrigger`。
- rerank A/B（生产 KB 56 条查询）：top-1 与 top-3 与 Gemini rerank **100% 一致**。
- ground truth 是旧系统行为而非正确性oracle，precision 无绝对标准；上述数字用于
  选旋钮，不用于宣称准确率。

**复测（2026-09-28，415 条去重真实轮次）**：

| 快模型 | 计入 | 失败 Jev/快模型 | 决策一致 | sentiment | intent | Jev 单方升级 | 快模型单方升级 |
|---|---|---|---|---|---|---|---|
| `gemini-3.5-flash-lite`（当时线上实配） | 413/415 | 0 / 2 | 85.7% | 93.7% | 78.7% | 1 | 58 |
| `gemini-3.5-flash` | 383/415 | 0 / 32 | 85.9% | 98.4% | 85.1% | 0 | 54 |
| **`gemini-3.8-flash`** | 403/415 | 0 / 12 | **87.1%** | 98.8% | 81.9% | 1 | 51 |
| `gemini-3.8-flash`（部署配置复跑, `-workers 3`） | 397/415 | 0 / 18 | 87.2% | 99.2% | 81.4% | 0 | 51 |

- 上表那种 86.8% / sentiment 99.2% 的组合是 **flash 时代**的数字：线上
  `GEMINI_FAST_MODEL` 之后换成了 `-lite`，sentiment 一致掉到 93.7%，决策一致率基本**没变**
  （85.7%–87.1% 随快模型浮动）。差额来自快模型换档，不是 Jev 决策层回归。
- 上表的两件事完整复现：**Jev 单方升级 0–1 次**、分歧**全是快模型多升级**（47–58 条）。
- 3.8 一致率最高且最稳（三次复跑 86.9 / 86.9 / 87.1，极差 0.2 个点）、误升级最少
  ⇒ 2026-09-28 起 `GEMINI_FAST_MODEL=gemini-3.8-flash`。
- **快模型的"失败"是超时预算，不是能力。** `JudgeTurn` 的预算是 `GEMINI_JUDGE_BUDGET_MS`
  （默认 10s），超时或 JSON 解析失败就返回失败、调用方静默降级。同一提示词在同一台机器上，
  dropout 在 2.4%–13.5% 之间摆动，上游降级窗口里到 18%–32%，**与并发无关**
  （`@workers=8` 为 2.4–2.9%，`@workers=2` 反而 13.5%）。单次看到某个快模型"不行"是误判，
  要看分布。
- **换 3.8-flash 不是免费升级，代价要一起看。** 它答得更好（决策一致 87.2% / sentiment
  99.2%）但也更常撞上那个预算：同一份 415 条语料，`-lite` 只丢 2 行、`gemini-3.5-flash`
  丢 32 行、3.8-flash 丢 12–18 行（同一模型两次复跑 12 与 18 ⇒ 与上游窗口相关）。丢的行走
  Jev→快模型的降级链，不是丢掉判定；要换这个取舍就调 `GEMINI_JUDGE_BUDGET_MS`（免重建）。
- **这条路的权重远小于 100%。** `judgeTurn`（`internal/platform/pipeline.go`）先问
  `judgeTurnJev`，只有 Jev `!ok` 时才调 `p.Gemini.JudgeTurn` ⇒ 快模型对**轮次判定**的影响
  正比于 Jev 的 dropout（本仓实测 Jev 0 失败）；它 100% 负责的是 rerank / 路由 / 护栏 /
  notify 分流。上表的"决策一致"量的是**回退模型的备胎质量**，不是线上主路径的准确率。
- **这份语料撑不起 precision/F1。** 418 轮里只有 136 条不同消息，其中 2026-09-14 一天占 282 条；
  ground truth 只有 10 个正样本，其中 5 个是客户自己要求转人工 ⇒ 分类器可负责的正样本只有 5 个。
  线上 gate=stack@0.60 在这个 population 上的 TP=5 / FP=31，precision 13.9%，但那是
  5 个正样本算出来的数字，没有统计意义。

**踩坑记录**：
- 生产中继（Cloudflare AI Gateway）已下线 gemini-2.5-flash 系（404 "no longer
  available to new users"）→ `FastModel` 改为 `GEMINI_FAST_MODEL` 可调，默认
  gemini-3.5-flash；嵌入模型 gemini-embedding-001 仍可用。旧 FastModel 是 const，
  辅助调用（rerank/JudgeTurn/compile）此前一旦有流量就会静默失败。
- widget 分类器曾与管道漂移（不走 Jev、不写 tags）——已统一为
  `Pipe.JudgeTurnFor` + `Pipe.PersistTurnVerdict`；新增分类逻辑只允许走这两个入口。
- widget 流式回复后的护栏必须用 `persistCtx`（访客挂断会取消请求 ctx）。

复跑校准：导出 SQL 在 `backend-go/cmd/jeveval/turns.sql`（`chat_messages` ⋈ `sessions`
⋈ 30 分钟窗口 handoff trigger，`COPY … TO STDOUT WITH (FORMAT csv, HEADER true)`），
在服务器上 `set -a; . ./.env-go; set +a; psql "$DATABASE_URL" -f turns.sql > turns.csv` 即可；
然后 `TYPESAFE_API_KEY=… go run ./cmd/jeveval -csv turns.csv [-gate stack|noul|jev|confirm]`。
`-mode agree` 对比快模型（**必须看它印的 `failures: jev=/gem=`**，否则会像 2026-09-28 那样
把 32% 的静默 dropout 当成模型结论）、`-mode speed` 做同输入延迟 A/B、`-mode rerank`
需 `DATABASE_URL` 隧道做检索 A/B。

复跑线上能力实测：`TYPESAFE_API_KEY=… go run ./cmd/jeveval -mode live`（22 个
高棉语客服场景，含否定转人工、混合语言、逐 flag 护栏用例；2026-09-21 首跑
20/22，两个"失败"均为期望过严的标签分歧：500 件批发标成 price 但经 solo
安全阀正确升级；"សួស្តី!" 标 neutral 而非 positive。行为零误判）。

高棉语能力实测（`-mode khmer`）：官方文档只承诺「英语最强，其他语言可用但
不等优，需自行实测」（models 页 Language support，未点名高棉语）。实测 2026-09-21：
最小对（单词翻转语义）4/4 通过（gap 0.75-0.95，Gemini 交叉读一致 4/4）、单词
词汇探测 4/4（0.90-0.95）、乱码/纯问候对照 2/2（0.20/0.09 不乱答 yes）。
结论：Jev 真实读懂高棉语，但准确率低于英语的文档警示仍然成立——阈值必须
持续用自有数据校准，勿直接抄英文语料的数字。

### Jev 与知识库的三处增强（2026-09-21 晚）

| # | 接入点 | 设计 | 实测 |
|---|---|---|---|
| A | 入库矛盾复核+补漏 | compile LLM 列出的矛盾逐条 Jev Noul 确认（`JEV_CONTRADICTION_MIN=0.60`，fail-open 全保留）；对未命中的现有文档 excerpt 补漏 sweep（同批并行，低危条目） | prompt 锐化前 0.31/0.32 无区分度（复合判断），锐化为「同一事项、不同具体值」后 **0.99/0.08**；doc 级 sweep **0.80/0.05** |
| B | 引用核查 | GuardReply 第 4 问 `supported_by_sources`（仅 grounded 回复携带；缺答案=审计不完整→fail 不-ok），未支持→店主告警（10 分钟节流） | 支持的回复 **0.87** vs 编造价格 **0.03** |
| C | 检索改写 | 不再信任 Gemini 自由文本改写为首选：代码出候选（原文/上一轮关键词承接/本轮分词词），Jev Choice 择优；`rewriteQueryJev` 三态（nil,true=原文最佳 / q,true=选中 / nil,false=降级 Gemini 改写） | carry-over 候选 conf 0.95；线上两轮会话无告警、hit_count 正常 |

教训：Jev 的 Noul 做**双 claim 对比**这类复合判断时问法必须拆到位（"同一事项+不同具体值"），
笼统的 "do these contradict" 没有区分度——这正是 typesafe 技能「一个问题一个窄判断」的实例。
compile 的 E2E（真实上传矛盾文档）未跑，验证止于 prompt 级探针 + fail-open。

---

## 十、RLS 兜底（061，2026-09-23）

### 它是什么

`061_rls_tenant_backstop.sql` 给 `chat_messages` 和 `knowledge_chunks` 启用并
`FORCE` 了 Row-Level Security（其余租户表自带 `user_id` 列，漏写 WHERE 在同一条
语句里可见，不需要这层）。策略按会话 GUC `app.user_id`（int4）判定归属：

- `chat_messages` 经 `sessions.user_id`（有 `idx_messages_session` 可走）；
- `knowledge_chunks` 经 `knowledge_documents.uploaded_by`（走 `idx_knowledge_chunks_doc`）。

GUC **已设**时：读/写全部被约束到该用户的行——**查询忘写 WHERE 也不再泄漏**，
跨租户 INSERT 被 `WITH CHECK` 拒绝。这正是 2026-09-12 审计挖出的那五处漏洞的形状。

### 为什么 GUC 未设时 fail-open（刻意的，不是偷懒）

- **部署顺序**：migrate-go 先跑、新二进制后启动；旧二进制（不懂 GUC）必须照常工作，
  fail-closed 会当场打断线上，也打断「二进制回滚」这个受支持的运维动作；
- **系统路径**：platform pipeline 与后台 worker 天然跨租户，必须看得见所有行。

所以 061 上线当天**不改变任何行为**——它是「武装了但未上膛」的兜底：迁移先就位，
把后续收严的成本从「迁移+代码」降为「只改代码」。

### 已知边界（不夸大）

| 边界 | 说明 |
|---|---|
| 超级用户/BYPASSRLS 无条件绕过 | `cmd/server` 启动时检测并在日志告警 `RLS tenant backstop (061) is bypassed`；生产 `DATABASE_URL` 应使用专用非超级用户角色 |
| TRUNCATE 不受策略约束 | 全仓无 TRUNCATE 路径，隐私删除走逐行 DELETE |
| GUC 设成非数字 | `::int` 转换报错（响亮失败），不会静默放宽 |
| FK 级联删除 | RI 动作绕过 RLS，删除流程不受影响 |

### 测试（DB 门控，与 sqlcheck 同款纪律）

```bash
cd backend-go
DATABASE_URL=... go test ./internal/migrations/ -run RLS -v
```

- `TestRLSBackstopPoliciesInstalled`：两表 enabled+forced、策略存在（没跑 061 会失败并提示 migrate-go）
- `TestRLSBackstopEnforcesWhenGUCSet`：双向读隔离、同租户写放行、跨租户写拒绝
  （savepoint 隔离预期错误）、GUC 未设全可见（**fail-open 契约被断言锁死**，
  谁改默认值谁就得同时改部署手册）

探针数据**提交后由 `t.Cleanup` 按外键安全顺序精确删除**——GUC 必须跨事务可观测，
不能和被检查的语句同事务（第一版这么写，回滚重置 GUC 的同时把种子也滚掉了，
三条断言同时说谎）。`uploaded_by` 无级联所以文档先删，users 级联带走
sessions/messages；若测试进程被杀，残留行带 `__rls_` 前缀+时间戳后缀可辨认。
超级用户连接会 skip（断言会因错误的原因失败）。

### ⚠️ 为什么 GUC 必须经 `app_tenant_id()` 读——扩展协议的 InitPlan 提升

第一版策略把 `current_setting('app.user_id', true)::int` **内联**进 policy 的
EXISTS，被真库测试当场击毙，而且 **psql 完全复现不了**：

- psql 走简单协议、每次执行 custom plan，OR 首分支逐行短路，GUC 为空时
  cast 根本不会发生——三轮手工验证全绿；
- pgx（extended protocol + 语句缓存）下，planner 会把子计划里**不引用外层行**
  的表达式提升为 **InitPlan，在执行起点就求值**：连接池长连接上 GUC 处于
  「`SET LOCAL` 结束后的重置态」，`current_setting(..., missing_ok)` 返回的是
  **空串而不是 NULL**（NULL 只属于从未碰过该 GUC 的连接），`''::int` 直接把
  这张表上的所有查询炸成 `invalid input syntax for type integer: ""`，
  OR 短路根本没有出场机会。

所以 061 提供了 **VOLATILE** 的 `app_tenant_id()`（内含 NULLIF 统一 ''/NULL），
VOLATILE 禁止折叠与 InitPlan 提升，逐行惰性求值，短路才恢复可靠。
**不要把 current_setting 内联回 policy，不要把该函数改成 STABLE**——那两个改动
单独看都「更高效」，合起来就是上面那个生产炸弹。

### 收严路径（真正上膛，按集群逐步做）

1. 逐端点集群引入「租户作用域事务」：`tx := pool.Begin(ctx)` 后第一条执行
   `SELECT app_set_tenant($1)`（061 提供的 VOLATILE 助手，内部就是
   `set_config(..., is_local => true)`）—— **绝不能用 is_local = false**：
   池化连接上会话级设置会把身份泄漏给下一个借走连接的请求，比没有 RLS 更糟。
   `handleSession`/`handleDoc` 包装器是天然的下手点（一个包装器罩住几十条路由）。
2. 每收编一个集群，带 `DATABASE_URL` 跑一次 rls 测试 + sqlcheck。
3. 全部认证路径收编完毕后才考虑翻 fail-closed——那是破坏性变更：迁移先行 +
   旧二进制当场失明，需要与部署手册联动，并放弃「迁移先于二进制」的窗口保护。

### 性能

GUC 未设时每行只多一次 VOLATILE 函数调用（亚微秒级；短路后不评估 EXISTS 子计划）；
GUC 已设时 EXISTS 走两表已有的 session/doc 索引，而现有访问路径本来就是
session/doc 作用域的。注意 VOLATILE 谓词默认并行不安全：这两张表上的全表扫描型
分析查询不会并行化——目前不存在这类查询，出现时用一次性 filter 改写或物化聚合，
或届时再评估把函数改 PARALLEL SAFE 的正确性。

---

## 十一、路由三档扩展：junk 沉默 / urgency / 语义缓存秒回（062，2026-09-23）

补齐「先判定、没把握才下沉」架构的三块缺口。一次路由 = 一个 Judge 批量
（route Choice + urgency Score 并行），检索照旧投机并行。

### junk 档：垃圾消息沉默

- 新路由 `junk`（`routing.go`）：`p ≥ 0.90`（`JEV_ROUTE_JUNK_MIN`，**四档里最高**
  ——错误沉默真客户是路由最坏的失败模式，测试锁死 junk 阈值 ≥ handoff 阈值）。
- 平台渠道（Telegram/Messenger/LINE/Zalo/Instagram）：**真沉默**——不生成、不投递、
  不打字指示后续，客户消息留在收件箱里给店主看。
- Web widget：SSE 访客不能吊着空气泡，回一条纯致谢行（`JunkAcknowledgement`，
  高棉语初稿待母语者过）+ `ignored: true` 事件；没有生成开销。
- 转人工路由升级 handoff 请求行时携带 `urgency`。

### urgency：路由顺带判急

- 同一 Judge 批量里的 Score 三档（routine/elevated/urgent），`ScoreValue` 返回
  概率加权位置 0..2，`≥1.5`（`JEV_ROUTE_URGENT_MIN`）判 urgent。
- **单向升级**：urgent 把 handoff 请求优先级抬到 `high`，永不降档
  （`handoffPriority()`，schema 只有 normal|high）。v1 不落 session 列——
  只作用于当轮升级；后台轮次分类触发的升级（无路由上下文）传空串走静态表。

### reply_cache：语义缓存秒回（迁移 062）

- 表：`reply_cache`（tenant + language + query_embedding(768) + answer + hit_count），
  HNSW 与 chunks 同形。**只缓存守卫后、非 mock 的最终回答**（守卫后落库前写入，
  after-hours 前缀是投递期文案不入缓存）。
- 命中路径：检索投机并行中先查缓存（1 次 embedding + 1 次 HNSW），**未命中零
  额外墙钟时间，命中亚秒返回**，零 token 计费，`model_name='reply-cache'` 可辨认。
  命中跳过守卫（存的就是守卫后的），`ReplyClaimsHandoff` 正则照跑。
- 失效：`rag.Service.KBChanged` 钩子——文档索引成功（上传/更新/URL 刷新/编译子文档
  全走索引 worker）或删除时，**整租户缓存全删**。宁滥勿缺：缓存的是旧 grounding。
- 门槛：`REPLY_CACHE_MIN_SIM=0.92`（余弦，防"那第二种呢"这类追问串答）、
  `REPLY_CACHE_MIN_RUNES=12`（短查询永不读也不写）、TTL 72h、每租户 500 条 LRU。
  `REPLY_CACHE_ENABLED=false` 一键关。
- 嵌入在写入侧多付一次（丢进 SpawnClassifier 可丢车道）；读取侧在检索并行窗内，
  不加墙钟。**embedding 换代（Phase 4）时此表与 chunks 一样要重建。**

### API 变更

`RouteInbound` 返回 `InboundRoute{Route, Prob, Urgency}`（原三值返回的调用方：
pipeline、widget、jeveval live 已全部跟进）；`RouteDecision` 返回三开关
`(escalate, skipGround, silent)`；`escalateToHuman`/`createHandoffRequest` 尾参
`urgency`（无则传 `""`）。

### 验证

- 单测：路由解析/分档/旋钮/junk 门槛/优先级合并（纯单测，无 DB）；
  replycache 机制（hit/miss/阈值/TTL/失效/nil 安全，stub 嵌入 + 真库，DSN 门控）。
- 发布前照旧：`DATABASE_URL=... go test ./internal/migrations/ ./internal/replycache/ ./internal/sqlcheck/`。

### 未做（下轮候选）

urgency 不落 session/分析面；缓存命中率没有运维指标（看日志 `reply cache hit`）；
widget `ignored` 事件前端未特殊渲染（显示致谢行已足够）。

#### 评审修复（2026-09-23，OCR 委托模式全量评审后）

用 `ocr delegate`（确定性选文件 + 规则匹配）+ 宿主 agent 逐 bundle 评审了
a69c7d1..6b8d374，修复全部 4 项发现：

1. **widget 缓存命中补发店主通知**（原先提前 return，店主对该类轮次失明；
   现与平台渠道对齐，且通知 goroutine 一并改用 persistCtx——原实现用请求
   ctx，访客挂断会取消进行中的通知）
2. **widget 侧缓存命中盖 `reply-cache` 戳**：`persistModelReply` 加
   `modelName` 尾参（"" = 当前模型），两个渠道的缓存命中在 DB 里都可辨认
3. **widget 转人工优先级不再硬编码**：`webEscalate`/ai_decision 走导出的
   `platform.HandoffPriority(trigger, urgency)`，与 pipeline 同一套计算，
   杜绝渠道间漂移复发
4. **缓存命中计数按 `cache_id` 主键**（原先按 answer 文本，相同答案的兄弟
   行会被连带 +1）；测试锁死「命中一行、兄弟行计数不动」

顺带修两个评审备注（原本只记录在案）：

- **junk 沉默补全**：店主 💬 通知与打字指示移到路由判定之后——junk 现在
  真沉默（不 ping、不 typing）；billing 计数（bumpMessagesUsed）保留在
  路由前，收到的消息照旧计费
- **分类器如实报告 grounding**：缓存命中的轮次 `hasMatch=true`（答案首次
  生成时确实有 KB 依据），不再喂给「知识库零命中」转人工触发器一个假输入

## 十二、Vertex 区域化模型列表（2026-09-25）

### 它修的是什么

后台模型下拉在 vertex 下实际上只有那一份**手写候选表**（`gemini.go` 的 `vertexPublisherModels`：3 个名字）。
它的注释写着"平台没有 publisher 模型列表路由"——那个结论是在 **`/v1`** 上量出来的，而
`/v1beta1/publishers/google/models` 是**存在**的（2026-09-25 实测：`global`、`us-central1`、
`asia-southeast1`、`europe-west4` 全部 200）。旧代码实际请求的是"项目自有模型"那条路由
（`…/locations/{l}/models`）：它列的是本项目 tuned/uploaded 的模型（生产上是空的），而且那些名字
用本客户端拼不出可调用的 URL —— 于是这份手写表同时成了"唯一来源"和**事实上的上限**：
运维想选的模型只要不在那 3 个里，就看不见。

现在列表直接问平台，并且**按区域**问——因为目录本来就是区域作用域的，而"服务 3.8 的区域"不是
生产跑的区域。

### 机制

| 项 | 值 |
|---|---|
| 路由 | `GET {host}/v1beta1/publishers/google/models?pageSize=100` |
| host | 单区域 `{region}-aiplatform.googleapis.com`；`global` = `aiplatform.googleapis.com`（不带 `global-` 前缀，那个主机名不解析） |
| 版本 | **`/v1/` 形式 404，只有 `/v1beta1/` 有用**（`GEMINI_VERTEX_API_BASE` 覆盖也会被重新限定成 v1beta1） |
| 分页 | 有 `nextPageToken` 就继续取，页数有上限；被截断时写进警告，不假装完整 |
| 鉴权 | 与其它 vertex 调用同一套 SA bearer token（studio key 不会上这条路） |

### API 契约

| 接口 | 说明 |
|---|---|
| `GET /api/v1/admin/models/{id}/available?region=<region>` | `region` 可省 —— 省略 = 服务端配置的区域（`GEMINI_VERTEX_REGION`）。返回**数组**：`{name, display_name, launch_stage, capability, available}`；`capability ∈ chat/embedding/image/tts/live/other`；`launch_stage` 未知为 `""` |
| `GET /api/v1/admin/models/vertex-regions` | `{regions:[{id,label}], current}`；当前配置区域**排第一**（不在静态候选表里也会出现）；纯配置读取，不联网 |
| 响应头 `X-Model-List-Warning` | 列表不完整（区域 404 / 报错 / 返回空）时带原因（含区域名与 HTTP 状态）；**仍是 200** + 能拿到的内容 + 配置中的在用模型 |

### 两条不能违反的规则

1. **配置中的在用模型永远并进结果，且排第一。** 实测：`asia-southeast1` 的列表只有 9 条、**没有
   `gemini-3.5-flash`**，而它在该区**可以调（200）**——它就是生产在用模型。列表没提到它，不代表
   它不可用。
2. **`available` 默认 `true`，列表成员关系从不换算成"不可用"。** 反例同样实测过：`us-central1`
   的列表列了 3.x，单区域 `generateContent` 却 404。所以"不在列表里"和"在列表里"都不是判据；
   唯一权威是后台的「测试」按钮。谁把它改成"在列表里才为真"，谁就会把生产在用模型显示成不可用。

### 实测的区域矩阵（2026-09-25，生产 SA）

| 区域 | gemini-3.8-flash | gemini-3.5-flash | gemini-embedding-001 |
|---|---|---|---|
| `global` | 200 | 429（配额） | 200 |
| `us` | 200 | 200 | 200 |
| `eu` | 200 | 200 | 200 |
| `asia-southeast1` ← 生产 | **404** | 200 | 200 |
| `asia-northeast1` | 404 | 200 | 200 |
| `asia-south1` | 404 | 200 | 200 |
| `us-central1` | 404 | **404** | 200 |
| `europe-west4` | 404 | **404** | 200 |

3.8 同样 404 的还有 `us-east1`、`us-east5`、`us-west1`、`europe-west1`、`europe-north1`、
`asia-east1`、`northamerica-northeast1`、`australia-southeast1`。
之前的文档写"3.6/3.7/3.8 在平台上不存在"，那是把"AI Studio / 只在 asia-southeast1 量过"当成了
平台事实——**3.8 存在，只是只在 `global`/`us`/`eu` 可调**；3.6/3.7 本次未重新实测。

### 列表 ≠ 服务路径（区域可选，但**不是**同一个选择器）

模型页的区域选择器**只改列表**：浏览某个区域的目录不会让流量换区，也不会让目录内容拦住任何
保存动作（`updateModelConfig` 完全不读目录）。

切服务区域是同一张卡片上的第二个动作（下拉旁边的「让服务改用 X」按钮）：它写 DB
`model_configs.vertex_region` → `reloadGeminiFromDB` → `Service.SetVertexRegion` 重建 transport
（只换 host/region，SA token 与 project 不动），**不重启、不改 env**。生效链条只有一个入口：

| 值来源 | 何时生效 | 优先级 |
|---|---|---|
| `model_configs.vertex_region` | 保存时热重载 + 每次启动 | **高**（非空即赢） |
| `GEMINI_VERTEX_REGION`（env） | 启动 | 低（列空时的默认值） |

因此：**切过区域之后，改 `.env-go` 的 `GEMINI_VERTEX_REGION` 再重启不会再移动流量** ——
要交回 env，得把 `model_configs.vertex_region` 清空。这是这套机制唯一会咬人的地方。

两道闸门，都只认 `NOT_FOUND`（配额/权限/网络失败一律不定性，不拦）：

- **切区域时**：用**在用模型**在新区域跑一次 `ProbeModel`，`NOT_FOUND` 就拒绝写入（否则默认配置
  上没切成功就是一次停产）。
- **存模型时**：用**服务区域**（`Service.Region()`，不是浏览区域）探测模型名，`NOT_FOUND` 同样拒绝。

`current`（`GET /admin/models/vertex-regions`）报的是 `Service.Region()`：picker、保存闸门、请求
URL 三处读同一个值，不会再出现「界面显示 global、实际发往 asia-southeast1」。

迁移：`065_model_vertex_region.sql`（`VARCHAR(40)`，与 `ValidVertexRegion` 同一长度上限；该值会
进请求 **host**，所以写入前一律校验，手工改库写进垃圾值只在启动时告警并忽略，不阻断启动）。

### 验证

- `cd backend-go && go build ./... && go vet ./... && go test ./...`（另跑过 `-race`）。
- 单测锚点：v1beta1 路径（含 `global` 主机不带 `region-` 前缀）、分页与页数上限、`launch_stage`
  有无、能力分类（纯函数表驱动）、显示名推导、在用模型并集、列表失败仍 200 且带
  `X-Model-List-Warning`、区域参数校验（含"恶意区域不得把带 token 的请求指向任意主机"）、
  studio 路径零回归。
- 回归守卫：`asia-southeast1` 的**真实 9 条列表**（不含 `gemini-3.5-flash`）必须返回
  `available: true` 且在用模型排第一——这条测试失败就说明规则 2 被改坏了。

### 未做

- `available` 目前没有任何"有正向证据才置 false"的来源（探针/退役信号都没有），因此它今天是恒
  `true` 的；字段保留是为了将来能编码这种证据而不改线上形状。
- 区域候选表是静态的（平台没有"列出所有区域"的接口），只保证 `current` 来自真实配置（`Service.Region()`，即 DB 开关 > env 默认）。
- 目录不做缓存：每次打开模型页都实打实问一次平台（一次一页 100 条）。

### 订正（2026-09-28）：3.8 的 p50 被生产否掉，长尾没卸

本节区域矩阵与 `deploy-khmer-ai-cs/references/dev-guide.md` §11.7 据一轮基准（global /
3.8-flash p50 22.51s、p95 106s）得出「不切」。生产数字只否掉其中一半：

- 搬迁后（`GEMINI_VERTEX_REGION=global`、DB 默认模型 `gemini-3.8-flash`，2026-09-26 19:11
  改区域）`/api/v1/chat` 的 200 响应 72 条中有 **28 条真正在生成**（其余 44 条 <50ms，命中
  `reply_cache`）：p50 **3285ms** / p90 **6612ms** / max **9626ms**，**无一条 >10s、无读
  超时** ⇒ 生产最慢的一条都不到基准 p50 的一半，**p50 不复现**。
- ⚠️ 但 28 条量不出 p95：§11.7 正文那条「`s.client.Timeout = 60s` × `postWithRetry` 最多
  3 次 ⇒ 单回合最坏接近 180s」的风险**仍然成立**，只是这份样本里没出现。§11.7「已搬，
  风险未卸」的框架照旧 —— 别把「否掉 p50」读成「否掉长尾」。
- `GEMINI_FAST_MODEL` 那条路（`JudgeTurn`/rerank/路由/护栏/notify）是另一条量级：实测 3.8
  p50 **2421ms**，比 `gemini-3.5-flash` 的 3564ms 还快约 1.5× ⇒ 2026-09-28 起
  `.env-go` 的 `GEMINI_FAST_MODEL=gemini-3.8-flash`。
- **未查清**：`token_usage` 里 `gemini-3.8-flash` 有 904 行、自 2026-09-06 04:17 起，与
  §11.7「09-26 19:11 才改区域」并存不上（按区域矩阵 3.8 在 `asia-southeast1` 该 404）。
  查清前别拿它推断区域历史。

准则：**基准与生产差一个数量级时先信生产，但「否掉 p50」与「否掉 p95 长尾」是两件事。**

### 长尾归因（2026-10-04 实测：重试从未叠加，长尾来自各跳预算）

上面那条「单回合最坏接近 180s」的范围终于被量过了。结论是**它没被踩到，但它旁边的两个问题是真的**。

**一、重试不叠加（实测，排除）**：给 `postWithProviderRetry` 每次尝试加埋点后跑 31 例回复评测，共
**98 次尝试，`attempt=2/3` 出现 0 次**。所以 181.2s 是纯理论边界，不是正在发生的事；把
`postMaxAttempts` 从 3 收到 2 买不到任何东西。

但 `callBudget()` 的默认值**就是** `retryWorstCase()`（= 3×60s + backoff = 181.2s）—— 一个
「兜住最坏值」的预算被设成了最坏值本身，等于没兜。已改为**一次尝试（`postAttemptTimeout`，60s）**：
单次慢响应仍能跑满 60s（不牺牲现状），而「慢的第一次之后再来一次慢的」被禁掉；重试本来是为
**快速**传输失败存在的（握手丢包），那种失败几乎不消耗预算，所以照旧能重试。

**二、长尾由「辅助跳付满额预算」组成**（各跳分开计时，2026-10-04）：

| 跳 | 观测 | 预算 |
|---|---|---|
| 查询嵌入 | 常态 **0.34–1.2s**（rageval 90 次采样 0 次超时）；但会**成簇突发**到 **11.29s**（同文档记的 global 冷启 ~11.2s 同量级） | `GEMINI_EMBED_BUDGET_MS` **5s** → 突发期整条 dense 腿被丢掉（fail open，只发一条 WARN） |
| 重排 | Jev 路径快；回退到 fast-model 重排时 **7.81s / 8.01s**（撞满 8s 预算） | 8s |
| 生成（客户真正在等的那段） | p50 5.3s / p90 7.1s / **max 21.2s** | 见上（60s） |

于是**单例最坏 ≈ 5s（嵌入超时）+ 8s（重排超时）+ 生成**，实测最慢一例 24–25s。
⚠️ **不要为了压这个尾巴把嵌入预算切小**：5s 已经会让突发期的 dense 腿消失（高棉语检索的骨干，
`LEG dense recall@5 84%` vs `lexical 64%`），而丢掉它**不会更快**——探针网络已经花了 5s。
真要动它，方向是**调大**（覆盖实测 11.3s）并在提示词/重排侧把总时长拿回来。

**三、已收窄的一处叠加**：`topDense == nil`（嵌入腿没出任何候选）时**不再触发重排**。原条件
`!leaderClear && !signalsAgree` 在 `topDense==nil` 时两边都为假（都带 nil 守卫）却仍进入重排 ——
即「先白等 5s 嵌入超时，再花 8s 重排只剩词法的候选」，而那正是重排最能少做事的局面。现抽为纯函数
`rag.shouldRerank` 并覆盖真值表（含 nil 行）。检索基线不变：`PIPELINE recall@5 40/45 · MRR 0.738`
（改前改后逐位相同）。

**四、重排在这份数据上的代价与收益（复测，因为语料 2026-09-24 长过一轮）**：`RAG_RERANK_SKIP`
扫描，45 题、`-pipeline`：

| skip | recall@5 | MRR | avg_sources | 45 例墙钟 |
|---|---|---|---|---|
| **0.60（现行）** | **40/45** | 0.738 | **5.00** | **40.8s** |
| 0.90 | 38/45 | **0.789** | 2.69 | **258s** |

即重排拿 **~6s/例** 换 MRR、同时**掉 2 条 recall 并把来源从 5.00 削到 2.69**。与 2026-09-14 那次
「rerank 单调有害」的方向一致，但今天不是单向恶化（MRR 是升的）—— 所以**不改**这个阈值：现行 0.60
已经是快路径，改与不改是产品取舍，不是工程缺陷。要改先想清楚「宁少 3 条来源换 MRR」是否成立。

## 十三、高棉语答复质量：先能测，再能改（2026-09-29）

三件事一起落的，顺序就是依赖顺序：**没有度量，前两件都无法验证**。

### 1. 度量：`jeveval -mode reply`（新）

- 评测集 `kb/evals/reply_eval.json`：23 例。**高棉语问句全部逐字取自 KB 自己的 FAQ**
  （`kb/docs/SVN-021-faq.md` 的 ស1–ស29，正则抓取，不是模型写的问题），
  期望事实同样来自 FAQ 答案（价格、MOQ、交期、电话）。另加 2 例英文/中文提问与 1 例陷阱
  （问 KB 里不存在的 40 kg/m³ 档，看它会不会编价）。
- 跑法（**在服务器上跑**，服务账号私钥不出机器）：
  ```bash
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o jeveval ./cmd/jeveval
  scp jeveval kb/evals/reply_eval.json root@<host>:/root/khmer-deploy/
  ssh root@<host> 'cd /root/khmer-deploy && set -a; . /opt/khmer-ai-cs/.env-go; set +a; \
      ./jeveval -mode reply -eval reply_eval.json -user 7 -v'
  ```
- 走**生产路径**：DB 默认模型配置（含存库提示词与区域）+ `rag.Service.Ground` 检索 +
  `rag.AugmentMessage`，与 `pipeline.go` 同形。
- 两层打分，职责不重叠：**确定性检查**定通过/失败（必须出现的事实、不得出现的引用标记/
  隐形字符/高棉数字、以及"整段没几个高棉字母"＝用错语言）；**judge**（快模型）打
  语言/敬语/自然度/格式四轴 0-3 分，只当趋势看，rubric 就写在评测集里以便审计。
- ⚠️ `-user` 必须给**知识库真正的 owner**（当前 KB 在**测试租户** `uploaded_by=7`，见 `kb/SPEC.md`）：默认的 1 属于别的租户，
  KB 检索会全空，于是整轮变成"没有资料时它会怎么答"——第一次跑就是这么被骗的。
  同理，任何 `no KB match` 都先算检索问题（去跑 `rageval`），别急着怪提示词。

首轮基线（2026-09-29，`-user 7`，生产 KB）：**18/23 通过，judge 均值 11.82/12**。

### 2. 提示词：高棉语专章 + 分语言转接句（`gemini/prompts.go`）

- 新增 `## Khmer quality`：不逐字翻译、**Khmer 回复里不得出现整句中文/英文**、
  禁止罗马化、称呼统一为 `អ្នក`（与 `bot_texts.go` 自己那套文案一致）、
  价格/数量/电话用 ASCII 数字（禁 `០–៩`）、不得夹泰/老挝同形字、不得插零宽字符、
  币种照记录不得换算。附两条高棉语示例（一条价格问答、一条转人工）。
- **修掉一个真 bug**：转人工那句原来只给中文模板（`已为您转接人工客服…`），
  高棉语客户会收到中文，而 `ReplyClaimsHandoff` 只认完成式承诺——现在三种语言各给一句，
  高棉语那句用的是仓库已有的 `handoffAcknowledgement` 原句（同时命中检测短语表）。

### 3. 出站清洗：`gemini.SanitizeReply`（`internal/gemini/khmer.go`）

- 在 `resultFromValue`（非流式）、`ChatStream` 的每个分片与拼装结果、mock 回复三处接入 ⇒
  **widget / REST / 平台投递三条路都过同一道清洗**。
- 规则：NFC → CRLF 归一 → 删零宽字符（ZWSP/ZWNJ/ZWJ/WJ/BOM/软连字符）与 C0/C1 控制符
  （保留 `\n` `\t`）→ NBSP 转空格 → 高棉数字转 ASCII → 空白整理。
- 与 `rag.NormalizeText` **故意不同**且不能合并：那边把零宽字符当**词边界转成空格**（检索要的），
  出站那样做会把一个高棉语词**劈成两半**；另外 `rag` 依赖 `gemini`，反向引用是环。
- 流式分片只做字符级规则（不 trim、不折叠空行）：分片边界可能正落在词间空白上。

### 首轮跑出来的两个"真问题"，两个都是**测量/注入的错**（2026-09-29 当天查清）

首轮报告写的是"英文提问检索不到"和"事实被漏掉"，两条都被否掉了——否掉它们的是证据，不是更好的猜测。

**1. "检索不到" → 工具在测一个生产不在的世界（冷连接）。**
`rageval`/`jeveval` 都没有预热 embedding：global 端点冷启 ~11.2s，预算只有 5s
（`GEMINI_EMBED_BUDGET_MS`）⇒ 每条 query 的 dense 腿都超时失败、静默降级成只剩词法腿。
同一组 4 条 query（英文/中文/高棉语，同一份高棉语文档）：

| 状态 | dense recall@5 | recall@10 | MRR |
|---|---|---|---|
| 冷（工具原样） | 0/4 | 0/4 | 0.000 |
| 预热后（生产预算 5s） | **3/4** | **4/4** | **0.778** |

**dense 本身能跨语言**（英文问句→高棉语文档命中），生产一直在预热（启动即探针，`warm.go`）。真正成立的是：
**降级态下非高棉语提问只剩词法腿 ⇔ 零召回**，而模型会诚实地说"没有记录"——看起来像模型质量问题，
实际是冷启动 + 截断。

**2. "事实被漏掉" → 注prompt时的截断，不是分块。**
FAQ 的 `ស9`（5 条产品线）在 chunk 2 里的偏移：枚举句起点 695、`EPS-R` 在 876（chunk 共 951），
而单源额度是 800（`RAG_SOURCE_LIMIT_RUNES` 默认值）。模型拿到的是一句"有 5 条"加 4 条，
**且没有任何"此处被截断"的信号**。`lang-en` 同理：注入上下文里 `MOQ` 出现 **0 次**，
模型的"我们没有记录"是**诚实回答**，不是幻觉也不是偷懒。

修法（两处都在本次落地）：

- `gemini.WarmEmbeddings(ctx, budget, tries)`：工具跑之前必须证明 dense 是热的；热不起来就大声报
  **COLD**，并在总结里再报一次（`rageval` 与 `jeveval -mode reply` 两端）。静默降级正是上一轮结论错掉的原因。
- `rag.groundingExcerpt(content, room, extra)`：截断只允许停在**空行块边界**，否则停在**行边界**，
  为此可以**越界**最多 `groundExcerptSlack`=400 字符；两者都不行时退回整行并在**结尾**加
  `[… excerpt ends here; …]`（标记放结尾：第一版放在开头，读起来像"前面缺了东西"）。
  顺序不是拍脑袋：FAQ 的问答之间**只有一个换行**（空行只出现在章节标题前），所以只认空行的规则
  在 1000 字符窗口里什么都找不到，会退化成在"…ប៉ុន្មានប្រភេទ?"的**问号**处切——给模型一个没有答案的问题，
  比原 bug 还差（这一版当天就实测到了）。
- `RAG_SOURCE_LIMIT_RUNES=1200`（写入 `.env-go`）：800 把同一节里的 MOQ 条目切在窗口外，1200 后 24 例全绿。
  代价：每轮约 +100 tokens/源，并把"第 5 个来源"换成"更长的前几个来源"（总预算 `RAG_CONTEXT_BUDGET_RUNES`=4800 不变）。

修完的实测（同一条命令、同一个租户、生产 env）：

| 状态 | 结果 |
|---|---|
| 旧提示词全集 | 21/23 · judge 11.91 |
| 新提示词全集（工具冷）+ 截断未修 | 22/24 · judge 11.71 |
| 预热 + 截断修复 + 1200 | **24/24 · judge 11.83**（无失败项） |

### 还留着的（不是"查得不够"，而是要人或要更大改动）

1. **事实覆盖会随运行波动**（温度 0.7）：同一用例两次运行一次过、一次缺事实 ⇒ 单跑不能当结论，
   要么对"必须永远命中"的事实降温度，要么跑 N 轮看命中率。24 例就是现成的基底。
2. **judge 反复点名两个措辞**：`ភ្នាក់ងារមនុស្ស`（"human agent"）像机翻；以及它更偏好 `បង` 而非产品自己在用的
   `អ្នក`。**只能由母语者定**，别照 judge 改提示词；评测集故意不告诉 judge 房子的用词习惯，免得把分歧抹掉。
3. **英/中长问句的覆盖**仍受 `topK` 与 `diversifyByDoc`（每文档最多 2 chunk）限制：一个文档装下整篇 FAQ，
   两段式提问需要同一文档的第 3 个 chunk 时就装不下。今天靠提高单源额度绕过；真要解决得动 topK/多样性策略，
   并用 `rageval` 量化。

### 温度：管理页那个字段一直是"只写不读"（2026-09-29）

`model_configs.temperature`（迁移 001 默认 0.7）被管理页显示、被 `UPDATE` 写入，
**没有任何代码读它**：`LoadDefaultConfig` 不选这列、`HotReload` 不收它、
`generationConfig` 只发 `maxOutputTokens`。所以控制台显示 0.7，而客户回复这条路上
**从未发送过 temperature**，实际生效的是平台默认值——Gemini 文档：默认 1.0，
且**强烈建议 Gemini 3 保持 1.0，低于 1.0 可能导致 looping 或退化**。
（`ExtractDocumentText` 与 `GenerateFastMax` 里的 `temperature: 0.0` 是刻意的：转写要逐字复现、
判官要可复现，那两处本来就不该动。）

接通后（`Service.temperature *float64` + `SetTemperature` + `generationConfig(maxTokens, temperature)`，
启动与保存各应用一次、NULL 会清除旧值），实测四档 × 3 轮 × 24 例：

| 档位 | 通过（72 例） | judge 均值 | 平均字数 | 循环/截断 | 仍失败 |
|---|---|---|---|---|---|
| unset（接通前线上的实际行为） | 71/72 (98.6%) | 11.74 | 270 | 0 / 0 | `lang-en` ×1 |
| **1.0** | **72/72 (100%)** | 11.75 | 267 | 0 / 0 | — |
| 0.7 | 71/72 (98.6%) | 11.73 | 272 | 0 / 0 | `lang-zh` ×1 |
| 0.3 | 70/72 (97.2%) | 11.72 | 264 | 0 / 0 | `lang-en` ×2 |

结论：**在 [0.3, 1.0] 区间内没有可测量的差别**（残余失败全是 `lang-en`/`lang-zh` 那条
跨语言覆盖用例，属 `topK`/`diversifyByDoc` 的检索问题，与采样无关；judge 差 0.03、
字数差 3 字都在噪声内）；既然厂商明确推荐 Gemini 3 用 1.0、并警告低于 1.0 有退化风险，
**线上把该列钉为 1.0**（服务端已应用，启动日志会打印 `"temperature":"1"`）。
旋钮现在是真话：管理页显示什么，请求就发什么。

**部署时当场抓到的一个坑**：那列的迁移默认值是 0.7，所以"接通"这一步本身就会把线上
从平台默认（1.0）悄悄拖进被劝退的 0.7 区——是启动日志那行 `temperature` 当场照出来的
（第一次启动打印 `"0.7"`）。加日志时并不知道它会立刻抓到东西，这就是为什么"显示真实状态"
的日志值得写。

**两个测量 bug，都是被数据（而不是被推理）抓出来的**：
1. `noKB → FAIL` 的规则对**流程类**用例（转人工）也生效：那条问句本来就不该有 KB 命中，
   于是两次"失败"的回复其实是**要求的那句原话**（含 `នឹងប្រគល់ការសន្ទនានេះទៅឱ្យ`，也命中检测表），
   差一点据此得出"0.3 破坏了指令遵循"的结论。改成按类别判定（`requiresGrounding`），
   并复跑确认 12/12 全 ok。
2. 分析脚本按"状态行"切块，最后一个用例把总结与 rubric 一起吞进来当成失败原因。
   教训：**失败的用例要读，不要数**——两次误判都是因为先数了数。

原始数据在服务器 `/root/khmer-deploy/arms/`（12 份 `-v` 输出）与 `handoff-confirm/`（复跑）。

---

## 十一、功能清单与缺口（2026-10-04 核对）

本节回答“现在能卖什么、还差什么”，每一条都能在代码、路由或迁移里找到落点。核对方式见本节末。

### 结论

产品功能面已经完整可用（多渠道接入、AI 对话、知识库、人工协同、多租户计费字段、平台管理都在）。
缺口不在“功能缺”，而在三层：**① 收不到钱**（无支付/订阅/自助升降级）、**② 收了钱不能翻车**（无自动备份、无预发、无外部监控、合规文本缺口）、**③ 产品化细节**（分类编辑、后端文档搜索、单机可用性）。

### 已实现

#### 1. 渠道接入

| 能力 | 说明 |
|---|---|
| 渠道 | Telegram（bot + OIDC 登录）、LINE、Zalo、Meta（Messenger/Instagram）、WhatsApp（含模板消息）、网站挂件（独立 iframe + `widget-embed.js`） |
| 渠道差异收在数据里 | `internal/platform/capabilities.go` 驱动凭据校验、跳转租户身份去重、断开连接（`Channel` 接口）；api 层与编排层零平台名分支（cac4c68） |
| 运维支撑 | OAuth 会话（`platform_oauth_sessions`）、连接健康（`platform_connection_health`）、投递回执（`platform_delivery_receipts`）、出站队列（`platform_outbox`） |

#### 2. 对话与 AI

| 能力 | 说明 |
|---|---|
| 入站管线 | 21 个命名阶段（load-config … post-delivery），顺序有测试钉住（`TestInboundStageListIsStable`） |
| 消息路由 | Jev 决策中间层：small_talk / 垃圾 / 问句；问句不被 small_talk、垃圾、无 KB 短路，也不因 small_talk 被豁免转人工（c80bcc2） |
| 回复生成 | ReplyGuard 质量告警、确定性出站清理、语义回复缓存、small talk 模板、人格 persona（最具体绑定优先，迁移 067） |
| 语言 | **回复语言跟随客户实际所写**（组件/控制台此前拿界面语言当回复语言：中文提问被整条链路按柬语处理，e520838）；三语文案 km/en/zh |
| 多模态 | 语音转写（Gemini ASR，mime 规范化；渠道语音留言会被计费）、翻译（单条/批量） |
| 内容安全 | 两段 gate（screen-inbound / screen-reply），KeywordStrategy + ModelStrategy；**默认关闭**，开关见 `.env`（6 个键尚未写入 `.env.example`） |
| 成本护栏 | 平台级滚动花费预算门（`usage.Budget`，Google 免费额度分档）；**计量覆盖**：主聊天路径 + 辅助生成调用（摄入编译 / 翻译 / rerank / 分类 / 语音转写 / TTS / 图像描述，经 `gemini.AuxUsageObserver` + `usage.WithUser`）+ **嵌入**（`usage.EstimateCostFor` 按模型选费率，输入计价）。按租户配额门见第 6 组 |

#### 3. 知识库（RAG）

| 能力 | 说明 |
|---|---|
| 摄入 | 文本 / 文件（txt/md/csv/docx/pdf）/ URL；分块 + embedding（AI Studio 或 Vertex 可切，region 可配） |
| 检索 | 向量 + 词法双路（任一路失败保留另一路）、高棉语分词列（迁移 058）、rerank 门控（无密集结果时跳过，不空跑） |
| 衍生能力 | AI 编译摘要（`origin=compiled`，可配来源，`compile_status`）、矛盾检测（`kb_contradictions`）、知识缺口 + 一键草稿（`knowledgeGapDraft`）、文档质量统计（引用/点赞） |
| 试问 | 语义问答 `ragQuery`：答案 + 来源列表（控制台知识库页顶部搜索框） |
| 控制台体验 | 按**分类**分组（原按文件扩展名，对脚本上传的 slug 标题会把全部文档归到一个“其他”组）、AI 编译文件折叠小节（默认收起）、`?q=` 本地过滤、`?doc=` 直达预览（e3e0579 / c98e2be） |

#### 4. 人工协同

| 能力 | 说明 |
|---|---|
| 转人工 | 三条自动来源：客户关键词、AI 回复声明（多语言句式匹配 `ReplyClaimsHandoff`）、Jev 决策；建请求 + 会话转 handoff + 通知店主 |
| 队列 | `human_handoff_requests`（pending/assigned/resolved；每会话只允许一条开放，唯一部分索引），接管/指派/解决/关闭，SLA 策略与违约扫描 |
| 协作 | 内部备注（`message_notes`）、话术库（`canned_responses`）、宏（`macros`）、客服团队（`agent_teams`）、会话指派（`session_assignments`） |

#### 5. 收件箱（统一工作台）

| 能力 | 说明 |
|---|---|
| 列表 | 服务端搜索（客户名/平台 ID/客服/标题/消息内容）、平台筛选、分页、归档、批量 |
| 会话 | 文本/媒体/语音/附件、自动翻译与草稿翻译、回复（文本/媒体/按钮/WhatsApp 模板）、消息反馈 |
| 未读 | 列表小圆点（`LS_LASTSEEN` 时间戳）＋ 记录内「新消息」分隔（`LS_LASTSEENMSG` 消息 id 快照）＋ 铃铛图标（93edb38） |
| AI 辅助 | 客户 360（档案/备注/历史）、AI 分析面板（摘要、话术建议、知识引用）、copilot 建议 |
| 全局搜索 | 顶栏同时搜**会话 + 知识库文档**，分区下拉；Enter 保留原来的 `/inbox?q=` 行为（c98e2be） |

#### 6. 计费与配额

| 能力 | 说明 |
|---|---|
| 数据模型 | `tenant_billing`：plan（free/pro/enterprise）、monthly_message_quota、monthly_doc_quota、messages_used、docs_used、cycle_start/end（30 天自动滚动） |
| 文档配额 | `consumeDocQuota`：单条条件 UPDATE 的原子门禁，三个上传入口都走；enterprise 计量不限量 |
| 消息配额 | `usage.ConsumeMessageQuota`（87534df）：渠道、网站组件、控制台 `/chat` 三入口都计入；超额时渠道/组件回转人工话术并建人工请求（`quota-notice` 标注），控制台返回 402 |
| 对账 | `cmd/billingreconcile`：按租户账期重算计数（默认 dry-run，`-apply` 写入），并列出没有 billing 行的租户 |
| 套餐变更 | 平台管理 `setTenantPlan` → `applyPlan` 一并写入配额（free 500/20、pro 5000/500、enterprise 不限） |
| 租户视图 | `GET /billing` + 控制台计费卡（用量条、账期） |

#### 7. 平台与安全

| 能力 | 说明 |
|---|---|
| 权限 | 租户**所有者** vs 成员的边界：`agent_teams` 成员关系 + `internal/api/tenant_owner.go`（18 条租户级路由用 `tenantAdminOnly`）；平台级操作保持 `platform_admin`（`/admin/models*`、`/platform/*`、全局 RAG 编译开关）。· 曾有的 `roles`/`user_roles` CRUD 接口**没有任何授权读者**，只像访问控制，已删除（迁移 069；两表均为空） |
| 安全 | TOTP 2FA（`user_totp`）、SSO（Google / Telegram OIDC）、API keys、审计日志（`audit_logs`）、数据删除请求（`deletion_requests` + 隐私页/删除状态页） |
| 集成 | Webhook 订阅与投递（`webhook_subscriptions` / `webhook_deliveries`）、定时任务（`scheduled_jobs`，迁移 066） |
| 平台管理台 | 租户列表（用量/配额/套餐/会话数）、平台分析、花费预算视图、改套餐 |

#### 8. 增长与运营

| 能力 | 说明 |
|---|---|
| 营销 | 活动（`marketing_campaigns`）、FA 建议（`faq_suggestions`）、话术与人格管理 |
| 通知 | 站内通知（`notifications`，阅读状态/未读数）+ Telegram 推送（`telegram_notify_settings`，按事件开关、消息节流） |
| 质量 | 回复质量告警（ReplyGuard 落库）、引用/点赞统计、知识缺口沉淀 |

#### 9. 运维与交付

| 能力 | 说明 |
|---|---|
| 运行形态 | systemd 双服务（后端 :8081 / 前端 standalone :3001），nginx + Cloudflare，`/ready` 健康探针（含 db/redis） |
| CI | `.github/workflows/ci.yml`：后端 build/vet/test + 前端构建；pre-commit（gofmt、go vet、迁移镜像、行尾、大文件） |
| SQL 门禁 | `internal/sqlcheck`：把源码里每条 SQL 拿生产 schema PREPARE（只解析、回滚事务）；必须 `SQLCHECK_REQUIRED=1` 否则 skip |
| 部署手册 | `deploy-khmer-ai-cs/SKILL.md`（step 0 版本戳核对、交叉编译、迁移、备份-换入、回滚），含`frontend-backup-*` / `server-go.bak-*` 回滚点 |
| 备份 | **手动** dump（见缺口）；本次运维用 dump 恢复验证过可用性 |

### 未实现 / 缺口

#### P0 —— 不收钱就不可能盈利

| 缺口 | 现状与影响 |
|---|---|
| **嵌入配额耗尽（2026-10-05 起）** | Vertex 的 `gemini-embedding-001` 对项目 `gen-lang-client-0354228918` 返回 **429 RESOURCE_EXHAUSTED**（26 小时内 17 次，最早 10-04 23:13，自 10-05 12:13 起每分钟级）——**向量检索整条腿失效**，自动降级为**纯词法检索**（fail-open，所以回答仍出得来，但高棉语/中文的语义召回已丢），回复缓存也无法写入（`reply cache store skipped: embedding failed`）。修法在 Google 侧（查该项目 embedding 配额/结算/模型启用）；`GEMINI_API_KEY`（AI Studio）在环境里可作退路，但服务是**单一 provider**，整个切回去会把聊天也压到 $10/窗口的 Tier-1 天花板 |
| 自动续费与自助升降级 | 一次性 30 天已跑通（order → capture → webhook 幂等 + `paid_until` 到期降级），但**无订阅自动续费**、无租户自助升降级（仍靠平台控制台手改）；`PAYMENT.CAPTURE.REFUNDED` 被忽略并返回 200，**退款/争议、发票/收据、税费全无** |
| 试用与欠费生命周期 | 无 `trial_ends_at` / `suspended_at`，无到期降级/停服/催缴；注册与 SSO 均自动开通租户并给 free 配额 |
| 按租户成本护栏 | 只有平台级滚动预算门；缺按租户日/月上限与异常突增告警（单租户跑飞会让全体 503） |

#### P1 —— 收了钱不能翻车

| 缺口 | 现状与影响 |
|---|---|
| 自动备份与恢复演练 | 服务器 `crontab` 为空；`/root/db-backups/` 里的 dump 是人工执行的；无定期恢复演练 |
| 预发/灰度 | 无 staging，直接发生产（有回滚点，但无预发验证） |
| 外部监控 | 无外部 uptime 探针；无延迟/错误率阈值告警（已有的是内部 Telegram 告警：预算、配额、渠道） |
| 内容安全默认值 | `SAFETY_ENABLED=0`，且 6 个 `SAFETY_*` 开关未写入 `.env.example`；B2B 签约通常要求默认开 |
| 合规文本 | 已有隐私政策 + 数据删除流程；**缺服务条款 / DPA** |
| Khmer 文案 | 累计新增文案（含套餐/结账/限额/功能项等 24 个 i18n 键）需母语复核——这是目标市场的第一印象 |

#### P2 —— 影响成交与续费的产品化

| 缺口 | 现状与影响 |
|---|---|
| 知识库分类编辑 | 后端 `category` / `tags` 已支持，控制台无输入框 → 42 篇文档全在一个分类下，按分类分组只能显示一组 |
| 文档内容搜索 | 后端 `GET /knowledge` 无 `q` 参数，现为前端本地过滤（几十篇够用；上千篇需后端搜索 + SQL 门禁） |
| 去重 | 有矛盾检测，无重复/近似重复检测 |
| 可用性 | 单机单实例（无温备/异地），且与 `wms.service` 同机（4C/7G） |
| 评测闭环 | `rageval` 语料已随测试租户删除（当前跑不动）；reply-cache 误命中率未量测 |
| 控制台语音提示 | `/chat/voice` 前端默认发 `language=km` 当 ASR 提示，应留空自动判定（与 `TranscribeAudio` 注释的警告相反） |
| RLS 兜底 | 迁移 061 已建策略，但生产代码未设 `app.user_id` GUC → 策略 fail-open，属于未接线脚手架（见第十节） |
| 交付卫生 | 前端构建若在含未提交文件的工作树上执行，产物会带未入库代码——本次用按路径 `git stash` 把他人 WIP 排除在构建之外 |
| 单点 | 平台 Telegram bot token 泄漏影响所有商家 |

#### 技术债 / 低成本项（不影响成交，但记着）

| 项 | 现状 |
|---|---|
| 测试残留 | 生产有 9 个 web 测试会话（含验证用），以及 7 笔未付款的 `payments` 行（`status=created`，不产生费用、不开通）。另 `/root/db-backups/tenant-wanfang-password.txt` 是**明文口令文件**，建议删除 |
| 既有 lint | `inbox/page.tsx` 3 处 `set-state-in-effect` + 1 个未使用变量（既有）；`widget/page.tsx` 同类 3 条（1 处 effect 内 setState + 2 处闭包计数，HEAD 上同样报）；`login/page.tsx` 的 OAuth 回跳 effect 2 处（`setError`/`setLoading`，HEAD 上同样报，加行内 eslint-disable 说明后 0 error）；`knowledge/page.tsx` 1 条 effect 依赖警告（已评估，故意保留：消它要包 `useCallback`，而 React Compiler 会拒绍保留手动 memo） |

### 2026-10-04 本次已上线（按提交）

| 提交 | 内容 |
|---|---|
| `87534df` | 消息配额硬拦截（`usage.ConsumeMessageQuota` 原子门禁；渠道/组件/控制台三入口；超额转人工 + `quota-notice`；DB 故障让事件重试）+ `cmd/billingreconcile` 对账工具 + canned 回复不再被 reply_cache 覆盖 |
| `e520838` | 回复语言跟随客户实际所写（界面语言降为兜底） |
| `cac4c68` | 能力表接管凭据校验与跨租户去重（修掉 WhatsApp 必填项漏校验、LINE/Zalo 误判冲突） |
| `c80bcc2` | 问句不再被 small_talk/junk 短路，也不再因 small_talk 被豁免转人工 |
| `d49cd32` | 全库审计去重 + 实测缺陷修复（ASR mime 规范化、高棉语转接句式、rag rerank 门控等） |
| `c98e2be` | 前端：顶栏搜索同时搜会话与知识库（+ `?q=` / `?doc=`） |
| `e3e0579` | 前端：文档库按分类分组 + AI 编译文件折叠 |
| `93edb38` / `4455b47` | 前端：新消息标记铃铛图标 / AI 头像与客户真实头像 |
| 数据侧 | 计数器对账（docs 0→42、msgs 15→64）；生产库仅 admin 与一个租户；知识库 42 篇 / 602 分块；对账后 admin 由 free 升为 **pro**（500 篇 / 5000 条）—— free 的 20 篇配额已实际超额，不升级会拦住上传。升级路径：平台管理 `PUT /api/v1/platform/tenants/{id}/plan`（走 `applyPlan`，配额一并写入，并落 `audit_logs`） |

### 怎么核对本清单

1. 路由与能力：`grep -oE 'Handle[f]?\\("(GET|POST|PUT|DELETE) /api/v1/[a-z0-9/{}_-]+' internal/api/router.go` 按前缀归类；迁移看 `internal/migrations/migrations/`（当前 67 个）。
2. SQL 类改动：`SQLCHECK_REQUIRED=1 DATABASE_URL=<隧道> go test -count=1 ./internal/sqlcheck/`（对生产 schema PREPARE，未设 DSN 会静默 skip）。
3. 计费数字：`go run ./cmd/billingreconcile`（dry-run），`0 mismatch` 才算计数器与真实行数一致。
4. 服务与版本：`curl -s http://127.0.0.1:8081/ready`、`cat /opt/khmer-ai-cs/frontend/.next/BUILD_ID`。

---

## 十二、收款（PayPal，2026-10-05）

### 流程

```text
控制台 /billing
  → GET  /api/v1/billing/plans         （套餐目录，价格来自环境变量，不在前端包里）
  → POST /api/v1/billing/paypal/order  （服务端建单，价格不由浏览器决定）
  → 浏览器跳 PayPal approve URL
  → 回到 /billing?paypal=return&token=<orderId>
  → POST /api/v1/billing/paypal/capture（服务端捕获 + 校验金额 + 开通）
  ← 同时 POST /api/v1/billing/paypal/webhook（PayPal 直接通知，验签后开通）
```

### 关键设计（都是“钱不能错”的那几条）

| 点 | 做法 |
|---|---|
| **幂等** | `payments` 行从 `created → captured` 只用**一条条件 UPDATE** 完成；前端回调与 webhook 会同时到达，只有把行翻过去的那一个发放 30 天 |
| **金额** | 捕获金额与币种必须与**我们下单时写进行里**的完全一致；不一致就把该行标 `failed` 并拒绍开通（币种精确比较，数值忽略尾零） |
| **归属** | 订单号是能力凭证：别人的订单与“不存在”返回同一个 404 |
| **重定向** | approve 链接**两层**钉死在 PayPal 自家域名（后端按 mode 校验 sandbox/live 主机，前端导航前再校一次 https + 主机白名单）——被污染的后端响应不能把买家送到钓鱼页 |
| **重试** | PayPal 侧用 `PayPal-Request-Id=orderID` 让捕获本身幂等；webhook 开通失败返回 5xx 让 PayPal 重试 |
| **到期** | `tenant_billing.paid_until` 与 30 天用量周期**分开**：续费从已付到期日顺延；到期由 `expirePaidPlans`（10 分钟一扫）降回 free；`paid_until IS NULL`（平台管理员手工授权的套餐）永不被动 |

### 端点

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/v1/billing/plans` | 目录：价格、是否可购买、当前套餐与用量 |
| POST | `/api/v1/billing/paypal/order` | {plan} → {order_id, approve_url, amount, currency} |
| POST | `/api/v1/billing/paypal/capture` | {order_id} → 捕获并开通（幂等） |
| POST | `/api/v1/billing/paypal/webhook` | 公开路由，签名校验是唯一认证 |

### 环境变量

见 `.env.example` 的「收款：PayPal」段：`PAYPAL_CLIENT_ID` / `PAYPAL_CLIENT_SECRET` / `PAYPAL_MODE` /
`PAYPAL_CURRENCY` / `PAYPAL_PRICE_PRO` / `PAYPAL_PRICE_ENTERPRISE` / `PAYPAL_WEBHOOK_ID`。
未配置 = 不出售（安全默认）；只配一半或 mode 拼错 = 拒绝启动。

### 上线与验收（先 sandbox）

1. 跑迁移 068（`payments` + `tenant_billing.paid_until`）→ **应用后立即**跑 SQL 引用门禁
   （`SQLCHECK_REQUIRED=1 … go test ./internal/sqlcheck/`）：新代码引用新列，只有库已迁移才通过——这正是 runbook 里那个强制窗口。
2. 填 sandbox 凭据 + 一个价格（如 `PAYPAL_PRICE_PRO=1.00`）→ 重启 → /billing 出现「用 PayPal 支付」。
3. 用 PayPal sandbox 买家号真实买一次：应跳转、回跳 `?paypal=return`、自动开通、`paid_until` = 今天+30 天、
   `payments.status=captured`。再手动重放一次 capture → 应返回 `already`，`paid_until` **不再增加**。
4. 建 webhook —— **必须用应用凭据调 API 建（应用锚定），不要用 dashboard 的 "Add webhook"**：
   ```bash
   curl -s -u "$PAYPAL_CLIENT_ID:$PAYPAL_CLIENT_SECRET" -H "Content-Type: application/json" \
     -d '{"url":"https://cs.<域名>/api/v1/billing/paypal/webhook","event_types":[{"name":"PAYMENT.CAPTURE.COMPLETED"}]}' \
     https://api-m.sandbox.paypal.com/v1/notifications/webhooks     # live 换成 api-m.paypal.com
   # 返回 {"id":"…"} → 写进 .env-go 的 PAYPAL_WEBHOOK_ID → systemctl restart khmer-ai-cs-go
   ```
   dashboard 建出来的是 **account 锚定**，与应用凭据不同域：事件在 `webhooks-events?webhook_id=…` 里查不到，
   验签**必然** `FAILURE`（2026-10-06 实测，见 §十三 坑位）。live 与 sandbox 是两套注册，切 mode 时 id 要一起换。
5. 只有**真实付款**能证明 webhook 通了：capture 200 之后几秒内 journal 出现
   `/api/v1/billing/paypal/webhook` **200** 且没有 `paypal webhook rejected`。

### sandbox 端到端实测（2026-10-06）

| 验的东西 | 结果 |
|---|---|
| 回跳 capture 主路径 | `POST /billing/paypal/order` 200 → `POST /billing/paypal/capture` 200；`payments.status=captured`，`detail.source="capture"` |
| 套餐到账 | `plan=pro`、消息 5000 / 文档 300（与 `usage/plans.go` 一致）、`paid_until` = 付款日 +30 天、用量周期同步起算 |
| 续费 | 第二笔（21 分钟后）→ `paid_until` 从 11-05 顺延到 **12-05**（`GREATEST(COALESCE(paid_until,NOW()),NOW())`，不从付款日重算）= 两笔共 60 天；**用量窗口不叠加**（`cycle_end` 仍是 11-05） |
| 幂等 | 同一单被 capture 三次只加 30 天；第 2、3 次命中 `status=='captured'` 短路，返回 `{"already":true}`（前端「该笔支付已开通（无需重复操作）」），**PayPal 侧无第二次扣款**（逐单核对：每单 1 笔 capture） |
| webhook 兜底 | 应用锚定的 webhook 在 capture 后 6 秒投递到达、验签 **200**（journal INFO、无 `rejected`） |
| 未验到的 | `POST /v1/notifications/simulate-event` 对四种 payload 形状全回 `MALFORMED_REQUEST_JSON`（别耗时间）；dashboard 的 "Send test" 只能测 account 锚定那条，测不到应用锚定的 |
| 账目 | sandbox 2×29.00 USD（假钱）：订单 `6WK71865HM856823L` / `6R4493701U744974F`，capture `9XA43510JE936922L` / `84S66721EM517713N` |

### 尚未做（下一步）

- **自动续费**：目前是一次性 30 天、到期降级；接 PayPal Billing Plans/订阅可在此之上加，不需改现有幂等结构。
- 退款/争议（`PAYMENT.CAPTURE.REFUNDED` 现在被忽略并返回 200）、发票/收据、税费。
- 试用期（`trial_ends_at`）与催缴；目前“试用”等同于 free。
- **拉模式对账（建议）**：扫 `payments` 里停在 `created` 的单子，主动向 PayPal 查单补开通
  （`cmd/billingreconcile` 是现成骨架）。它不依赖 PayPal 愿不愿意投递，对“买家付完不回跳”比 webhook 更可靠。
- live 切换清单：live 凭据 + live 价格 + **live webhook**（另建一个，连同 `PAYPAL_WEBHOOK_ID` 一起换）。

---

## 十三、生产环境清单（2026-10-05 实测）

> 本节数值均为当天在服务器上实测。**密钥不写进本文件**：所有密钥只存在于
> `/opt/khmer-ai-cs/.env-go`（mode 600，owner `khmerai`），仓库里只记录**变量名**
> （见 `.env.example`；该类文件共 59 个键）。原 `deploy-khmer-ai-cs/SKILL.md` 刻意用
> `<部署服务器IP>` 占位符；按运维要求，这里写明实际值（仓库为私有）。

### 主机

| 项 | 值 |
|---|---|
| 公网 IP | `38.55.192.90` |
| 主机名 | `9a39ap5m3r79t3q` |
| 系统 | Debian GNU/Linux 13 (trixie)，内核 `6.12.107+deb13-cloud-amd64`，x86_64 |
| 规格 | 4 vCPU / 7 GB 内存（可用 ~6 GB）/ 根分区 89 GB（已用 30 GB，36%） |
| 已运行 | 4 周 5 天（至 2026-10-05） |
| SSH | root + **密钥**（`BatchMode=yes` 一次就登）；密码路径见 SKILL.md，**密码不入库** |
| ⚠️ 同机共存 | **`wms.service` 与本站同机，勿动** |

### 域名与入口

| 项 | 值 |
|---|---|
| 控制台/API | `cs.wanfanginsulationmaterial.com`（Cloudflare 橙云代理；源站自签证书 `/etc/nginx/ssl/wms.crt` 与 WMS 共用 → zone SSL 模式必须 **Full**） |
| 媒体 | `media.wanfanginsulationmaterial.com`（R2 公开域名：客户头像、渠道媒体、语音） |
| nginx 站点 | `/etc/nginx/sites-enabled/khmer-ai-cs`：`:80`/`:443`，`server_name cs.wanfanginsulationmaterial.com`；`/api/` → `127.0.0.1:8081`；`/api/v1/realtime/inbox` → WebSocket 升级（3600s）；`/` → `127.0.0.1:3001` |

### 服务与端口

| 单元 | 监听 | 用户 | 备注 |
|---|---|---|---|
| `khmer-ai-cs-go.service` | `127.0.0.1:8081` | `khmerai` | env=`/opt/khmer-ai-cs/.env-go`；`/ready` 报 db/redis + 构建版本 |
| `khmer-ai-cs-web.service` | `127.0.0.1:3001` | `khmerai` | Next 16 standalone，`/usr/bin/node server.js`（node **v20.20.2**） |
| `khmer-ai-cs.service` | — | — | **旧后端，已 disable，留作回滚** |
| `postgresql@17-main.service` | `127.0.0.1:5432` | — | PostgreSQL **17.11**，库 `khmer_ai_cs`，应用用户 `khmerai`；扩展 pgvector 0.8.0 + pg_trgm |
| `redis-server.service` | `127.0.0.1:6379`（+`[::1]`） | — | Redis **8.0.2**，`requirepass` 已开 |
| `nginx.service` | `0.0.0.0:80`/`:443`（+IPv6） | — | 见上 |
| `wms.service` | — | — | **别人的服务，勿动** |

### 目录与文件

| 路径 | 用途 |
|---|---|
| `/opt/khmer-ai-cs/` | 应用根：`server-go`（755）、`frontend/`（standalone 运行目录）、`build/`、`.env-go`（**600**）+ 若干 `.env-go.bak-*` |
| `/opt/khmer-ai-cs/frontend-backup-<ts>/` | 前端回滚点（自动保留最近 3 份） |
| `/opt/khmer-ai-cs/server-go.bak-<ts>` | 后端回滚点（当前 **17 份**，建议定期清理只留 3~5） |
| `/root/khmer-deploy/` | 上传落点 + 运维工具：`mktoken`（签 JWT，**需先 source `.env-go`**）、`jeveval` / `rageval` / `embedcmp` / `vertexprobe`、`audio-samples/`（ASR 回归样本） |
| `/root/db-backups/` | `pg_dump` 产物（人工执行）+ `.env-go` 备份；⚠️ 其中 `tenant-wanfang-password.txt` 是**明文口令**，建议删除 |
| `/etc/nginx/sites-enabled/khmer-ai-cs` | 站点配置（见上） |

### 当前线上版本（2026-10-06）

| 件 | 值 |
|---|---|
| 后端 | `f1ddfdf`（`/ready.version` 与 `strings server-go \| grep vcs.revision` 一致，`vcs.modified=false`；构建机 go1.26.5） |
| 前端 | BUILD_ID `OD03Yj_Wy1cthgfPaecdx`（见 `/opt/khmer-ai-cs/frontend/.next/BUILD_ID`） |
| schema | `schema_migrations` = **71**（最新 `071_team_invite_history`） |
| 收款 | `PAYPAL_MODE=sandbox`、`PAYPAL_PRICE_PRO=29.00` / `_ENTERPRISE=199.00`、`PAYPAL_WEBHOOK_ID=7J2288704D8439049`（**应用锚定**；dashboard 里 account 锚定的 `9E4063638Y8280225` 应删） |
| 套餐 | admin=pro（平台手工开通）、user 10=pro、user 11=pro（sandbox 两笔，`paid_until` **2026-12-05**） |

**2026-10-06 晚发布（`ee9fd00`，8a50de9 → ee9fd00）**：席位管理并入用户管理页
+ 计费回报 `seats_used`/`seats_quota`（§十四）；重复添加客服 409 优先于席位门；
租户权限文案、`UserProfile.is_tenant_owner`、widget `data-api` 死参数、`studioEnv`
测试环境钉 TTL。发布前门禁：`vertexprobe` 必需项 exit 0，生产 schema
`SQLCHECK_REQUIRED=1` 全绿（111s，走已有隧道）。回滚点
`server-go.bak-20261006105524` / `migrate-go.bak-20261006105524` /
`frontend-backup-20261006105535`；验证：`/ready.version=ee9fd00`、域名登录 401、
首页 200 + CSP、live `widget-embed.js` 与仓库逐字节一致。

### 发布流程速查（详见 `deploy-khmer-ai-cs/SKILL.md`）

1. **step 0**：`strings /opt/khmer-ai-cs/server-go | grep vcs.revision` → 必须是**仓库里存在**的提交（否则先别编译，去构建树补齐）
2. 本地门禁：`go build ./... && go vet ./... && go test ./...`；**SQL 引用门禁**：开隧道 →`SQLCHECK_REQUIRED=1 DATABASE_URL=<隧道 DSN> go test -count=1 ./internal/sqlcheck/`（不加 `SQLCHECK_REQUIRED=1` 会静默 skip）
3. 交叉编译 `server-go` + `migrate-go`（`CGO_ENABLED=0 GOOS=linux GOARCH=amd64`，`-ldflags "-X khmer-ai-cs-go/internal/api.Version=<短哈希>"`；服务器上没有 Go）
4. 上传 → **sha256 对账** → 备份 → `systemctl stop khmer-ai-cs-go` → （有迁移才）`migrate-go` → **迁移后立即再跑一次 SQL 门禁** → `install -o khmerai -g khmerai -m 755` → `start` → 查 `/ready`
5. 前端：`NEXT_PUBLIC_API_URL=https://cs.wanfanginsulationmaterial.com/api/v1 npm run build` → 组装 standalone + `.next/static` + `public` → 打 tar → 上传 → 解包哨兵 `test -f server.js` → 换目录 → `chown -R khmerai:khmerai` → `restart khmer-ai-cs-web` → 比对 `.next/BUILD_ID`
6. 回滚：后端 `cp -a server-go.bak-<ts> server-go` + restart（迁移是增量的，回滚二进制不坏库）；前端换回 `frontend-backup-<ts>`

### 实测坑位（省下一个人的半天）

| 症状 | 原因与做法 |
|---|---|
| Windows/Git Bash 发出的请求里中文变成 `?` | **Windows 版 curl 会把非 ASCII 参数转码**。改用文件（`--data-binary @file`）或本地 python 直发 UTF-8 字节；输出用 `sys.stdout.buffer` 或写文件再 `cat`（python 的 stdout 在 Windows 是 cp1252） |
| 本地写的文件“找不到” | MSYS 的 `/tmp` 与 Windows 程序的 `C:\tmp` 不是同一个；**Windows 版 python 里 `/tmp/x` = 当前盘符根**（`D:\tmp\x`），要用 `D:/tmp/x` 或 `process.cwd()` 相对路径。Node/python 是 Windows 程序，bash 的 `ls /tmp` 看不到它们写的文件 |
| 服务端脚本 403 但本地 curl 200 | Cloudflare 会拦 `Python-urllib` 的 UA，**以及来自源站自身 IP 的请求**：脚本里带浏览器 UA + `Origin`，或干脆在服务器上打 `127.0.0.1:8081` |
| `git show origin/main:path` 报 `ambiguous argument` | MSYS 把 `:path` 当路径转换 → `MSYS_NO_PATHCONV=1 git show ...` |
| `pg_dump` 权限错误 | 应用用户 `khmerai` 对遗留表无权限 → 用 **postgres 超级用户** dump |
| 日志查不到旧事件 | journald 只保留有限窗口（2026-10-05 时仅能回溯到 10-01 16:45 左右） |
| 二进制版本戳 `vcs.modified=true` | 构建时工作树里有未提交的**已跟踪**文件（哪怕只是别人改的前端文件）就这样；要干净戳得先确保 `git status` 干净（可用按路径 `git stash` 排除他人 WIP） |
| `mktoken` 打印 `JWT_SECRET missing` | 它需要环境变量 → `set -a; . /opt/khmer-ai-cs/.env-go; set +a` 之后再跑 |
| SQL 引号被 shell 吃掉 | 复杂 SQL 一律 **base64 传参**：`echo <b64> \| base64 -d > /tmp/q.sql && psql -f /tmp/q.sql` |

### 安全卫生（建议尽快做）

1. 删除 `/root/db-backups/tenant-wanfang-password.txt`（明文口令）；同理 `.env-go.bak-*` 建议只留必要的。
2. 本次会话中 Paypal **sandbox** 凭据曾出现在聊天里（只动假钱，风险低）；**换 live 凭据时不要经聊天传递**，直接写入 `.env-go`，并顺手轮换一次 sandbox 密钥。
3. `payments` 表里有 7 笔未付款的 `created` 订单（测试残留），可清。

---

## 十四、席位管理并入用户管理页 + 技术债清单收口（2026-10-06）

本轮提交未部署到生产；线上仍是 §十三 记录的版本。

### 席位为什么搬家

`agent_teams` 的成员既是“客服席位”也是“用户管理页的人员名单”（店主 + 成员），
增长页那张客服卡片与用户管理页表格画的是同一批人，两个入口只会互相护驾。现在
只剩用户管理页一处：

- 顶部席位卡：`seats_used / seats_quota` 进度条；满员时提示升级并链到 `/billing`。
- 表格新增“席位”列：店主标 `admin.seatOwner`，已占席位的显示“移出席位”，其余 `—`。
- 行内 `#user_id` 可复制（添加客服要知道对方的 user_id）。
- 技能的显示名、技能标签跟着成员行显示（路由规则按 `routing_rules.target_skills`
  匹配，删掉那张卡片后这些字段不能跟着消失）。
- 平台管理员不渲染这张卡：它跨租户，且席位写入在服务端是租户级的。

### 后端契约（钱不能错的那一半）

- `GET /billing` 的 `current` 新增 `seats_used` / `seats_quota`（`paidState` →
  `addSeatState`）；`GET /billing/plans` 的 `seats` 复用同一个 `unlimitedPtr`。
- 两边都走 `sqlCountActiveSeats`（`is_active = true`）——与 `checkSeatLimit`
  是同一句 SQL，卡片上写 5，就不能第 5 个人被 402 拦住。
- `seats_quota` 为 `null` = 不限（enterprise 的 `1e9` 哨兵不进浏览器），
  控制台按 `bl.unlimited` 显示“不限”。
- **计数失败写 `null` 不写 0**：把读失败渲染成“0 个在用”，商家会据此做决定。
- 新增 DB 门控测试 `internal/api/billing_seats_test.go`：无 billing 行按 free、
  只数 `is_active`、enterprise 为 `null` 三条断言。

### 顺手收口的四件小事（原 §十一 技术债表里的行）

1. **重复添加客服被席位门抢答 402**：`addTeamAgent` 原先先查席位再查重复，
   free（1 席）第二次添加同一人得到“请升级套餐”；正确的答案是 409“已在团队中”。
   现在先做 `(owner_user_id, agent_user_id)` 的 EXISTS、再进席位门，
   `TestAddTeamAgentDoubleAddIsConflict` 恢复为 409（该测试只在 `DATABASE_URL`
   存在时跑，CI 无库所以长期没暴露）。
2. **`TestReloadGeminiFromDBStudioIsUnchanged` 只在该 shell 没 source `.env` 时通过**：
   `.env` 的 `GEMINI_CACHE_TTL=3600` 把一次 Chat 变成“建缓存 + 生成”两个请求。
   `studioEnv` 统一钉掉这个开关（与 gemini 包自己的缓存测试同款做法）。
3. **文案**：租户级路由的 403 从「需要管理员权限」改成「需要租户管理员权限」
   （`router.go` + `api-errors.ts` 的 km/en 映射同步；同时给新的 409 补了映射）。
4. **类型与死参数**：`UserProfile` 补 `is_tenant_owner`（layout.tsx 的内联窄化删除）；
   `widget-embed.js` 删掉早已不生效的 `data-api`（`/widget` 不再读 `?api=`），
   忘写这个属性的挂件反而能正常起来。

另：工作树里原有一份未提交 WIP——`widget/page.tsx` 与 `lib/api.ts` 的 POST 在 URL
query 上再带一份 token。复核后**撤回**：POST 路径只认 body token
（`widgetChat` / `widgetFeedback` 解出 `req.Token` 后调 `resolveWidgetToken`），
query 这份没有任何读者；把公开 token 多塞进一层访问日志，换不到东西。

### 校验

```bash
# 本地 PG（khmer-ai-local）起在 5440：5433 被 Windows 保留端口挡住，
# 且本地库停在 057，先补 12 个待应用迁移
cd backend-go
set -a; . ./.env; set +a
export DATABASE_URL="${DATABASE_URL/5433/5440}"
go run ./cmd/migrate
SQLCHECK_REQUIRED=1 go test -count=1 ./...   # 全绿，含 DB 门控的席位/团队/凭据测试
```

```bash
cd frontend
npx tsc --noEmit
NEXT_PUBLIC_API_URL=https://cs.wanfanginsulationmaterial.com/api/v1 npm run build
```

注意 `go test ./...` 不带 `DATABASE_URL` 会**静默跳过**那些 DB 门控测试（本轮两个
真问题就是这么漏到今天的），发布前尽量带上本地库或隧道跑一遍。

---

## 十五、客服席位改成邀请链接（`7ec1cf9`，2026-10-06 夜）

### 为什么不是「user_id 太简单」而是走不通 + 可接管

- **走不通**：没有任何界面把 user_id 给到客服本人（profile 只有 username/邮箱），
  而店主的用户列表（`tenantUserScope`）只包含店主自己 + 已入席的成员——新客服
  根本不在列表里。密码注册在生产是关的（`ALLOW_REGISTRATION` 未设 = false），
  账号只来自 Google/Telegram SSO 或平台管理员手工建，所以只有平台管理员能读到
  那个 id，租户自助流程无法完成。
- **可接管**：user_id 是顺序整数，而 `addTeamAgent` 的准入只拒 platform_admin /
  已被认领 / 已有 tenant 状态（billing/渠道/会话/知识库）。刚注册的 SSO 账号这
  些行一个都没有——任何店主猜一个小整数就能把陌生人认领进自己的团队；而
  `agent_teams` 成员关系就是 `userInCallerTenant` 的答案，所以店主随后能改它的
  角色、停用它（还会 bump token_version 当场踢下线），受害者同时因
  `tenantOwnerAllowed = !isAgent` 失去自己账号的所有者身份。

### 现在的形状

| 件 | 行为 |
|---|---|
| 迁移 070 `team_invites` | 只存 token 的 **SHA-256**；链接仅在生成时显示一次（丢了就撤销重建）。待接受的邀请**不占席位** |
| `POST /team/invites` | 租户所有者；可带可选称呼/技能；生成时先查席位，满员直接 402 |
| `GET /team/invites` | 待接受列表（不含链接，只有哈希）；`DELETE /team/invites/{id}` 撤销，按 owner 隔离 |
| `POST /team/invites/accept` | **任何已登录用户**；请求体只有 code，绑定的是调用者自己的 user_id——这就是旧流程缺的那份同意 |
| 事务 | 接受在一个事务里：锁 owner 行 → `checkSeatLimitFrom` → guarded UPDATE 领取邀请 → INSERT；失败全部回滚，所以满员/已在别队都不会烧掉链接 |
| `POST /team/agents` | 收成**平台破窗**（`platform_admin` only）；租户侧已无按 id 认领的路径 |
| `updateUserRole` | 启用/停用账号只允许 platform_admin（租户侧用「移出席位」收回权限）——把「认领后停用」半条链彻底切断 |
| 前端 | 席位卡 = 生成链接/复制（只读框，一次性提示）/待接受列表/撤销；新公开路由 `/join?code=`（未登录把 code 存 sessionStorage，登录页四条成功路径都会回到它）；`#user_id` 复制按钮改为仅平台管理员可见 |
| 测试 | `team_invites_test.go`（DB 门控）：绑定调用者/一次性/满席回滚后可重试/自邀·平台管理员·独立商家·已在他队各拒绝/撤销 owner 隔离；`team_agent_conflict_test.go` 改平台管理员调用并补租户 403 |

### 上线记录（`ee9fd00 → 7ec1cf9`，含迁移 070）

- 门禁：`vertexprobe` 必需项 exit 0；**迁移后**用生产 schema 跑
  `SQLCHECK_REQUIRED=1`（119s 全绿）——本次新表 `team_invites` 只有先迁移
  才存在，所以顺序必须是 stop → migrate → sqlcheck → start。
- 回滚点：`server-go.bak-20261006113023` / `migrate-go.bak-20261006113023` /
  `frontend-backup-20261006113239`（旧二进制不引用新表，迁移是附加式的，可直接回退）。
- 验证：`/ready.version=7ec1cf9`；域名登录 401、`POST /team/invites` 未登录
  401、`/join?code=…` 200、首页 200+CSP；`team_invites` 在生产库存在（0 行）；
  重启后 journal 无 ERROR，`embed keep-warm` 仍在（step 0 那条教训）。

### 上线记录二（`7ec1cf9 → f1ddfdf`，含迁移 071，2026-10-06 夜）

**内容**：邀请链接可设有效期（1h～90d，UI 1/7/30 天）与人数上限（1～100，
不超剩余席位）；新增邀请历史（`GET /team/invites/history` + 控制台表：状态/
指纹/创建/有效期至/已加入 x/y/名单含加入时间）；撤销改为标记留痕；迁移 071
建 `team_invite_uses` 日志表（加入时快照用户名）。

**迁移的向前兼容**：071 **故意不删** `used_by_user_id`/`used_at` —— 旧二进制
（`7ec1cf9`）每次接受都会写这两列，删了就没法二进制回滚。等这个版本足够
老再补一个迁移清理。

- 门禁：`vertexprobe` **红**（详见下）但按负责人决定照常发布；**迁移后**用
  生产 schema 跑 `SQLCHECK_REQUIRED=1` → 121s 全绿。
- 回滚点：`server-go.bak-20261006122446` / `migrate-go.bak-20261006122446` /
  `frontend-backup-20261006122705`。
- 验证：`/ready.version=f1ddfdf`、前端 BUILD_ID `OD03Yj_Wy1cthgfPaecdx`；
  `POST/GET /team/invites` 与 `GET /team/invites/history` 未登录均 401、
  `/join?code=…` 200、首页 200+CSP；生产 schema 里 `max_uses` 在、旧列也在、
  `team_invite_uses` 0 行；重启后 journal 无 ERROR。

#### ⚠️ 未决：`gemini-3.8-flash` 延迟（发布时门禁红的原因）

2026-10-06 12:20Z 左右连测：`vertexprobe` 必需项 `chat: gemini-3.8-flash`
连续 3 次在 45s/60s 预算下 `context deadline exceeded`，把预算拉到 **180s 才通过**；
同一模型的 context-cache 检查能过，其他模型（`gemini-3.5-flash-lite` /
`gemini-3.6-flash` / `gemini-3.5-flash` / `gemini-2.5-*`）与 embedding 全部正常。
它就是生产的主模型+快模型（`model_configs.is_default`），所以真有人来消息，
一轮回复会慢到不可用。当时 `token_usage` 最后一次成功调用在 ~23.5h 前
（同期只有 1 条访客消息）——是**没流量**，不是回复失败；门禁先发现了。
下一步：后台「模型」页把 `is_default` 切到健康档（建议 `gemini-3.6-flash`，
区域仍 `global`，热生效），再跑一次 `vertexprobe` 确认转绿；本文档暂不改
线上模型，需人工决定。
