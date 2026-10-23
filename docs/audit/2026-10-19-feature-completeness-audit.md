# MekongChat 功能完整性审计报告（整合版）

> 审计基线：git `231a755`（2026-10-17，与 origin/main 同步）
> 审计日期：2026-10-19
> 审计方式：4 个领域并行只读审计 + Lead 交叉复核；每条结论附代码证据（file:line）
> 归档说明：本文件整合了总报告（第一部分）与四份领域明细（第二至第五部分），原始分报告生成于 `_audit/incomplete/`，此文件为提交到仓库的整合版本。

> **2026-10-21 更新**：P0-1「密码找回/重置整体缺失」已修复并上线（commit `b9cecd6`）：迁移 087 + 公开 request/confirm 端点 + 平台管理员一次性重置链接 + 前端 `/reset-password` 页；生产已部署（SMTP 未配置时由平台管理员在控制台生成链接）。实现与验证细节见 `docs/DEVELOPMENT.md` 第三十七节。

## 目录

- [第一部分 · 总报告](#part1)（结论摘要、功能地图、P0/P1/P2 清单、dormant、文档核对、修复顺序）
- [第二部分 · 账号/认证/权限/席位/平台管理/计费/安全](#part2)
- [第三部分 · 渠道/入站出站/收件箱/转人工/实时/挂件/Webhook/CSAT/跟进](#part3)
- [第四部分 · AI/模型服务商/人格/知识库 RAG/文档图片/用量配额](#part4)
- [第五部分 · 前端全站 + 配置/迁移/部署/CI/测试](#part5)

---

<a id="part1"></a>

# 第一部分 · 总报告


---

# MekongChat 功能完整性审计报告

- 审计基线：git `231a755`（2026-10-17，与 origin/main 同步；工作树无已跟踪改动）
- 审计日期：2026-10-19
- 审计方式：4 个领域并行只读审计（账号/权限/计费/平台管理、渠道/收件箱/实时、AI/知识库/配额、前端/配置/迁移/部署/CI），Lead 交叉复核关键结论
- 判定口径：**确认不完整 = 有代码证据（file:line）**；**疑似 = 需生产环境/外部审批/运行数据**。docs/DEVELOPMENT.md 只作线索，不作证据。
- 基线验证：`go build ./...` 与 `go vet ./...` 均 exit 0；迁移 86 个（001–086）；三语字典各 1378 键且键集一致；前端 118 条 `/api/v1` 调用与后端 150 条注册路由对账后 0 条指向不存在的端点。

> 本报告只做审计，不改任何代码/配置/数据。整合版包含两部分：第一部分为总报告（结论、分级清单、文档核对、修复顺序），第二至第五部分为四个领域的完整明细（账号/权限/计费、渠道/收件箱、AI/知识库、前端/配置/CI），保留逐条 file:line 证据。

---

## 一、结论摘要

1. **产品主链路完成度高**：多渠道收发（Telegram/Messenger/Instagram/WhatsApp/Widget）、22 阶段入站管线、出站队列重试/回执、收件箱协作、知识库 RAG、AI 三服务商路由、一次性 PayPal 收款、成员权限矩阵、实时 WebSocket、平台管理台均已实现且有测试；前端 24 个页面全部有真实数据源，无 TODO/空实现/写死 mock。

2. **真正的不完整集中在三类**：
   - **①「卖点/设置界面承诺，后端没有消费者」**：出站 Webhook 只有订阅 CRUD 没有投递器；API 密钥有后端三端点却无任何前端入口（同时被列为 Enterprise 套餐卖点）；通知偏好下拉只写不读；宏、分配规则、FAQ 建议、`context_cache_ttl`、`turn_metrics`、近似重复检测结果 `similar_docs` 等只写/只算/只建、无人消费。
   - **②「钱的另一半与账号可恢复性」**：PayPal 只处理 `PAYMENT.CAPTURE.COMPLETED`，退款/争议被 200 忽略；无自动续费、发票/税费、试用、欠费停服；**无密码找回、无 TOTP 恢复码、平台管理员也不能重置**，账号一旦忘记/换机只能改库；数据删除只覆盖 Meta 回调。
   - **③「交付门禁与运维」**：CI 不带数据库 → sqlcheck 与约 20 个 DB 测试静默 skip；前端 `npm run lint` 在 HEAD 上 exit 1 且 CI 不跑 lint、前端 0 自动化测试；自动备份、外部 uptime、预发/staging 仍缺。

3. **另有 3 个需要「尽快修」的实现缺陷**：`DELETE /team/agents/{id}` 无权限门禁（任何席位可移除席位）；把席位 role 改成 `admin` 可整体绕过成员权限矩阵；WhatsApp 手动连接表单缺后端必填的 WABA ID，保存必 400。

4. **文档已明显滞后于代码**：temperature 只写不读、无支付、无重复检测、营销活动、多附件/编辑撤回、分类编辑、free/pro 配额数字等 10+ 条自述缺口已被后续提交修复或推翻；同时文档声称「Webhook 订阅与投递已实现」实际不成立。

### 数量概览

| 分类 | 数量 | 说明 |
|---|---|---|
| 确认不完整项（P0） | 2 | 密码找回/重置；退款/争议 |
| 确认不完整项（P1） | 16 | 见 §三 |
| 确认不完整项（P2） | 28 | 见 §四 |
| 已实现但默认关闭/待外部审批 | 8 | 见 §五，不算未实现 |
| 文档陈述已过时/不成立 | 14 | 见 §六 |
| 未能核实（需生产/外部） | 9 | 见 §九 |

---

## 二、功能地图（模块级状态）

| 模块 | 状态 | 缺口摘要 |
|---|---|---|
| 登录 / 注册 / 2FA / SSO | 基本完整 | 无密码找回、无 TOTP 恢复码、无强制 2FA、无管理员重置；注册开关未接前端 |
| 席位 / 邀请 / 权限矩阵 | 主体完整 | 删席位路由无门禁；role=admin 绕过矩阵 |
| 计费（PayPal） | 一次性收款完整 | 退款/争议、订阅、发票/税费、试用/欠费/拉式对账全缺 |
| 渠道接入 | 参差 | Telegram/Meta/WhatsApp/Widget 完整；LINE/Zalo 媒体半实现；Email/Voice 零实现 |
| 入站管线 / 出站队列 | 完整 | 22 阶段无空实现；队列重试/回执/乱序守卫齐全 |
| 收件箱 | 功能完整 | 无翻页入口（固定 100 条）；平台/客服/标签筛选仅前端本地 |
| 转人工 / SLA / CSAT / 跟进 | 完整 | SLA「解决超时」只记录不通知；跟进默认关 |
| 知识库 / RAG | 主体完整 | 后端无内容搜索 `q`；列表固定 100 条无翻页；分类/标签编辑**已实现**（全屏 Markdown 编辑器） |
| 人格 persona | 半成品 | conversation 作用域永不生效；/ai-test 与网站挂件不应用人格；error_reply/tools 无消费者 |
| AI 服务商 / 模型配置 | 完整 | 辅助能力仅 Gemini（设计如此）；`context_cache_ttl` 死旋钮 |
| 用量 / 配额 / 成本 | 配额完整 | 无按租户成本上限；`POST /rag/query` 不计费不查预算门 |
| 平台管理台 | 完整 | 缺租户密码/2FA 重置、删除租户、删除请求处理 |
| 运营 / 增长组件 | 名存实亡较多 | 宏、分配规则、FAQ 建议无运行时消费者 |
| 通知 | 站内+Telegram 完整 | 通知偏好不生效；无邮件通知 |
| 隐私 / 数据删除 | 半实现 | 仅 Meta 删除回调；无自助注销闭环 |
| 实时 WebSocket | 完整 | — |
| 前端 | 页面完整 | 若干后端能力无入口；lint 红、0 测试 |
| 交付 / 运维 | 门禁偏弱 | CI 无 DB；无自动备份/外部监控/staging |

---

## 三、P0 / P1 不完整清单

### P0-1 密码找回 / 密码重置整体缺失（账号不可恢复）
- **状态**：未实现 → **已修复（2026-10-21，commit `b9cecd6`）**：迁移 087、公开 request/confirm、平台管理员一次性链接、前端 `/reset-password`；生产已上线
- **证据**：公开认证路由只有 login/register/SSO/methods（`backend-go/internal/api/router.go:21-36`）；唯一改密端点要求旧密码（`internal/api/auth_handlers.go:341-371` `!auth.VerifyPassword(req.OldPassword, ...)`）；平台管理台无重置入口（`router.go:330-356`）；全仓无 SMTP 发信实现，邮件重置也没有地基。
- **缺什么**：`/auth/password-reset/request|confirm` + 一次性 token；或至少平台管理员重置端点（带审计）。
- **影响**：管理员创建的商户是密码账号，忘记密码即永久锁死；只能 SSH 改库。
- **工作量**：M（最小可用 S）。

### P0-2 PayPal 退款 / 争议（反向资金流）未实现
- **状态**：未实现
- **证据**：`internal/api/billing_paypal.go:358-361` 只认 `PAYMENT.CAPTURE.COMPLETED`，其余事件返回 `ignored` 200（含 `PAYMENT.CAPTURE.REFUNDED`）；`payments.status` 只有 created→captured/failed（`migrations/068_payments.sql:24-26`），无代码写 `refunded`；收入看板只统计 captured（`platform_ops_handlers.go:197-205`）。
- **缺什么**：退款/争议事件映射 → 写退款状态 + 收回/缩短 `paid_until`；净收入口径。
- **影响**：客户退款后仍保留付费套餐；收入看板只增不减。
- **工作量**：M。

### P1-1 出站 Webhook 订阅只有 CRUD，没有任何投递（已售企业集成）
- **状态**：**已修复（2026-10-23）**：迁移 088 + `internal/webhookout`（事件/扇出）+ `internal/api/webhook_delivery.go`（worker/签名/重试/SSRF）+ 投递历史/重试 API + 企业版面板；首批支持 `session.created / handoff.requested / session.resolved`
- **证据（修复前）**：订阅 CRUD 在 `internal/api/misc2_handlers.go:280,318,327`；迁移 030 建了 `webhook_deliveries`（`migrations/030_enterprise_agent_platform.sql:132`），但全仓库无 `INSERT INTO webhook_deliveries`、无投递 worker、无签名发送（字段 `secret` 存了没人用）；前端有增删面板 `components/admin/enterprise-tab.tsx:124`。
- **影响**：企业客户订阅后永远收不到事件；文档 §十一-7「Webhook 订阅与投递」不成立。
- **工作量**：L（MVP 已交付，其余 4 个事件/订阅更新/测试按钮列在 docs §三十八「明确没做」）。

### P1-2 API 密钥：后端完整、企业版卖点、前端 0 入口
- **状态**：前后端契约不完整
- **证据**：三端点 + Bearer `kcs_` 认证（`router.go:210-212`、`middleware.go:233,321`）；套餐把「API 密钥（系统对接）」列为 Enterprise 特性（`internal/usage/plans.go:51,87-91`、`dict-en.ts:1416`）；`frontend/src` 搜 `api-keys` 为 0。
- **影响**：企业客户为「系统对接」付费却拿不到密钥，只能平台手工造；也没有公开 API 文档。
- **工作量**：S–M。

### P1-3 WhatsApp 手动连接表单缺必填 WABA ID（保存必 400）
- **状态**：前后端契约不完整
- **证据**：能力表要求 `whatsapp_business_account_id`（`internal/platform/capabilities.go:331-338`），后端保存时校验（`internal/api/platform_handlers.go:271`）；前端手动表单只有 page_id/access_token/webhook_secret（`frontend/src/app/platforms/page.tsx:175-181`），保存 payload 也不带该字段（`:877-888`）。
- **影响**：填完点保存得到 400；唯一可用入口只剩 Embedded Signup（依赖 `META_WHATSAPP_EMBEDDED_SIGNUP_CONFIG_ID`，未配置则完全不可连）。
- **工作量**：S。

### P1-4 Meta 订阅字段缺口：评论转私信 / reaction CSAT / 回执在事件层不可达
- **状态**：**已修复（2026-10-23）**：订阅字段加 `message_reactions`/`feed`/`comments`（带 baseline 回退）；`verify` 会为存量连接重新订阅；Messenger/IG delivery/read 回执落 outbox（详见 docs §三十九）
- **证据**：OAuth 只订阅 `messages,messaging_postbacks,message_deliveries,message_reads`（`internal/api/meta_oauth.go:256,262`），没有 `message_reactions`、`feed`、`comments`；但 `internal/platform/webhooks.go:207-237`（评论→私信）、`:270-294`（reaction→CSAT）已实现；Meta 的 delivery/read 事件也没有落库分支（只有 WhatsApp 状态回执 `webhooks.go:1288+`）。
- **影响**：即使 Meta App Review 通过，评论转私信/reaction 评分也收不到事件；Messenger/IG 没有已读/送达回执。
- **工作量**：S（订阅字段+回执分支）/M（评论端到端）。

### P1-5 权限矩阵两个破口：删席位无门禁；role=admin 绕过矩阵
- **状态**：**已修复（2026-10-23）**：`removeTeamAgent` 加 owner 门禁；owner 判定改为 membership 优先（platform admin 除外）；给 active seat 授 `admin` 直接 400（详见 docs §三十九）
- **证据**：
  - `router.go:298` 裸挂 `DELETE /api/v1/team/agents/{id}`，handler 只按租户删除（`misc2_handlers.go:806-816`）；席位的 `Tenant()` 就是店主 id，因此**任一席位可删除本租户任意席位**。同组其他 `/team/*` 都有 `requireOwnerOfTenant`。
  - `admin_handlers.go:584-601` 允许租户管理员把席位 role 设为 `admin`；而 `tenant_owner.go:45-48` 对 `admin` 直接返回 owner 真值 → 该席位可过所有 owner-only 路由（PayPal 下单/捕获、CSV 报表、Telegram 通知路由、租户分析）。
- **影响**：越权操作（删同事席位/接管付款与报表），与文档「团队与角色变更仍店主专属」矛盾。
- **工作量**：S + M。

### P1-6 通知偏好是空开关
- **状态**：**已修复（2026-10-23）**：新增 `internal/notifypref`，两条 `notifyUser` 与 `NotifyNewCustomerMessage` 按 all/escalations/none 过滤（读失败 fail-open；详见 docs §三十九）
- **证据**：设置页 all/escalations/none 三档（`frontend/src/app/settings/page.tsx:117-126`）→ `PUT /auth/preferences` 写 `users.notification_pref`（`auth_handlers.go:451`）；但两条 `notifyUser`（`internal/api/api.go:72-89`、`internal/platform/pipeline.go:1456-1469`）从不读取该列。
- **影响**：选「无」后交接/SLA/负反馈告警照发。
- **工作量**：S。

### P1-7 收入生命周期缺失：自动续费 / 发票收据 / 税费 / 试用 / 欠费停服 / 拉式对账
- **状态**：未实现
- **证据**：`internal/paypal/paypal.go:5-8` 明确只卖一次性 30 天；`billing_paypal.go:43-45` 续费只是 `paid_until+30d`；全仓无 `trial_ends_at`/`suspended_at`；`cmd/billingreconcile` 只核用量不查 PayPal（`cmd/billingreconcile/main.go:1-15`）。
- **影响**：B2B 拿不到发票无法报销；到期静默降 free；付完未回跳的订单依赖 webhook。
- **工作量**：L（发票/税费 M）。

### P1-8 成本护栏：无按租户上限；`POST /rag/query` 主模型调用不计费不查预算门
- **状态**：半实现
- **证据**：`usage.Budget` 对 `token_usage` 做**全局** SUM，无 user_id 条件（`internal/usage/spend.go:337-341`）；所有调用点（渠道/挂件/chat/翻译/平台）共用这一全局门；`knowledge_handlers.go:470-496` 的 RAG 试问直接 `a.serving().Chat(...)`，既不 `usage.Record` 也不查 `usage.Budget`。
- **影响**：单租户跑飞会让全体撞预算门；知识库「试问」可无限消耗平台额度且成本不可见。
- **工作量**：S（试问记账/门）+ M（按租户上限）。

### P1-9 人格（persona）半成品
- **状态**：半实现 / 未接线
- **证据**：声明 session→conversation→global 三级（`internal/persona/persona.go:24`），但唯一生产调用点恒传 `""`（`internal/platform/inbound_stages.go:281` `ForTurn(ctx, ..., t.SessionID, "")`）→ conversation 档永远不生效；人格只经 `ChatAs` 在渠道入站应用（`inbound_stages.go:562`），控制台 `/ai-test → /chat`（`chat_handlers.go:294`）和网站挂件（`widget_handlers.go:578`）都不解析人格；`error_reply` 有 UI+落库但全后端无消费者；`tools` 字段无 UI 无执行器。
- **影响**：商家配置「本次对话人格」无效；AI 测试/挂件表现与实际渠道不一致，测试结果误导。
- **工作量**：M（conversation 档 S；两条生成路径接人格 M）。

### P1-10 列表规模硬上限：收件箱固定 100 条、知识库固定 100/200 条，均无翻页
- **状态**：半实现（后端支持分页，前端无入口）
- **证据**：收件箱前端 `listInbox({..., pageSize: 100})` 且无 page 状态（`frontend/src/app/inbox/page.tsx:353-360`）；后端支持 page/page_size（`inbox_handlers.go:102-168`），平台筛选仅对已加载页本地过滤。知识库 `listKnowledge(1, 100)`（`knowledge/page.tsx:237`）无翻页；顶栏全局搜索 `listKnowledge(1, 200)`（`components/layout.tsx:214`）。
- **影响**：会话/文档超过上限后「消失」（数据仍在、索引仍生效，但控制台看不到/编辑不了）。
- **工作量**：S。

### P1-11 注册开关未接前端（默认部署注册必然 403）
- **状态**：前后端契约不完整
- **证据**：`/auth/methods` 已返回 `allow_registration`（`google_sso.go:326-337`），但登录页不读该字段（`login/page.tsx:118-124`），注册 tab 无条件渲染；默认 `ALLOW_REGISTRATION=false`（`config.go:296`）→ 提交返回 403。
- **影响**：访客看到 Register 填完报错；被邀请客服若未走 SSO 则卡在登录页。
- **工作量**：S。

### P1-12 交付门禁：CI 静默跳过 SQL/DB 测试；前端 lint 红且 CI 不跑 lint、0 测试
- **状态**：关键路径无测试 / 门禁缺口
- **证据**：`.github/workflows/ci.yml:20-22` 只有 build/vet/test，无 postgres、无 `DATABASE_URL`、无 `SQLCHECK_REQUIRED`；`sqlcheck_test.go:109-118` 未设 DSN 即 `t.Skip`，全仓 `t.Skip` 54 处、约 20 个 DB 门控测试文件；前端 CI 只 build（`ci.yml:36-39`），`npm run lint` 实测 exit 1（`frontend/verify-app.js:1`、`verify-theme.js:1` 的 require 错误），`package.json` 无 test script。
- **影响**：SQL 列名/迁移与代码不同步这类历史事故可一路合入；前端回归只能上线后发现。
- **工作量**：M（CI DB）+ S（lint）+ L（前端测试逐步）。

### P1-13 自动备份 / 外部监控 / 预发 staging 仍缺
- **状态**：未实现（交付层）
- **证据**：仓库无备份脚本/定时器（`glob **/*backup*` 只命中 node_modules）；部署手册只提醒人工 `pg_dump`（`deploy-khmer-ai-cs/SKILL.md:287`）；`deploy-khmer-ai-cs/ops/` 只有机内 5 分钟 `/ready` readiness timer，无外部 uptime；单主机部署无 staging。
- **影响**：数据损坏只能靠人工 dump；整机故障外部无告警；坏版本直接上生产。
- **工作量**：备份 S–M、外部监控 S、staging L。

### P1-14 TOTP 无恢复码 / 无强制 2FA / 管理员不能重置
- **状态**：半实现
- **证据**：`user_totp` 只有 secret/enabled（`migrations/030_enterprise_agent_platform.sql:97-103`）；关闭/替换 2FA 必须当前动态码（`misc_handlers.go:128-144`）；无全局/角色级强制（grep `REQUIRE_2FA|FORCE_2FA` = 0）；平台台无重置端点。平台超管（可改套餐/建租户/读全平台花费）可以完全不开 2FA。
- **工作量**：M。

### P1-15 数据删除闭环：只有 Meta 回调，无自助注销/租户删除
- **状态**：半实现
- **证据**：唯一删除能力是 Meta signed_request 回调 + 状态页（`internal/platform/deletion.go:18-114`、`privacy_handlers.go:19-59`、`router.go:49`）；认证面无注销/删除租户端点；隐私政策承诺「删除完成邮件确认」（`privacy/page.tsx:114`）但无邮件实现；平台台无删除请求处理入口。
- **影响**：PDPA/GDPR 类删除权只能邮件+人工 SQL，无法向数据主体证明完成。
- **工作量**：M。

### P1-16 内容安全 gate 默认关闭，6 个 `SAFETY_*` 不在 `.env.example`
- **状态**：默认关闭-only + 配置不完整
- **证据**：`internal/platform/safety.go:176-195` 只有配置了关键词/模型腿才启用；`.env.example` 搜 `SAFETY` = 0；B2B 签约通常要求默认开。
- **工作量**：S（配置与产品决策）。

> 说明：P1-16 在文档 §十一-7/§三十六 已自述，属「已知未收口」；列入 P1 是因为它影响 B2B 签约。

---

## 四、P2 不完整清单（未接线 / 死配置 / 产品化缺口）

| # | 功能 | 状态 | 代码证据 | 缺什么/影响 |
|---|---|---|---|---|
| P2-1 | 宏（macros） | 未接线 | CRUD 在 `internal/api/misc2_handlers.go:187-255`；收件箱前端 grep `macro`=0 | 无执行入口，商家建了多步回复却不能用 |
| P2-2 | 分配规则（routing_rules） | 未接线 | `misc2_handlers.go:109-165` 只有 CRUD；全仓无 `FROM routing_rules` 运行时读取 | VIP→客服规则永远不生效，「假开关」 |
| P2-3 | FAQ 建议（faq_suggestions） | 未接线 | 只有 list/accept/dismiss（`misc2_handlers.go:519-566`），全仓无 `INSERT INTO faq_suggestions` | 表永远为空，Growth 页该面板永远空 |
| P2-4 | 客户身份合并（linked_to） | 未实现 | 迁移 026 加 `customer_profiles.linked_to`（`026_marketing_and_identity.sql:9`），Go 代码 0 引用；customer360 只按单一 platform_user_id 查（`misc2_handlers.go:460-495`） | 跨渠道同一客户无法合并，360 视图不完整 |
| P2-5 | `message_notes` / `session_assignments` | 死表 | 仅 `tenantscope.go:81,114` 表清单与 `deletion.go:336` 注释引用；实际备注/指派走 `sessions` 列 | 结构误导后续开发/外部集成 |
| P2-6 | `turn_metrics` | 只写不读 | 写：`pipeline.go:598`；清理：`cmd/retention/main.go:103`；全仓 0 条 SELECT | 文档「p95 可查询」不成立，只增加写盘成本 |
| P2-7 | `model_configs.context_cache_ttl` | 只写不读 | API 写入/列表回显（`model_config.go:91,275`），管理页可编辑；Gemini 只读 env `GEMINI_CACHE_TTL`（`contextcache.go:81-88`），四个行加载 SQL 都不选该列 | 管理页死旋钮，改了缓存不生效 |
| P2-8 | 近似重复检测 `similar_docs` | 前端未接线 | 后端已算并返回（`rag/service.go:632-667`，阈值 0.88）；前端 grep `similar_docs`=0 | 商家重复上传无提示；后端每次上传白付一次 embedding |
| P2-9 | `edited_at` 可见性 | 只写不读 | 写：`inbox_handlers.go:543`；前端 grep `edited_at`=0 | 编辑过的消息无「已编辑」标记 |
| P2-10 | capability 字段 | 声明未消费 | `SupportsTyping/SupportsButtons/FeedbackButtons/MaxPerInbound` 无生产读取（typing 由 Channel.Typing 全量调用、CSAT 按渠道硬编码） | 「能力表驱动」不彻底，后续加渠道易漂移 |
| P2-11 | 渠道断开 teardown | 半实现 | 只有 Telegram 实现 provider 侧清理（`channel_telegram.go:20`），LINE/Zalo/Meta 嵌入 `noTeardown`（`channel.go:170-172`） | 换租户重连时旧 app 仍可能收事件 |
| P2-12 | SLA 解决超时通知 | 半实现 | 首响违约有通知，解决违约传空 title/body（`internal/api/tasks.go:371-379`，注释自认「record only」） | 客户等解决等到超时永远不提醒 |
| P2-13 | Widget token 生命周期 | 半实现 | 只有 GET/POST/DELETE（`router.go:199-201`），无更新端点；前端只能创建/删除（`widget-admin/page.tsx`） | 颜色/欢迎语/建议问题/域名创建后不可改 |
| P2-14 | Email / Voice(SMS) 渠道 | 未实现（配置/UI 已声明） | 配置在 `config.go:252-261`；唯一消费者是企业页状态卡（`analytics_handlers.go:305-317`、`enterprise-tab.tsx:326-339`）；`net/smtp`=0、`TWILIO_AUTH_TOKEN` 无人读 | 运营看到「已配置」实际零渠道 |
| P2-15 | LINE 渠道媒体 | 半实现 | 出站仅文本（`channel_line.go:19-21`）；入站只处理 text/audio，图片/文件/贴纸静默丢（`webhooks.go:917-928`） | LINE 客户发商品图，AI 收不到 |
| P2-16 | Zalo 入站媒体 | 半实现 | 只认 `user_send_text`/`user_send_image`，其余忽略（`webhooks.go:985-1004`） | 文件/位置/贴纸无记录 |
| P2-17 | 知识库后端搜索 `q` | 未实现 | `knowledge_handlers.go:345-358` 只读 page/page_size；前端本地过滤标题/分类（`knowledge/page.tsx:295`） | 正文关键词搜不到；上千篇时本地过滤只覆盖当前页 |
| P2-18 | 控制台语音输入 | 未实现 | 前端 0 `MediaRecorder/getUserMedia`；后端 ASR 只服务入站（`pipeline.go:793-810`），无 `/chat/voice` | 客服不能录一段语音给客户 |
| P2-19 | `/chat/stream` 与 chat sessions 端点 | 疑似遗留 | 前端 grep `chat/stream|createSession|listSessions`=0；nginx 仍为 SSE 关 buffering | 无人维护的公开面，或需补文档 |
| P2-20 | `/platform-admin` 前端门禁 | 半实现 | 页面无 `isPlatformAdmin` 判断（`app/platform-admin/page.tsx:42-58`），后端接口安全 | 非平台管理员误入看到一堆 403 |
| P2-21 | 套餐功能差异化 | 未实现 | `usage.Plans` 的 `Included` 只渲染卡片（`plans.go:31-33,57-93`）；实际门禁只有 messages/docs/channels/seats（`plan_limits.go`） | Free 用户也能用宣传为 Pro 的 SLA/宏/Webhook/分析 |
| P2-22 | 审计日志覆盖 | 半实现 | 只记录 `auditedPrefixes` 的非 GET（`router.go:504-555`），登录/注册/SSO 明确排除；读取仅 `platformAdminOnly`（`router.go:356`） | 无法回答「谁何时登录/谁改了凭据」；租户看不到本租户审计 |
| P2-23 | 遗留 `PUT /billing/plan` | 半实现/契约漂移 | `misc2_handlers.go:599-635` 硬编码 500/20、5000/500、`1<<62`（int4 溢出）且吞错误；权威 `usage/plans.go:57-93` 是 200/10、5000/300；前端 Growth 页仍在调用 | 平台管理员切套餐可能「成功但没变」；两套配额数字 |
| P2-24 | 计费数字文案不一致 | 文档不符 | 到期通知仍写「20 documents / 500 messages」（`billing_paypal.go:472-473`）；文档 §十一-6 写 free 500/20、pro 5000/500 | 运营/商户看到矛盾额度 |
| P2-25 | `.env.example` 漂移 | 配置不完整 | 僵尸键：`SMTP_PORT/USERNAME/PASSWORD/FROM`、`TWILIO_AUTH_TOKEN`、`FRONTEND_PORT`，以及注释中的 `REPLY_CACHE_*`（功能已在迁移 076 删除）；反向缺 `SAFETY_*`×6、`T2I_*`×5、`META_COMMENT_DM_ENABLED`、`FOLLOWUP_HOURS`、`SCHEDULER_TZ`、`PAYPAL_*` 等 | 按模板部署打不开内容安全/跟进/支付等；运维被误导 |
| P2-26 | SSO signup 默认开 | 默认开放 | `SSO_ALLOW_SIGNUP` 默认 true（`config.go:290`），与 `ALLOW_REGISTRATION=false` 解耦；启用 SSO 的部署可自助开租户 | 运营以为注册已关，实际仍可自助开号耗预算 |
| P2-27 | Widget / 知识库规模与搜索 | 半实现 | 见 P1-10/P2-17 | 同上 |
| P2-28 | 关键路径测试缺口 | 测试缺口 | 支付激活/webhook/到期降级、TOTP 生命周期、`removeTeamAgent`、`createAPIKey`、`setPlan`、Meta 删除签名均无 handler/DB 测试（`api/billing_paypal_test.go` 仅 5 个纯函数测试） | 钱/凭据路径无回归保护 |

---

## 五、已实现但默认关闭 / 依赖外部审批（dormant，非「未实现」）

| 功能 | 开关/依赖 | 代码状态 |
|---|---|---|
| 内容安全 gate（关键词+模型） | `SAFETY_ENABLED` 默认关、需配关键词/模型腿 | 完整（`platform/safety.go` + 测试） |
| 语音回复 TTS | `TTS_ENABLED=false`，还需 R2；注释称无可用预览 TTS 模型 | 完整（`gemini.SynthesizeSpeech` → R2 → 渠道音频） |
| 解决后主动跟进 | `FOLLOWUP_HOURS=0` 默认关 | 完整（`api/followup.go`，只发 ProactiveSend 渠道） |
| 评论转私信 | `META_COMMENT_DM_ENABLED=false` + Meta App Review + **订阅字段缺口（P1-4）** | 代码完整但事件不可达 |
| HUMAN_AGENT 7 天窗口 | Meta App Review；未批时首次失败告警并终态化 | 完整（`policy.go:92-115`、`clients.go:300-306`） |
| WhatsApp Embedded Signup | `META_WHATSAPP_EMBEDDED_SIGNUP_CONFIG_ID` 未配置时不可用 | 完整（`meta_oauth.go:540+`） |
| Google / Telegram OIDC 登录 | `SSO_ENABLED`/`TELEGRAM_LOGIN_ENABLED` 默认关 | 完整（含 PKCE/state/2FA 拒绝） |
| PayPal 付费套餐 | `PAYPAL_*` 未配置则不可购买 | 完整（一次性 30 天） |
| RLS 租户后盾（061） | 无代码设 `app.user_id` GUC，启动日志自述 inert | 脚手架（文档已如实登记） |

---

## 六、文档声明核对（重要：哪些「缺口」已过时、哪些「已实现」不成立）

### 已被代码推翻/修复的文档缺口（不要再当缺口做）
| 文档声明 | 现状 | 证据 |
|---|---|---|
| §十三 温度「只写不读」 | **已修复** | `gemini.go:608-621,1728-1731,1842`；测试钉住 |
| §十一-8 营销活动 `marketing_campaigns` | **已删除** | 迁移 `078_drop_marketing_campaigns.sql`；全仓无读写 |
| §十一 P2 分类编辑缺失 | **已实现**（全屏 Markdown 编辑器支持 title/category/tags 编辑；上传也有分类） | `components/knowledge/markdown-editor.tsx:96-106,150-162`；`knowledge/page.tsx:922-925`；上传 `upload-dialog.tsx:208-211` |
| §十一 P2 无重复/近似重复检测 | **后端已实现，前端未接**（见 P2-8） | `rag/service.go:632-667` |
| §三十二 多附件/编辑撤回「未做」 | **已实现**（§三十三/§三十四；迁移 084/086） | `inbox_handlers.go:456-558`、`channel_extras_test.go:465-541` |
| §十一 P0 无支付 | **一次性 PayPal 已上线**（退款等仍缺） | `billing_paypal.go:222-320` |
| §十一 P0 消息配额不强制 / 挂件不计数 | **已修复** | `inbound_stages.go:794`、`widget_handlers.go:459`、`chat_handlers.go:246,370` |
| §十一-6 free 500/20、pro 5000/500 | **已过时** | 代码 free 200/10、pro 5000/300（`usage/plans.go:57-93`） |
| §六 前端 13 个零消费者导出 | **已过时** | 137 个 api 导出现均有消费者（grep 验证 0 零消费） |
| §十一-2 入站 21 阶段 | **已过时**（现 22） | `inbound_stages.go:116-143` |

### 文档声称已实现、但代码不成立
| 文档声明 | 实际 | 证据 |
|---|---|---|
| §十一-7「Webhook 订阅与投递」 | 只有订阅 CRUD，无投递 | P1-1 |
| §十一-7「API keys 已实现」 | 后端有，前端 0 入口（企业卖点） | P1-2 |
| §十一-1「断开连接（Channel 接口）」 | 仅 Telegram 做 provider 侧清理 | P2-11 |
| §二十九「turn_metrics 可查 p95」 | 只写不读 | P2-6 |
| §十二「退款/争议、发票/税费、试用」尚未做 | **仍成立** | P0-2 / P1-7 |
| §十一 P1「无自动备份、无外部监控、无预发」 | **仍成立** | P1-13 |

---

## 七、建议修复顺序（Top 12）

| 优先级 | 事项 | 建议动作 | 工作量 |
|---|---|---|---|
| 1 | 删席位无门禁 + role=admin 旁路 | 加 `requireOwnerOfTenant`；禁止给席位授 admin（或让 `isTenantOwner` 对 member 否决）；加测试 | S+M |
| 2 | 密码找回/重置 | 平台管理员重置端点（带审计）先行；邮件通道成熟后补自助重置 | S（最小）/M |
| 3 | 退款/争议 | 处理 `PAYMENT.CAPTURE.REFUNDED/DISPUTE`，写退款态并收回权益；看板扣减 | M |
| 4 | 通知偏好 | 两条 `notifyUser` + Telegram 推送前按 `notification_pref` 过滤 | S |
| 5 | 出站 Webhook | 事件枚举 + 投递 worker + 签名 + 重试 + `webhook_deliveries` 落库/读取 | L |
| 6 | API keys 前端 | 设置页加「API 密钥」卡（列表/创建一次性展示/吊销/过期） | S–M |
| 7 | WhatsApp 手动连接 | 补 WABA ID 字段与 payload（或明确仅 Embedded Signup） | S |
| 8 | 成本护栏 | `/rag/query` 记账+预算门；补按租户日/月上限与突增告警 | S+M |
| 9 | 交付门禁 | CI 起 postgres+pgvector 跑 migrate + `SQLCHECK_REQUIRED=1 go test ./...`；修前端 lint 并纳入 CI | M |
| 10 | 分页 | 收件箱与知识库翻页/加载更多（后端已支持） | S |
| 11 | CI/运维止损 | 自动备份 timer + 外部 uptime 探针 + 发布前备份库验证 | S–M |
| 12 | 死配置清理 | 删/接：宏、分配规则、FAQ 建议、linked_to、message_notes、turn_metrics、context_cache_ttl、`.env.example` 僵尸键 | S–M |

> P2 中的「人格 conversation 档、error_reply、Meta 订阅字段、LINE/Zalo 媒体、Email/Voice 声明、plan 功能差异化」建议按商业优先级插入（若近期主推 Enterprise，则订阅字段/Webhook/API keys/人格应提前）。

---

## 八、未能核实的项

1. 生产实际环境变量（`.env-go`）：`ALLOW_REGISTRATION`、`SSO_ALLOW_SIGNUP`、`EMAIL_ENABLED`、`VOICE_ENABLED`、`META_*` 等真实取值；影响 P1-11/P2-26/P2-14 的实际暴露面。
2. PayPal live 模式与历史退款数据；退款损失金额。
3. Meta/WhatsApp 生产订阅与 App Review 审批状态（评论转私信、HUMAN_AGENT、reaction）。
4. Anthropic / DeepSeek 真实端点（无 key，测试均为 stub）。
5. Gemini embedding 配额 429 的生产现状与 RAG 质量影响。
6. 生产库会话/文档量是否已超前端 100/200 上限。
7. 备份/监控/定时任务在服务器上的真实部署状态（仓库无自动化，服务器 crontab/timer 未见）。
8. TTS/语音渠道端到端行为（需 `TTS_ENABLED` + R2 + 真实渠道）。
9. Khmer 文案母语质量（三语键集一致，但译文准确性需母语者复核）。

---

## 九、附录：领域报告与证据索引（见第二至第五部分）

| 报告 | 覆盖 | 位置 |
|---|---|---|
| T1 账号/认证/权限/席位/平台管理/计费/安全 | 19 项发现（A1–A18） | [第二部分](#part2) |
| T2 渠道/入站出站/收件箱/转人工/实时/挂件/Webhook/CSAT/跟进 | 16 项发现 | [第三部分](#part3) |
| T3 AI/模型服务商/人格/知识库 RAG/文档图片/用量配额 | 15 项发现 | [第四部分](#part4) |
| T4 前端全站 + 配置/迁移/部署/CI/测试 | 17 项发现 | [第五部分](#part5) |

**Lead 交叉复核记录（本轮实际跑过）**：
- `go build ./...`、`go vet ./...` 绿；`npm run lint` exit 1（2 error，`frontend/verify-*.js`）。
- 抽查并确认：router 无 reset/forgot 路由；`billing_paypal.go:358` 忽略非 CAPTURE.COMPLETED；`webhook_deliveries` 全仓无 INSERT；`api-keys` 前端 0 命中；WhatsApp 表单缺 WABA ID；Meta `subscribed_fields` 无 reactions/feed/comments；`DELETE /team/agents/{id}` 无门禁；`role=admin` 走 `isTenantOwner` 直接放行；`notification_pref` 仅 auth_handlers 读写；`ForTurn(..., "")`；`ragQuery` 无 Record/Budget；`spend.go` 全局 SUM；收件箱/知识库固定 100；注册 tab 无条件渲染；`turn_metrics`/`similar_docs`/`edited_at` 无读侧；`persona error_reply/tools` 无消费者；`context_cache_ttl` 只写不读。
- **纠正一处领域报告误判**：T3 报告「已存文档分类/标签不可编辑、tags 无 UI」不成立——全屏 Markdown 编辑器支持编辑 title/category/tags（`markdown-editor.tsx:96-106,150-162`，入口 `knowledge/page.tsx:922-925`）；仅知识库预览面板的「快速编辑」只提交 content。本报告按「已实现」处理。

---

*报告生成：2026-10-19，审计基线 231a755。只读审计，未改动代码/配置/数据库。*

---

<a id="part2"></a>

# 第二部分 · 领域明细：账号 / 认证 / 权限 / 席位 / 平台管理 / 计费 / 安全


---

# 账号 / 认证 / 权限 / 席位 / 平台管理 / 计费 / 安全 功能完整性审计

> 审计对象：MekongChat 代码库（Go 后端 `backend-go/` + Next.js 前端 `frontend/` + PostgreSQL/Redis）
> 基线：git HEAD `231a755`（2026-10-17，与 origin/main 同步；其父提交 `a33f793` 为依赖升级 + 密码轮换 + API key/TOTP/WS 吊销 + CSP 收紧）
> 审计性质：**只读**。未改代码/配置，未部署，未连数据库、未跑服务；仅写本报告文件。忽略 `_audit/`、`_research/` 等未跟踪目录。

## 范围与方法

**范围**：`internal/api/` 的 auth_handlers / google_sso / telegram_sso / sso_flow / jwks / member_permissions / tenant_owner / team_invites / privacy_handlers / profile_handlers / platform_admin_handlers / platform_handlers / platform_ops_handlers / billing_paypal / plan_limits / admin_handlers / middleware / router；`internal/auth/`、`internal/security/`、`internal/realtime/`（吊销链路）、`internal/paypal/`、`internal/config/`、`internal/usage/`、`cmd/`；迁移 001–086（重点 030 TOTP、052 认证加固、061 RLS、066 定时任务、068 支付、069 删装饰性 RBAC、070/071/072 邀请与权限矩阵、077 审计索引）；前端 `app/{login,join,settings,billing,platform-admin,admin/users,admin/enterprise,privacy}`、`lib/{api,billing-api,api-errors}`（只核对契约）。

**方法**：
1. 从 `router.go`（579 行）逐条枚举 100+ 路由，按认证 / 租户 / 平台三档归类，核对每条路由的守卫（`tenantAdminOnly`/`permOnly`/`platformAdminOnly`/处理器内自守/裸挂）。
2. 对每个应有功能回到 handler + SQL + 迁移 + 前端调用四方查证；占位符检索（`TODO|FIXME|not implemented|尚未|暂不支持|待实现` 在 `internal/api` **0 命中**，占位类缺陷因此以「缺端点 / 缺消费者 / 恒假开关」形态出现，而非 TODO）。
3. 文档 `docs/DEVELOPMENT.md` 的 §十一(1161)、§十二(1326)、§十四(1510)、§十五(1583)、§十六(1660)、§二十九(2501)、§三十(2511)、§三十二(2669)、§三十五(2734)、§三十六(2780) 逐条比对当前代码。
4. 先列「应有功能清单」再查证：注册/登录/找回与验证/2FA 生命周期/SSO 边界/API key/会话吊销/审计；租户+席位+邀请+权限矩阵；平台管理台各操作；PayPal 收款全生命周期（下单、捕获、退款、续费、发票、税费、试用、欠费、对账）；RLS；删除请求；安全默认值。
5. 证据全部为仓库相对 `file:line` + 摘录；DB 依赖结论若无法静态证明，标「疑似 / 未能核实」。

## 结论摘要

1. **认证主链完整，但账号「不可恢复」**：登录锁定（双闸）、TOTP 校验与重放保护、Google/Telegram OIDC（验签 + state 绑定 + 2FA 账号拒绝走 SSO）、改密吊销 token/API key/WS 都有代码与测试；但**没有密码找回、没有 TOTP 恢复码、平台管理员也没有重置密码或 2FA 的端点**——密码账号一旦忘记/换手机即永久锁死，只能改库（P0）。
2. **PayPal 一次性收款工程化程度高，但钱的另一半没做**：幂等条件 UPDATE、金额币种校验、签名验证、webhook 兜底、10 分钟到期降级都在线；**退款/争议事件被 200 忽略、无发票/税费/自动续费/试用/欠费停服、无拉模式对账**，退款后套餐不会收回、收入看板无法表达退款（P0/P1）。
3. **席位与权限矩阵主体完整，但有 3 个真实破口**：`DELETE /team/agents/{id}` 路由**无任何门禁**（任一成员可删本租户席位）；店主可把席位 role 改成 `admin`，随后该席位整体绕过权限矩阵拿到 owner-only 面；遗留的 `PUT /billing/plan` 与 `usage.Plans` 双写，配额是旧数字且 enterprise `1<<62` 写 int4 溢出被吞（P1）。
4. **「已声明未接线」集中在四处**：`EMAIL_*`/`VOICE_*` 配置与企业页集成状态（无任何邮件/语音实现，隐私政策却承诺「邮件确认删除」）；RLS 061 策略（启动日志自述 inert）；API key 后端完备但前端无入口却被列为 enterprise 卖点；审计日志不含认证事件（P1/P2）。
5. **默认值存在安全/体验缺口**：`SAFETY_*` 默认全关且 `.env.example` 无这 6 个键；`SSO_ALLOW_SIGNUP` 默认 true 与 `ALLOW_REGISTRATION` 默认 false 解耦；登录页**恒显示注册 tab**（后端默认关闭注册）——生产（文档自述注册关闭）用户点注册必然收到 403（P1/P2）。
6. **文档数字已过时**：§十一-6 的 `free 500/20、pro 5000/500` 与代码 `free 200/10、pro 5000/300` 不符，且该旧数字仍被遗留 `setPlan` 和到期通知文案引用（P2）。

## 已核对且完整的功能（简要）

| 功能 | 证据 | 测试 |
|---|---|---|
| 登录：bcrypt、legacy hash 登录时 rehash、失败锁定（5 次/15 分钟 per (username,IP) + 50 次账户级上限）、TOTP 校验 + 每账户 step 重放保护 | auth_handlers.go:73-78, 101-111, 220-231, 131-164 | login_lockout_test.go（2 个单元测试） |
| 注册：`ALLOW_REGISTRATION` + 可选邀请码常量时间比较、用户名/邮箱/密码长度校验、重复冲突 | auth_handlers.go:234-311 | —（无 DB 测试，见 A18） |
| Google/Telegram SSO：JWKS 验 RS256、aud/iss/exp 校验、state 单次 + 浏览器 cookie 绑定、2FA 账号拒绝 SSO、不抢占已有密码账号（防 pre-hijack） | google_sso.go:105-146, 205-262, 264-324；telegram_sso.go:152-180 | —（无 handler 测试） |
| 凭据轮换：改密 token_version+1、吊销全部 API key、发布 `auth.revoke` 关闭 WS；TOTP 变更/账号禁用同样吊销 | auth_handlers.go:376-400；realtime.go:43-44, 81-85；misc_handlers.go:146-166 | api_key_revocation_test.go；realtime e2e_test.go |
| 席位邀请：SHA-256 存 token、7 天默认/90 天上限、1–100 次、撤销留痕、历史、事务内锁 owner+邀请、满席位回滚 | team_invites.go:39-77, 128-187, 314-327, 341-468；迁移 070/071 | team_invites_test.go（6 个 DB 门控） |
| 成员权限矩阵：16 键、默认只读、`permOnly` 路由、租户数据面 `user.Tenant()`、写入落在店主租户 | member_permissions.go:25-101, 166-188；router.go:105-110 等 | member_permissions_test.go（9 个） |
| 平台管理台：租户列表/搜索/auth 过滤、详情+健康、启停、改套餐、创建租户、分析、花费预算、token 看板、审计日志、support/revenue/channels/gaps/todo | platform_admin_handlers.go:23-242, 523-631；platform_ops_handlers.go:122-190, 192-341 | platform_tenants_test.go、platform_ops_test.go、platform_tokens_test.go |
| PayPal 收款：目录价格服务端配置、建单/捕获、金额币种精确校验、webhook 验签（未配 webhook id 拒绝运行）、条件 UPDATE 幂等、30 天顺延、10 分钟到期降级 | billing_paypal.go:32-52, 268-322, 324-437, 439-476；tasks.go:33 | 仅纯函数测试（见 A18） |
| 消息/文档/渠道/席位四类配额门（免费/付费差异） | plan_limits.go:69-123；usage/plans.go:57-93；billing_paypal.go:156-179 | billing_seats_test.go |
| Meta 数据删除回调：signed_request HMAC + algorithm 校验 + issued_at/expires 重放窗口、幂等记录、状态页 | deletion.go:18-114, 360-461；privacy_handlers.go:19-59 | —（无测试，见 A18） |
| 运营支撑：支持消息 Telegram relay 双向（商家→bot→运营，运营 reply→商家并标记已回复） | platform_bot_webhook.go:330-391；platform_ops_handlers.go:20-26 | platform_ops_test.go（读侧一致性） |
| `/billing` 席位结账门禁（10-17 已修）：下单/确认 tenantAdminOnly、成员无购买按钮 | router.go:280-283；billing/page.tsx:53-55, 242-250 | billing_seats_test.go |

## 不完整功能清单

### A1. 密码找回 / 密码重置（含管理员代重置）
- **状态**：未实现
- **代码证据**：
  - 公开认证路由只有登录/注册/SSO/methods：`backend-go/internal/api/router.go:21-36`（`POST /api/v1/auth/login`、`/register`、`/google/*`、`/telegram/*`、`GET /auth/methods`），**无 forgot/reset/verify-email 路由**（对 router.go grep `reset|forgot|verification` → 0 命中）。
  - 唯一的改密端点要求旧密码：`backend-go/internal/api/auth_handlers.go:341-371`（`SELECT password_hash ... !auth.VerifyPassword(req.OldPassword, *hashPtr)` → `原密码错误`）。
  - 平台管理台也无重置入口：`router.go:330-356` 只有 tenants 列表/详情/status/plan/create 与只读运营接口。
  - 仓库内没有任何 SMTP 发送实现：`internal/config/config.go:65-69` 定义 `EmailConfig{Enabled, SMTPHost, InboundDomains}`，唯一消费者是 `analytics_handlers.go:306-312` 的状态展示。
- **缺什么**：`POST /auth/password-reset/request` + token 表/迁移 + 邮件发送（或至少平台管理员 `reset-password` 端点）；邮箱验证（注册只校验格式，`auth_handlers.go:264-266`，从不验证归属）。
- **用户可感知影响**：平台管理员 `POST /platform/tenants` 创建的商户是密码账号（`platform_admin_handlers.go:523-553`），忘记密码后**产品内无任何恢复路径**，只能运维改库；未配置 SSO 时整站不可登录。
- **优先级**：P0　**工作量**：M（邮件通道另计；最小可用 = 平台管理员重置端点 + 一次性 token，S）

### A2. 退款 / 争议处理（PayPal 反向资金流）
- **状态**：未实现
- **代码证据**：
  - webhook 只处理 `PAYMENT.CAPTURE.COMPLETED`，其余（含 `PAYMENT.CAPTURE.REFUNDED`）明确忽略并 200：`backend-go/internal/api/billing_paypal.go:358-361`（`if event.EventType != "PAYMENT.CAPTURE.COMPLETED" { return ... "ignored" }`）。
  - 支付行状态机只有 `created → captured / failed`：`backend-go/migrations/068_payments.sql:24-26`（`-- created → captured, or failed`）；`refunded`/`cancelled` 只出现在测试允许列表 `internal/api/platform_ops_test.go:223`，**无任何代码写入**（后端 grep `refunded` 仅此 1 处，前端 0 处）。
  - 收入看板只汇总 `status = 'captured'`：`platform_ops_handlers.go:197-205`，退款无字段可扣。
- **缺什么**：REFUNDED/DISPUTE 事件映射 → 写 `refunded` + 缩短/撤销 `paid_until`；退款记录与净收入。
- **用户可感知影响**：客户 PayPal 退款后仍保有 paid 套餐；收入看板 gross 只增不减；退款只能改库。
- **优先级**：P0（钱路径正确性）　**工作量**：M

### A3. 订阅/自动续费、发票与税费、试用、欠费停服、拉模式对账
- **状态**：未实现
- **代码证据**：
  - 客户端明确只做一次性 30 天：`backend-go/internal/paypal/paypal.go:5-8`（`It deliberately does not model PayPal Billing Plans / subscriptions yet: the first iteration sells one 30-day period at a time`）。
  - 迁移注释同样定性：`backend-go/migrations/068_payments.sql:1`（`one-time purchase of a 30-day paid plan`）；续费只是 `paid_until + 30 days`：`billing_paypal.go:43-45`。
  - 到期处理只有降级：`billing_paypal.go:442-476`（`applyPlan(...PlanFree)` + 一条 Telegram 通知），无 `suspended_at`/`trial_ends_at`（全仓库 grep 均 0）。
  - 对账工具只核用量计数，不查 PayPal：`cmd/billingreconcile/main.go:1-15`（`recount each tenant's monthly usage`）。
  - 配置无税费/发票 key：`config.go:298-306` 只有 `PAYPAL_PRICE_PRO/ENTERPRISE/CURRENCY`。
- **缺什么**：订阅（或定期提醒续费）、账单历史/收据下载、税率与发票字段、trial 状态、dunning 与停服策略、拉式补单 job。
- **用户可感知影响**：B2B 客户拿不到发票无法报销 → 直接卡签约；到期静默降 free，客户先感知为「AI 突然不答/配额变小」；「付完不回跳」的订单若 webhook 也丢，既未开通也无补偿。
- **优先级**：P1　**工作量**：L（发票/税费 M，订阅 L）

### A4. 邮箱 / 语音（SMS）通道「已声明未接线」
- **状态**：未接线
- **代码证据**：
  - `.env.example:290-302` 以「Enterprise channels/auth」声明 `EMAIL_ENABLED`、`SMTP_HOST/PORT/USERNAME/PASSWORD/FROM`、`EMAIL_INBOUND_DOMAINS`、`VOICE_ENABLED`、`TWILIO_ACCOUNT_SID/AUTH_TOKEN/FROM_NUMBER`。
  - 后端只有一处读取（且只用于展示）：`internal/config/config.go:252-256`、`257-261`；唯一消费者 `analytics_handlers.go:305-317` 返回 `{enabled, configured, inbound_domains}`。
  - `internal/platform` 无任何 email/sms 渠道实现（grep `email` in platform → 0），能力表也没有 email 通道。
  - 前端企业页把它渲染成「Channel integrations」状态卡：`frontend/src/components/admin/enterprise-tab.tsx:326-339`（Email / Voice/SMS / SSO）。
- **缺什么**：要么实现邮件/语音渠道（入站解析 + 出站投递 + 能力表 + 迁移），要么从配置与 UI 移除，避免运营误判可售。
- **用户可感知影响**：商家/运营看到 Email「已配置/已启用」状态，实际既发不出也收不到任何邮件；隐私政策承诺的「邮件确认删除」也无法执行（见 A10）。
- **优先级**：P1　**工作量**：M（移除 S；实现 L）

### A5. TOTP 恢复码、强制 2FA、管理员重置 2FA
- **状态**：半实现
- **代码证据**：
  - `user_totp` 只有 secret/enabled：`backend-go/migrations/030_enterprise_agent_platform.sql:97-103`（`user_id, secret, enabled, verified_at, created_at`），无恢复码列/表。
  - 关闭 2FA 必须提供当前动态码：`internal/api/misc_handlers.go:128-144`（`if !auth.VerifyTOTP(...) { return 验证码错误 }`）；替换验证器需密码 + 当前码（`misc_handlers.go:66-83`）。
  - 登录只在账号**已注册 TOTP** 时要求第二因子，无全局/角色级强制：`auth_handlers.go:120-131`；grep `REQUIRE_2FA|FORCE_2FA|requireTOTP` → 0。
  - 平台管理台无重置 2FA 端点（`router.go:330-356`；TOTP 路由全部 self-service，`router.go:204-207`）。
- **缺什么**：恢复码（生成/哈希存储/一次性消费/展示一次）、超管强制 2FA（至少 `platform_admin`）、平台侧「重置某账号 2FA」破窗端点 + 审计。
- **用户可感知影响**：手机丢失/换机 = 账号永久锁死（与 A1 叠加：连密码也找不回）；跨租户超级管理员（能改套餐、能建租户、能读全平台 token 花费）可以完全不开 2FA。
- **优先级**：P1　**工作量**：M

### A6. `DELETE /team/agents/{id}` 无权限门禁（任何席位可移除席位）
- **状态**：未接线（权限）
- **代码证据**：
  - 路由裸挂，没有 `tenantAdminOnly`/`permOnly`：`backend-go/internal/api/router.go:298`（`authed.Handle("DELETE /api/v1/team/agents/{id}", a.handleDoc(a.removeTeamAgent))`）。
  - 处理器只按租户删除，不检查调用者是否为店主：`internal/api/misc2_handlers.go:806-816`（`DELETE FROM agent_teams WHERE team_id = $1 AND owner_user_id = $2`，`user.Tenant()`）。
  - 席位的 `Tenant()` 就是店主 id：`internal/api/api.go:180-188` + `member_permissions.go:123-136`（`user.TenantID = *ownerID`）。
  - 同组其他 `/team/*` 处理器都有店主门（`listTeam` `misc2_handlers.go:641-648` `requireOwnerOfTenant`；邀请 `team_invites.go:130-132` 等）——只有这个删除没有。路由测试 `tenant_owner_test.go:73` 以「/team/* handlers gate inside」为由把它排除在扫描外，但该前提在此处不成立。
- **缺什么**：`requireOwnerOfTenant`（或 `platformAdminOnly`）门禁 + 测试。
- **用户可感知影响**：任何成员只要枚举 `team_id` 即可把同事（或自己）移出席位，破坏排班与 SLA 归属；文档 §三十 2542 行承诺「团队与角色变更仍店主专属」，与代码不符。
- **优先级**：P1　**工作量**：S

### A7. 席位 role=`admin` 绕过成员权限矩阵
- **状态**：半实现 / 文档不符
- **代码证据**：
  - 店主可把席位用户 role 改为 `admin`：`internal/api/admin_handlers.go:584-601`（`case "user","admin": // Tenant roles — grantable by tenant admins` 后 `UPDATE users SET role = ...`）；前端用户表对每一行（含席位）渲染 role 下拉：`frontend/src/components/admin/admin-pages.tsx:955-962`（含 `admin`/`platform_admin` 选项）。
  - `role=admin` 直接判为租户 owner，**不再看 agent_teams 成员关系**：`internal/api/tenant_owner.go:45-48`（`if isAdmin { return true }`），并被测试固定：`tenant_owner_test.go:23-24`（"admin who is also an agent elsewhere" want true）。
  - `tenantAdminOnly` 走 `isTenantOwner`（`router.go:569-578`），因此该席位可过所有 owner-only 路由：PayPal 下单/捕获（`router.go:282-283`）、Telegram 通知路由（`router.go:92-95`）、CSV 报表（`router.go:193`）、租户分析（`router.go:176-188`）；而 `requireOwnerOfTenant` 只挡 `IsMember`（`member_permissions.go:154-164`），两条判定不一致。
- **缺什么**：禁止把 `admin` 授予 agent_teams 成员（或视为共同所有者并在 UI 明示），或让 `isTenantOwner` 对 member 一票否决（与 AGENTS.md「A seat is not a tenant」/文档 §三十 2540-2543 一致）。
- **用户可感知影响**：店主为「多给点权限」把角色改成 Admin 后，该席位悄悄获得付款、报表、通知路由等 owner-only 能力，权限矩阵形同虚设。
- **优先级**：P1　**工作量**：M

### A8. 套餐双写：遗留 `PUT /billing/plan` 配额过时 + enterprise 溢出静默失败
- **状态**：半实现（遗留旁路）
- **代码证据**：
  - `internal/api/misc2_handlers.go:599-635`：`msgQ, docQ := 500, 20`；`pro → 5000/500`；`enterprise → 1<<62 / 1<<62`；最后 `_, _ = a.DB.Exec(...)` **吞掉错误**后返回 `套餐已更新`。
  - 权威定义在 `internal/usage/plans.go:57-93`：free 200/10、pro 5000/300、enterprise 30000/unlimited；同文件 10-13 行明确写「The previous enterprise value, 1<<62, does not fit in int4」——legacy 端点写的正是这个已知会失败的值（配额列是 int4）。
  - 该端点仍挂在路由上：`router.go:264`；前端仍有消费者：`frontend/src/lib/api.ts:1177-1178` + `components/admin/growth-tab.tsx:229-237`（platform_admin 自用页）。
  - 旧数字还被两处文案引用：到期通知 `billing_paypal.go:472-473`（`20 documents / 500 messages per 30 days`，实际 free 10/200）；文档 `docs/DEVELOPMENT.md:1229`（free 500/20、pro 5000/500）。
- **缺什么**：删除 `setPlan` 或改为调用 `applyPlan`；修正通知文案与文档；加 DB 测试。
- **用户可感知影响**：平台管理员在 Growth 页切 enterprise → 收到成功 toast 但套餐未变（或 free/pro 写进与计费目录不一致的配额）；到期通知的额度与实际不符。
- **优先级**：P1　**工作量**：S

### A9. 注册开关未反映在登录页（默认部署存在必然失败的注册入口）
- **状态**：前后端契约不完整
- **代码证据**：
  - 后端把开关放在公开能力探针里：`internal/api/google_sso.go:326-337`（`"allow_registration": a.Cfg.AllowRegistration`）。
  - 登录页请求了该探针，但只消费 google/telegram：`frontend/src/app/login/page.tsx:118-124`，注册 tab 无条件渲染：`login/page.tsx:193-206` 与 `345-352`。
  - 默认关闭：`internal/config/config.go:296`（`ALLOW_REGISTRATION: envBool("ALLOW_REGISTRATION", false)`）；提交后 403：`auth_handlers.go:235-237`（`注册已关闭，请联系管理员`）。
- **缺什么**：前端读取 `allow_registration`（以及 `google_signup/telegram_signup`）决定是否渲染注册 tab/按钮。
- **用户可感知影响**：默认/生产配置（文档 §十五 1589 行自述「密码注册在生产是关的」）下，访客看到「Register」入口，填完表单得到 403；被邀请的客服若未通过 SSO 建号，同样卡在登录页。
- **优先级**：P1　**工作量**：S
### A10. 商家自助注销 / 删除请求闭环
- **状态**：半实现
- **代码证据**：
  - 唯一删除能力是 Meta 数据删除回调 + 状态页：`internal/platform/deletion.go:18-29`；公开状态路由 `router.go:49`；处理器 `privacy_handlers.go:19-59`。认证面**没有任何注销账号/删除租户端点**（router.go 认证组无 DELETE /account；平台组无 DELETE /platform/tenants）。
  - 隐私政策把商家注销写成人工邮件：`frontend/src/app/privacy/page.tsx:114`（`Delete your account and all associated conversations: email b1783467220@gmail.com ... We complete deletion within 30 days and confirm by email`）——但系统没有邮件发送实现（A4），「confirm by email」无法兑现。
  - Meta 回调对「无法定位主体」的请求按 design 记为 unresolved：`deletion.go:23-29`、`privacy_handlers.go:44-51`（`note = "未找到与该标识关联的记录..."`）。
  - 平台管理台无删除请求列表/处理入口（`router.go:330-356` 无 deletion 相关路由）——闭环只覆盖 Meta 自动化那一段。
- **缺什么**：租户自助注销（或删除申请）端点 + 数据级联/软删策略；平台侧删除请求视图；人工请求的工单化与 30 天计时；邮件确认（依赖 A4）或改为站内状态页。
- **用户可感知影响**：PDPA/GDPR 类「删除权」只能靠邮件 + 人工 SQL，且无法向数据主体证明完成；Meta 之外的渠道客户没有对应删除路径。
- **优先级**：P1　**工作量**：M

### A11. 平台管理台缺少账号处置操作
- **状态**：半实现（只读为主 + 缺关键写操作）
- **代码证据**：`router.go:330-356` 平台面只有 `GET /platform/tenants`、`GET /platform/tenants/{id}`、`PUT .../status`、`PUT .../plan`、`POST /platform/tenants`、analytics/spend/tokens/support/revenue/channels/gaps/retry/todo/audit-logs。前端 `platform-admin/page.tsx:61-79` 的 9 个 tab 全部只读（改套餐/启停除外）；运营面注释明确 read-only：`platform_ops_handlers.go:15-28`（Platform operations surfaces (read-only)）与 `router.go:342-344`（All read-only）。
- **缺什么**：租户密码重置、2FA 重置、租户删除/数据导出、代客排障入口；删除请求列表（A10）。
- **用户可感知影响**：商家忘密码/丢 2FA（A1/A5）时客服无法自救，只能 SSH 改库；无法给退租商户做数据清除。
- **优先级**：P1　**工作量**：M

### A12. API key：后端完整、前端无入口，却被当作 enterprise 卖点
- **状态**：未接线（前端）
- **代码证据**：
  - 后端有完整 CRUD：`router.go:209-212`（list/create/delete）与 `misc_handlers.go:190-282`（TTL 默认/上限 365 天、只存 SHA-256、吊销）。
  - 前端全仓库无任何 `/api-keys` 调用/页面（grep `api-keys|listAPIKeys|createAPIKey` 在 `frontend/src` 仅命中 i18n 文本，无 api 函数；`lib/api.ts` 无相关函数）。设置页只有语言/密码/2FA：`app/settings/page.tsx:102,173,280`。
  - 同时 enterprise 套餐把它作为特性展示：`internal/usage/plans.go:51`（`FeatureIntegrations = "bl.feature.apikeys"`）+ `plans.go:87-91`（仅 enterprise Included）；`dict-en.ts:1416`（`"API keys (system integration)"`）。
- **缺什么**：控制台 API key 管理页（列表/创建一次性展示/吊销/过期显示），或在售卖卡上移除该特性。
- **用户可感知影响**：enterprise 客户看到「含 API 密钥（系统对接）」却找不到入口；只能靠 curl 调未公开端点，且没有使用文档页面。
- **优先级**：P2　**工作量**：S

### A13. 审计日志：不含认证事件、租户不可见
- **状态**：半实现
- **代码证据**：
  - 审计中间件只记录 `auditedPrefixes` 的非 GET 变更，且明确排除登录/注册/SSO：`router.go:504-516`（`The public login/register/SSO endpoints are deliberately excluded: they have no authenticated actor`）；`router.go:524-555` 写入 `action = METHOD path`。
  - 读取仅 `platformAdminOnly`：`router.go:356`；租户侧无审计页（settings 页仅语言/密码/2FA）。
  - `recordLoginFailure` 只写 Redis：`auth_handlers.go:220-231`，不落库；登录成功也无记录。
- **缺什么**：认证事件审计（login success/failure、SSO、2FA、密码/2FA 变更、邀请接受）写入 `audit_logs` 或独立表；租户所有者可查看本租户敏感操作（渠道凭据轮换、成员权限变更已有记录但目前看不到）。
- **用户可感知影响**：无法回答「谁在何时登录 / 谁改了我渠道凭据」；安全事件只能翻 journald。对 B2B 安全问卷（SOC2 类）是硬缺口。
- **优先级**：P2　**工作量**：M

### A14. RLS 租户后盾（迁移 061）未接线
- **状态**：未接线（已知取舍）
- **代码证据**：
  - 迁移建策略/函数：`backend-go/migrations/061_rls_tenant_backstop.sql:71-78`（`app_tenant_id()`、`app_set_tenant()`）、`83,104`（`FORCE ROW LEVEL SECURITY`）。
  - 生产代码不设 GUC，启动日志自述 inert：`backend-go/cmd/server/main.go:53-86`（`Nothing in Go sets app.user_id ... both policies fail open for every role. Treat it as unwired scaffolding`）。
  - 唯一 set 调用在测试：`internal/migrations/rls_backstop_test.go`。
- **缺什么**：把每请求/每事务包进 `SET LOCAL app.user_id`（连接/事务模型改造），或删掉该脚手架以免误判为已生效控制。
- **用户可感知影响**：目前无直接表现（实际控制是 `tenantscope` 静态门禁 + 语句内租户列），但一旦某条漏写 `WHERE user_id` 的 SQL 出现，没有第二道防线；文档 §二十九 2505 已如实登记为「明确没做」。
- **优先级**：P2　**工作量**：L

### A15. 内容安全默认全关、`.env.example` 缺 6 个键
- **状态**：默认关闭 / 配置不完整
- **代码证据**：
  - 默认空关键词 + 模型腿关闭：`internal/platform/safety.go:16-26`（`The keyword list ships EMPTY`、`The model leg is off unless SAFETY_MODEL_ENABLED=1`）；`safety.go:166-186`（`cfg.Keywords = splitTerms(os.Getenv("SAFETY_KEYWORDS"))`、`cfg.ModelEnabled = isTruthy(...)`）；`safety.go:180-186` 缺省按「有配置才开」处理。
  - `.env.example` 中 **0 个 SAFETY_* 键**（grep 无命中）；文档 §十一-7 的 1189 行与 1277 行自述「默认关闭」「6 个键尚未写入 .env.example」。
  - 另有 `SAFETY_JUDGE_BUDGET_MS`（safety.go:44）、`SAFETY_MODEL_MIN`（safety.go:266）同样未入样例。
- **缺什么**：把 6 个键写入 `.env.example` 并给出默认建议；产品层面决定 B2B 签约是否默认开（文档 §十一-7 已列为 P1 缺口）。
- **用户可感知影响**：开箱即用的部署没有任何内容审核；若客户在聊天里发违法/极端内容，AI 会照常回答，商家承担合规风险。
- **优先级**：P2（文档已登记，属已知未收口）　**工作量**：S

### A16. SSO 自动开租户默认开，与「注册关闭」语义冲突
- **状态**：默认开放
- **代码证据**：
  - `SSO_ALLOW_SIGNUP` 默认 true：`internal/config/config.go:290`（`AllowSignup: envBool("SSO_ALLOW_SIGNUP", true)`）；样例同值 `.env.example:316`。
  - 首次 SSO 登录直接 `INSERT INTO users ... role 'user'`（即自动建租户）：`google_sso.go:297-318`、`telegram_sso.go:340-361`；而 `ALLOW_REGISTRATION` 默认 false（`config.go:296`）。
  - SSO 开关默认关（`SSO_ENABLED=false`, `config.go:279`），所以只有「启用 SSO 且未显式关 signup」的部署暴露。
- **缺什么**：部署建议/文案明确「关 SSO signup」；或让 SSO signup 继承 `ALLOW_REGISTRATION`（现有设计是故意解耦：`config.go:89-93` 注释）。
- **用户可感知影响**：运营以为「注册已关闭」，实际上任何 Google/Telegram 账号都能自助开租户、拿 free 配额、消耗平台 embedding/模型预算。
- **优先级**：P2　**工作量**：S

### A17. 计费数字与文案不一致（文档 / 通知 / 错误映射）
- **状态**：文档不符
- **代码证据**：
  - 文档写 `free 500/20、pro 5000/500`：`docs/DEVELOPMENT.md:1229`；代码 `usage/plans.go:57-93` 是 free 200/10、pro 5000/300、enterprise 30000/unlimited。
  - 到期 Telegram 通知写 `20 documents / 500 messages`：`billing_paypal.go:472-473`。
  - 前端错误翻译仍映射旧后端文案「最少 6 位」：`frontend/src/lib/api-errors.ts:132`，后端已改 8 位（`auth_handlers.go:347-349`）；平台建租户 i18n 也写 `≥6`（`dict-en.ts:1231-1232`），前端校验却是 8（`platform-admin/page.tsx:395`）。
- **缺什么**：单一数字来源（文案里的数字从 `usage.Plans` 渲染/生成）；同步 i18n 与 error 映射。
- **用户可感知影响**：运营商/商户看到互相矛盾的套餐额度；6 位密码用户收到未翻译的中文错误；小但直接损伤信任。
- **优先级**：P2　**工作量**：S

### A18. 关键路径无测试（单列）
- **状态**：测试缺口
- **代码证据**（以下 handler 在 `*_test.go` 中 grep **0 命中**）：
  - 支付开通与幂等核心 `activatePayment`、`paypalWebhook`、到期降级 `expirePaidPlans`：无 DB 测试；`billing_paypal_test.go` 只有 5 个纯函数测试（`TestNormalizeAmount`、`TestAmountMatches`、`TestBillingPlanPrice`、`TestPaypalEnabledIsNilSafe`、`TestBillingReturnURLDerivesFromPublicAPIURL`，行 15-97）。「只有一条条件 UPDATE 决定谁发 30 天」这一关键不变量只靠 code review。
  - TOTP 生命周期：`totpSetup/totpVerify/totpDisable` 及登录 2FA gate/重放保护无 handler 测试；只有 `internal/auth/auth_test.go:133-176` 的算法单元测试。
  - `removeTeamAgent`（A6 的破口恰因无测试）、`createAPIKey`（TTL/哈希/吊销）、`setPlan`（A8）同样 0 测试。
  - Meta 数据删除：`deletion.go` 无签名/重放/幂等测试（对 `parseSignedRequest` 也无测试）。
  - 已有正向覆盖：登录锁定、API key 改密吊销、邀请、权限矩阵、平台列表/运营、WS 吊销（见上表），所以问题不是「没测试文化」，而是**钱与凭据的最关键路径恰好缺**。
- **缺什么**：DB 门控测试（`DATABASE_URL` 下）覆盖：捕获/重复捕获不加 30 天、金额不符标 failed、webhook 与 capture 竞态只开一次、到期降级、TOTP 登录 + 重放拒绝、removeTeamAgent 403、setPlan 与 plans 一致、删除回调签名与重放。
- **用户可感知影响**：这些路径的错误是「钱/账号」级别，且现有 CI 不带 DATABASE_URL 时 DB 门控测试会**静默 skip**（文档 §十四 1578-1579 自述两个真问题因此漏网）——功能事实上不可回归验证。
- **优先级**：P2（与 A2/A6/A8 合并做更划算）　**工作量**：M

## 文档声明核对表

| 文档声明 | 结论 | 证据 |
|---|---|---|
| §十一-7（1189/1277）：内容安全默认关闭、6 个 SAFETY 键未入 `.env.example` | **仍成立** | safety.go:16-26,166-186；`.env.example` grep SAFETY = 0 |
| §十一-6（1225-1230）：free 500/20、pro 5000/500；套餐变更走 setTenantPlan→applyPlan | **已过时** | 现免费 200/10、pro 5000/300（usage/plans.go:57-93）；遗留 setPlan 仍写旧数（misc2_handlers.go:623-629） |
| §十一-7（1236）：46 条租户路由用 tenantAdminOnly、权限矩阵只覆盖收件箱/知识库 | **已过时（代码更好）** | 10-17 起 6 个面改为 `permOnly`（router.go:105-110,153-158,189,199-201,221-245,252-260）；§三十 已更新 |
| §十一-7（1237）：TOTP 2FA / SSO / API keys / 审计日志 / 数据删除请求「已实现」 | **部分成立** | TOTP/SSO/审计成立；API key 无前端入口（A12）；删除请求只有 Meta 回调（A10） |
| §十二（1397-1404）：自动续费、退款/争议、发票/收据/税费、试用/欠缴、拉模式对账「尚未做」 | **仍成立** | paypal.go:5-8；billing_paypal.go:358-361（REFUNDED 被忽略）；无 trial/suspend 列；billingreconcile 只核用量（main.go:1-15） |
| §十二（1344-1349）：幂等、金额校验、两层重定向校验、10 分钟到期降级 | **仍成立** | billing_paypal.go:384-437, 56-111；tasks.go:33；billing/page.tsx:29-55 |
| §十四（1520-1537）：席位卡 seats_used/seats_quota、null=不限、计数失败写 null | **仍成立** | billing_paypal.go:156-179；billing_seats_test.go |
| §十五（1601-1611）：邀请链接一次性/有效期/人数/历史/撤销、`POST /team/agents` 收成平台破窗、`updateUserRole` 停用仅平台管理员 | **基本成立，但缺一条** | team_invites.go / 070/071/072 均实现；但 §十五 未覆盖 `DELETE /team/agents/{id}` 仍无门禁（A6） |
| §十六 / §三十（2532-2543）：租户面路由只有 tenantAdminOnly 与 permOnly；团队与角色变更仍店主专属 | **部分成立** | 路由扫描测试钉住门禁（tenant_owner_test.go:60-73），但 A6（无门禁删除）与 A7（role=admin 旁路）破坏该承诺 |
| §二十九（2501-2509）「明确没做」：RLS 061 接线 | **仍成立** | main.go:81-85 自述 inert；061 只有测试调用 `app_set_tenant` |
| §三十二（2669-2676）「明确没做」：相册多图/编辑撤回/Telegram Business | **已部分过时** | 编辑/撤回已在 §三十三（迁移 086）实现；多图/Telegram Business 仍成立（属 T2/T4 域，仅登记） |
| §三十五（2746-2748）：结账 tenantAdminOnly、成员无购买按钮 | **仍成立** | router.go:280-283；billing/page.tsx:53-55, 242-250 |
| §三十六（2825-2830）「明确未修」：RLS、注册枚举 | **仍成立** | RLS 同上；注册冲突文案仍明确（auth_handlers.go:273-276 `用户名或邮箱已存在`） |
| `.env.example`（290-302）「Enterprise channels/auth」声明 Email/Voice | **不成立（未接线）** | 仅 analytics_handlers.go:305-317 展示；无邮件/语音实现（A4） |
| 隐私政策（privacy/page.tsx:114）「删除完成后邮件确认」 | **不成立** | 无 SMTP 发送实现（config.go:252-256 仅状态展示）；删除只有人工邮件 + Meta 回调 |

## 未能核实的项（及原因）

1. **生产实际环境变量**：仓库不含 `/opt/khmer-ai-cs/.env-go`。A9（注册 tab 必然 403）、A16（SSO 自助开租户）以「默认值 + 文档自述生产关闭注册（§十五 1589）」推断；若生产把 `ALLOW_REGISTRATION` 设为 true 或未启用 SSO，实际暴露面需线上核对。
2. **退款/争议的真实发生情况**：需要 PayPal 后台 / 生产 `payments` 数据；代码层结论（事件被忽略、无退款写入）已确认，但「当前有多少退款未收回」无法查。
3. **人工密码重置/2FA 重置是否另有仓库外运维脚本**：`cmd/` 下无相关工具（rotatekey 只轮换加密密钥，billingreconcile 只核用量）；若运维用一次性 SQL，则属流程补偿而非产品能力，报告按「产品内无路径」定性。
4. **RLS 生产数据库角色**：启动日志可确定策略 inert；生产是否为 superuser/BYPASSRLS 需连库确认（不影响「未接线」结论）。
5. **Redis 缺失时 TOTP 重放保护的降级行为**：登录代码用 `if a.Redis != nil` 守卫（auth_handlers.go:154），若 Redis 故障则该守卫被跳过、同一验证码可重放。生产 Redis 是硬依赖（限流/就绪探针），故标「疑似」而非确认缺陷。
6. **API key 在生产的使用面**：无法查库统计是否已有租户通过 curl 使用；前端无入口、无文档是代码事实。
7. **审计日志保留/清理策略**：未发现针对 `audit_logs` 的 retention job，但生产数据量未知，是否构成问题需运营判断；未计入不完整清单。
8. **平台管理台对 deletion_requests 的人工处理**：代码无入口；是否通过 SSH/SQL 处理属运维流程，未能核实。

---

**审计执行说明**：本报告只读完成，未运行服务、未连接数据库、未改任何代码或配置；行号基于 HEAD `231a755`。本报告已整合进本文件第二部分。

---

<a id="part3"></a>

# 第三部分 · 领域明细：渠道 / 入站出站 / 收件箱 / 转人工 / 实时 / 挂件 / Webhook / CSAT / 跟进


---

# 渠道接入 / 入站管线 / 出站 / 收件箱 / 转人工 / 实时 / 挂件 / Webhook / CSAT / 跟进 —— 功能完整性审计

## 范围与方法

- 代码基线：HEAD `231a755`（工作树只读，未改动任何代码/配置）。
- 后端：`backend-go/internal/platform/`（capabilities、channel*、inbound_stages、webhooks、webhook_routes、pipeline 出站、policy、verify）、`internal/realtime/`、`internal/api/`（inbox_handlers、chat_handlers、widget_handlers、meta_oauth、followup、tasks、misc2/misc3）、`internal/scheduler/`、迁移 `backend-go/internal/migrations/migrations/`。
- 前端（只核对契约与入口）：`frontend/src/app/{inbox,widget,widget-admin,platforms,handoff-requests,help}/`、`frontend/src/lib/api.ts`、`frontend/src/components/admin/enterprise-tab.tsx`、`frontend/public/widget-embed.js`。
- 方法：先按产品应有功能列清单（渠道×能力、入站 22 阶段、出站队列、收件箱交互、转人工/SLA/CSAT/跟进、实时、挂件、webhook、配置项/迁移表），再逐项在 路由 → handler/实现 → 迁移 → 测试 → 前端 调用链中查证；对 `capabilities.go` 每个声明字段做“生产者/消费者”双向 grep；对 `.env.example` / config 每个渠道开关反查是否有生产消费者。文档（docs/DEVELOPMENT.md）只作线索，所有结论以代码为准。
- 判定口径：**确认不完整**＝有代码证据；**疑似**＝需生产环境/外部审批/运行数据才能判定。

## 结论摘要（3-6 条）

1. **出站 Webhook 是一个只写不发的“企业集成”**：订阅可 CRUD、UI 可增删，迁移也建了 `webhook_deliveries`，但全仓库没有任何代码写入投递记录或向订阅 URL 发请求 —— 文档 §十一-7“Webhook 订阅与投递”名不副实（P1，未接线）。
2. **WhatsApp 手动连接路径被自己的必填校验堵死**：能力表要求 `whatsapp_business_account_id`，后端也会校验，但控制台手动连接表单没有该字段、保存请求体也不携带它，PUT 必然 400；唯一可用入口是 Embedded Signup（依赖环境变量，未配置时彻底不可连）（P1，契约不完整）。
3. **Meta 的订阅字段与代码解析对不上**：OAuth 只订阅 `messages,messaging_postbacks,message_deliveries,message_reads`，因此代码里已写好的「评论转私信」「reaction→CSAT」「delivered/read 回执」三条路径在生产事件层面不可达（P1/P2，已声明未接线）。
4. **收件箱列表只有“最新 100 条”**：后端支持 page/page_size，前端固定取 100 且没有翻页/加载更多入口；平台筛选也只在前端对已加载页做本地过滤（文档称“服务端搜索…平台筛选、分页”）（P2，半实现）。同时 `macros`、`routing_rules` 两组“运营设置”只有 CRUD、无任何运行时消费者（P2，已声明未接线）。
5. **渠道实现参差**：Telegram / Meta 家族 / WhatsApp 收发较完整；LINE 出站仅文本、入站图片/文件/贴纸静默丢弃；Zalo 入站仅文本/图片；Email 与 Voice(SMS) 只有 env + 状态卡，零渠道代码（P2，半实现/未实现）。
6. 默认关闭类功能（follow-up、TTS 语音回复、评论转私信）经核对是**已实现但默认关**，其中 HUMAN_AGENT、评论转私信还叠加 Meta App Review 外部审批；这不等于“未实现”，但上线时会被当成缺口。

## 已核对且完整的功能（简要）

- **Telegram**：文本分块+HTML 子集（`markdown_html.go`）、图片/音频/视频/文件/相册（`channel_telegram.go`、`channel_extras_test.go:465`）、caption 入站、贴纸/位置/名片转文本（`webhooks.go:821`）、按钮选择回传、CSAT 内联键盘与旧键盘降级（`pipeline.go:2088`）、回复引用（`reply_parameters`）、编辑/撤回（`channel.go:96-132` + `inbox_handlers.go:456-558`）、webhook secret 校验与自动注册（`verify.go:127-161`）、断开时删除 webhook（`channel_telegram.go:20`）。
- **Meta 家族（Messenger/IG/WhatsApp）**：统一发送（文本/图片/视频/文件/按钮/模板/多附件）、caption 规则、Messenger mark_seen+typing、CSAT quick replies、HUMAN_AGENT 打标与“未审批”终态分类（`policy.go:92-115`、`clients.go:300-306`）、24h 窗口与模板豁免（`policy.go:43-86`）、入站 media/reaction/postback/quick reply/referral 日志（`webhooks.go:299-385`）、WhatsApp 模板列表与发送（`analytics_handlers.go:537`、`clients.go:432,521`）、Meta OAuth 选页订阅、WhatsApp Embedded Signup 订阅（`meta_oauth.go:255-268,598`）、WhatsApp 状态回执与延迟回执重放（`webhooks.go:1076-1082,1237-1330`、`pipeline.go:2019-2065`）。
- **LINE / Zalo 鉴权与自检**：`LineVerifySignature` / `ZaloVerifySignature` 签名门禁、连接身份采纳与绑定、连接自检（`verify.go:190-239`）；LINE webhook 自动注册+官方自测、Zalo OA 校验。
- **入站管线**：22 个命名阶段顺序固定且有测试（`inbound_stages.go:116-143`、`inbound_stages_test.go:124`），逐段读过无空实现/无 TODO；转人工三来源（关键词 `inbound_stages.go:347`、Jev 路由 `441`、AI 声明转人工 `674/898`）均接线；配额耗尽、媒体不可读均落人工请求；会话重开/释放转人工/升级门/阶段计时齐全。
- **出站**：`platform_outbox` 唯一约束入队（`pipeline.go:1824-1836`）、按会话队头串行防乱序（`pipeline.go:1880-1890`）、失败退避重试、终态失败首告警（`pipeline.go:1941-1962`）、发送成功但写回失败二次尝试+告警（`1973-2004`）、已离开 active 的模型回复自动取消（`1906-1930`）；收件箱/平台页可手工重试入站与投递（`router.go:318-319`）。
- **收件箱**：服务端搜索（跨标题/平台 ID/客户名/客服/消息内容，count 与 page 同谓词，`inbox_handlers.go:141-168`）、状态/归档筛选、指派/接管/解决/关闭/重开、标签、内部备注（`sessions.internal_notes`）、会话摘要、附件单/多条、引用回复、编辑/撤回（仅 Telegram）、CSAT 徽章、投递状态、批量关闭/批量指派、自动翻译（前端批量+缓存）、内部备注、快捷回复（canned）、客户 360、copilot、未读/轮询+WS。
- **实时**：WebSocket 子协议鉴权 + origin 白名单 + token_version 吊销 + `auth.revoke` 强制断连（`realtime.go:81-94,205-232`）、Redis 跨实例 fan-out、前端指数退避重连/隐藏页恢复/轮询降级（`frontend/src/lib/realtime.tsx:45-153`）。
- **挂件**：token 生成/吊销、origin 白名单（文档化其非强边界）、每 IP/每 token 限流、SSE 首轮 + 6s 轮询客服回复、反馈接口、iframe 页面与 `widget-embed.js`、主题色/欢迎语/建议问题（`widget_handlers.go:187-305`、`widget/page.tsx:257-272`）。
- **SLA / 跟进**：首响违约扫描（60s，含营业时间判定、每租户每类型去重、通知）（`tasks.go:26-28,272-381`）；解决后主动跟进默认关、开启后事务内落 chat_messages+outbox+followup_sent_at、只发 ProactiveSend 渠道（`followup.go:56-150`）。
- **CSAT**：Telegram 内联键盘 + Meta quick reply + Messenger reaction 三入口共用 `recordMessageFeedback`，👎 建 `negative_feedback` 人工请求（`webhooks.go:622-643`）；挂件 feedback 端点；分析页 avg CSAT。

## 不完整功能清单

> 状态取值：未实现｜半实现｜未接线｜默认关闭-only｜文档不符。P0=阻断成交/数据错误；P1=客户或商家可感知的缺失；P2=产品化/技术债。

### 1. 出站 Webhook 订阅只有 CRUD、没有任何投递
- **状态**：未接线
- **证据**：
  - 迁移建表：`backend-go/internal/migrations/migrations/030_enterprise_agent_platform.sql:122` `CREATE TABLE ... webhook_subscriptions`、`:132` `CREATE TABLE ... webhook_deliveries`；
  - 后端只有读写订阅本身：`backend-go/internal/api/misc2_handlers.go:280`（list）、`:318`（insert）、`:327`（delete）；
  - 全仓库 grep `webhook_deliveries` 仅命中迁移与 `internal/tenantscope/tenantscope.go:115` 的表清单，**没有任何 INSERT / HTTP 投递器 / worker**；
  - 前端 `frontend/src/components/admin/enterprise-tab.tsx:124` 起有 Webhooks 增删面板，`frontend/src/lib/api.ts:1715-1723` 仅 list/create/delete。
- **具体缺什么**：事件发射（哪个业务动作触发）、投递 worker + 重试/退避、签名（secret 已存但未使用）、失败记录与后台查看（`webhook_deliveries` 无读接口）。
- **用户可感知影响**：企业客户按文档接入 webhook 后永远收不到任何事件；控制台却显示订阅成功。这是“看起来有、实际没有”的集成承诺。
- **优先级**：P1；**工作量**：L（事件枚举+投递器+重试+测试，>3d）。

### 2. WhatsApp 手动连接表单缺少后端必填的 WABA ID
- **状态**：前后端契约不完整（未实现路径被 UI 暴露）
- **证据**：
  - 能力表要求：`backend-go/internal/platform/capabilities.go:331-338`，`{Key: "whatsapp_business_account_id", ... Required: true}`；
  - 后端保存时按能力表校验：`backend-go/internal/api/platform_handlers.go:271` `if msg := caps.MissingRequiredCredential(...); msg != "" { return ErrBadRequest(msg) }`；
  - 前端 WhatsApp 手动连接字段只有三项：`frontend/src/app/platforms/page.tsx:175-181`（`page_id`（Phone Number ID）、`access_token`、`webhook_secret`）；
  - 保存请求体同样不带该字段：`frontend/src/app/platforms/page.tsx:877-888`（config_id/platform/access_token/page_id/instagram_business_id/bot_token/webhook_secret/is_active）。
- **具体缺什么**：补 WABA ID 输入并写入 payload；或把该字段声明为“Embedded Signup only”并在手动表单处禁用/说明。
- **用户可感知影响**：商家在“WhatsApp → 添加账号”填完三项点保存，得到 400 “该平台需要配置 whatsapp_business_account_id”。真实可用路径只剩 Embedded Signup（`meta_oauth.go:540-546` 需 `META_WHATSAPP_EMBEDDED_SIGNUP_CONFIG_ID`，未配置时 API 返回不可用）；此时 WhatsApp 渠道在控制台完全无法接入。
- **优先级**：P1；**工作量**：S（<1d，前端补字段+文案+一个契约测试）。

### 3. Meta 订阅字段缺三项 → 评论转私信 / reaction 评分 / 已读回执在生产不可达
- **状态**：未接线（代码已写，事件永远不来）
- **证据**：
  - 订阅字段：`backend-go/internal/api/meta_oauth.go:256` `{"subscribed_fields","messages,messaging_postbacks,message_deliveries,message_reads"}`；IG 分支 `:262` 同值。**没有** `message_reactions`、`feed`、`comments`；
  - 代码里确实写好了消费端：`internal/platform/webhooks.go:207-237` `handleMetaComment`（评论→私信）、`:270-294` `handleMetaReaction`（👍/👎→CSAT）、`webhooks.go:98-162` 里 `changes` 分支只处理 comment；
  - 但 `message_deliveries/message_reads` 订阅后，`handleMetaMessaging`（`webhooks.go:299-385`）只识别 is_echo/reaction/postback/message，**没有 delivery/read 事件分支**，也没有任何 `provider_status` 写入（WhatsApp 之外）。
- **具体缺什么**：(a) 把 `message_reactions`、`feed`、`comments` 加入 subscribed_fields（或文档明确要求手工后台订阅）；(b) 为 Meta 的 delivery/read 事件实现与 WhatsApp 相同的 outbox 回执落库；(c) 评论转私信的人工审核/权限矩阵开关仍缺（现仅 env）。
- **用户可感知影响**：Messenger/IG 会话里 AI 回复没有已读/送达回执；客户对 AI 回复点表情不会沉淀 CSAT；开启 `META_COMMENT_DM_ENABLED` 也不会收到评论事件（文档 §三十二-三 归因于 App Review，但即使审通过，字段没订阅同样不投递）。
- **优先级**：P1（评论转私信）/P2（回执、reaction）；**工作量**：S（字段+回执分支）/M（评论 DM 端到端与测试）。

### 4. 收件箱列表无翻页入口；平台筛选仅前端
- **状态**：半实现（后端有、前端无入口）；文档不符
- **证据**：
  - 后端 `internal/api/inbox_handlers.go:102-107` 解析 `page/page_size`（上限 200），`:141-168` 的 count/page SQL 也做了 LIMIT/OFFSET；
  - 前端固定只取第一页 100 条、且没有 page 状态或“加载更多”：`frontend/src/app/inbox/page.tsx:353-360` `listInbox({ status, archived, pageSize: 100, q })`；全文件 grep `page` 无列表翻页；
  - 平台筛选是本地过滤：`frontend/src/app/inbox/page.tsx:376-398` “Two-stage filter… then the platform chip narrows the list”；后端 `listInbox` 只接受 status/archived/q（`inbox_handlers.go:108-120`），无 `platform`/`assignee`/`tag` 参数。
- **具体缺什么**：列表翻页或无限滚动；平台/客服/标签的服务端筛选参数（否则“搜索页 3 才会命中”要靠关键词而非筛选）。
- **用户可感知影响**：会话超过 100 条的租户，第 101 条以后的对话无法浏览（只能靠关键词搜索）；平台筛选只过滤“已加载的 100 条”，计数徽章也只代表当前页。
- **优先级**：P2；**工作量**：S（前端翻页）/M（服务端筛选参数+SQL 门禁）。

### 5. 宏（macros）只有 CRUD，没有执行入口
- **状态**：未接线
- **证据**：
  - 后端：`internal/api/misc2_handlers.go:187`（list）、`:227`（insert steps jsonb）、`:242`（update）、`:255`（delete）；`router.go:232-235`。
  - 全仓库 grep `macros`：没有任何“读取 steps 并逐条发送”的消费者；收件箱前端 grep `macro` 为 0（只在 `components/admin/enterprise-tab.tsx:178-211` 有管理面板）。
- **具体缺什么**：在收件箱选择宏 → 展开 steps → 按 delay 依次入 outbox 的执行器；权限接线；测试。
- **用户可感知影响**：商家可创建“多步回复”，但任何界面都无法使用它；文档 §十一-4 把它列为人工协同能力。
- **优先级**：P2；**工作量**：M（执行器+UI+测试，1-3d）。

### 6. 分配规则（routing_rules）只写不读
- **状态**：未接线
- **证据**：`internal/api/misc2_handlers.go:109-165` 只有 list/upsert/delete；全仓库除 `tenantscope.go:78` 与迁移/测试外无任何 `FROM routing_rules` 读取；`frontend/src/components/admin/enterprise-tab.tsx:124-170` 提供规则 CRUD。
- **具体缺什么**：会话创建/转人工时按 priority + conditions 选客服的解析器；与 `agent_teams`/技能表打通；至少把“当前规则未生效”在 UI 标注。
- **用户可感知影响**：配置了“VIP 客户→A 客服”的规则后，会话仍要求人工手动指派；规则页是“假开关”。
- **优先级**：P2；**工作量**：M。

### 7. LINE 渠道半实现（出站仅文本，入站图片/文件/贴纸静默丢弃）
- **状态**：半实现
- **证据**：
  - 出站：`internal/platform/channel_line.go:19-21` `if msg.Kind != "text" { return "", fmt.Errorf("LINE currently supports text messages only") }`；能力表 `capabilities.go:286-302` `Media: MediaText`（声明诚实，但意味着 AI 图片/文件回复在 LINE 永远不支持）。
  - 入站：`internal/platform/webhooks.go:917-928` 只处理 `text` 与 `audio/voice`；其它 `msgType`（image/video/file/sticker/location）走到 `:926` `if text == "" { continue }` 被丢弃，无事件、无日志。
- **具体缺什么**：LINE 图片/文件入站下载（client 已有 DownloadContent）→ 媒体事件；贴纸/位置文本标记（对齐 Telegram）；出站图片/按钮（LINE 支持 image + quickReply，能力表可按需扩展）。
- **用户可感知影响**：LINE 客户发商品图问“这个有货吗”，AI 完全收不到，会话里也无任何提示；商家以为消息没发进来。
- **优先级**：P2（柬埔寨主战场是 Telegram/Meta，LINE 量小）；**工作量**：M（入站 S、出站图片 M）。

### 8. Zalo 入站只认文本/图片（贴纸/文件/位置/表情丢弃）
- **状态**：半实现
- **证据**：`internal/platform/webhooks.go:985-1004` 的 switch 只有 `user_send_text`、`user_send_image`，`default: // Stickers / files / location events are ignored for now.`
- **具体缺什么**：与 Telegram 对齐的文本标记/媒体下载；或至少在事件表留痕并告警。
- **用户可感知影响**：Zalo 客户发文件/位置无回应，且无后台记录。
- **优先级**：P2；**工作量**：S（标记）/M（媒体下载）。

### 9. Email 与 Voice(SMS) 渠道：env、config、状态卡都有，渠道代码为零
- **状态**：未实现（配置/UI 已声明）
- **证据**：
  - `.env.example:291,292,297` `EMAIL_ENABLED/SMTP_HOST/EMAIL_INBOUND_DOMAINS`；`:299,300` `VOICE_ENABLED/TWILIO_ACCOUNT_SID`；
  - 解析：`backend-go/internal/config/config.go:65-75`（EmailConfig/VoiceConfig）、`:253-260`；
  - **唯一消费者**是状态上报：`internal/api/analytics_handlers.go:305-323` `integrationsStatus` 返回 email/voice 的 enabled/configured；
  - 前端展示：`frontend/src/components/admin/enterprise-tab.tsx:326-338` 把 `Email`、`Voice/SMS`、`SSO` 渲染成三张状态卡（configured 只来自 env 非空）；
  - 能力表无 email/voice 行（`capabilities.go:254-403`），无 webhook 路由（`webhook_routes.go:44-55`），无 Send/Channel 实现。
- **具体缺什么**：要么实现（SMTP 收信/发信、Twilio 语音/SMS、会话映射、渠道能力行），要么删除 env/状态卡避免误导。
- **用户可感知影响**：企业页把 Email/Voice 显示为“已配置”而实际没有任何邮件/电话渠道 —— 典型的“看起来支持”。（迁移/表侧也无对应会话类型。）
- **优先级**：P2；**工作量**：L（真正实现）/S（先摘掉/标注“未上线”）。

### 10. SLA「解决超时」只记录不通知；首响/解决不对称
- **状态**：半实现
- **证据**：`internal/api/tasks.go:348-356` 首响违约传 `"SLA 违约：首次响应超时"` + notify；`:371-379` 解决违约传空 title/body，注释 `:367-370` 自认 “record only … Asymmetric on purpose or not is an open question”。
- **具体缺什么**：解决类违约同样发 owner 通知（并可选择去重/批量）；`listSLABreaches` 只能被动查看（`misc3_handlers.go:13-47`）。
- **用户可感知影响**：只有“首次响应超时”会响铃；“客户等解决等到超时”永远不提醒，SLA 页之外无感知。
- **优先级**：P2；**工作量**：S。

### 11. 迁移 001/030 的两张协作表无消费者（message_notes / session_assignments）
- **状态**：未接线（死表）
- **证据**：DDL：`001_init.sql:235` `session_assignments`、`030_enterprise_agent_platform.sql:108` `message_notes`；全仓库 Go 代码仅 `tenantscope.go:81,114` 的表清单与 `deletion.go:336` 的注释引用，**无 SELECT/INSERT**。实际功能另走 `sessions.internal_notes` / `sessions.assigned_agent_id`（`inbox_handlers.go:274-322,566-590`）。
- **具体缺什么**：删表或接线（备注历史/指派历史需要独立行才有价值）；文档 §十一-4 以这两张表为证据描述“内部备注/会话指派”，已过时。
- **用户可感知影响**：无直接感知（功能以另一列实现），但表结构/文档误导后续开发与外部集成。
- **优先级**：P2；**工作量**：S（迁移删/文档改）。

### 12. `edited_at` 只写不读（编辑无可见标记）
- **状态**：未接线
- **证据**：写入：`internal/api/inbox_handlers.go:543` `UPDATE chat_messages SET content=$1, edited_at=NOW()`；读取：`internal/api/chat_handlers.go:707-710` 的列清单没有 edited_at，前端全仓库 grep `edited_at` 为 0。迁移 086 建列（docs §三十三）。
- **具体缺什么**：transcript 返回 edited_at 并在气泡上显示“已编辑”；否则审计承诺（“我们发过什么”）只能查库。
- **优先级**：P2；**工作量**：S。

### 13. capability 表字段声明无消费者（架构债）
- **状态**：未接线
- **证据**：`capabilities.go:117` `SupportsTyping`、`:118` `SupportsButtons`、`:126` `FeedbackButtons`、`:151` `MaxPerInbound` —— 全仓库除声明处与测试外**零读取**（typing 由各 Channel.Typing 自行 no-op；按钮/CSAT 由 Telegram/CSAT 分支硬编码；`MaxPerInbound` 注释写“WeChat 5/48h”，但本 build 无 WeChat 渠道）。
- **具体缺什么**：要么让编排层消费（typing 前判断、按钮能力校验、每入站发送上限），要么删字段避免“声明即实现”的错觉。
- **优先级**：P2；**工作量**：S。

### 14. 渠道「断开」只有 Telegram 做了 provider 侧清理
- **状态**：半实现
- **证据**：`channel_line.go:13` / `channel_zalo.go:13` / `channel_meta.go:13` 全部内嵌 `noTeardown`（`channel.go:170-172` 返回 ""）；`channel_telegram.go:20` 的 Disconnect 会 DeleteWebhook。文档 §十一-1 称“断开连接（Channel 接口）”与 capabilities 注释（`capabilities.go:81-89`“tears down the provider-side half”）给读者“Meta 会取消订阅”的印象。
- **具体缺什么**：Meta/WhatsApp 应 `DELETE /{page|waba}/subscribed_apps`、LINE 应删 webhook endpoint；否则同一账号换租户重连时旧 app 仍接收事件（当前靠身份唯一索引挡了一部分）。
- **优先级**：P2；**工作量**：S。

### 15. 关键路径测试缺口（单列）
- **状态**：测试缺口
- **证据**：全仓库 `*_test.go` 中 `claimOutbound`/`deliverToProvider`/`markDelivered`/`enqueueDelivery`/`flushDeliveryReceipts` 命中为 0（只有 `audit_fixes_test.go` 对 SQL 常量做正则断言）；`LineWebhook`/`ZaloWebhook`/`handleMetaComment` 也无测试；widget 只有 token 解析测试（`widget_handlers_test.go:19,71`）。
- **具体缺什么**：出站队列（claim 队头/重试/终态/取消/回执重放）的 DB 级测试、LINE/Zalo webhook 解析测试、评论转私信测试（含字段订阅断言）。
- **用户可感知影响**：这些路径一旦回归（历史上就发生过 receipt 写错列、SLA 扫描静默 no-op）没有任何测试能挡住。
- **优先级**：P2；**工作量**：M。

### 16. 默认关闭 / 依赖外部审批（已实现，勿误报为缺失）
- **状态**：默认关闭-only（属产品决策）
- **证据**：follow-up `internal/api/followup.go:21-31`（`FOLLOWUP_HOURS` 默认 0，`tasks.go:52-55` 仅在 >0 时注册循环）；评论转私信 `config.go:249` `envBool("META_COMMENT_DM_ENABLED", false)` + `webhooks.go:207-210` 早退；TTS 语音回复 `config.go:155,294`（`TTS_ENABLED=false`，`TTSActive()` 还需 R2）+ `inbound_stages.go:725`；HUMAN_AGENT `policy.go:105-111` 依赖 Meta App Review（`human_tag.go` 对未审批错误做终态分类与首告警，`pipeline.go:1955-1959`）。
- **具体缺什么**：上线前需要环境配置/外部审批；代码本身完整。评论转私信即使审批通过也还缺字段订阅（见第 3 条）。
- **优先级**：—；**工作量**：—。

## 文档声明核对表（docs/DEVELOPMENT.md）

| 文档声明 | 现状 | 证据 |
|---|---|---|
| §十一-1 渠道：Telegram、LINE、Zalo、Meta、WhatsApp（含模板）、网站挂件 | **成立**（但 LINE/Zalo 能力有限、WhatsApp 手动连接被校验堵死） | `capabilities.go:254-403`；`channel_line.go:19`；`platforms/page.tsx:175-181` |
| §十一-1 渠道差异收在数据里、api/编排层零平台名分支 | **基本成立**；前端连接表单/能力文案仍是独立硬编码（有漂移风险） | `platforms/page.tsx:125-221` vs `capabilities.go:254-403` |
| §十一-1 运维支撑：OAuth 会话、连接健康、投递回执、出站队列 | **部分成立**：OAuth/健康/outbox 有；**投递回执仅 WhatsApp**（Meta 订阅了 delivery/read 但无消费分支；LINE/Zalo/Telegram 无回执） | `webhooks.go:1076-1082,1289-1330`；`meta_oauth.go:256`；`webhooks.go:299-385` |
| §十一-2 入站 21 个命名阶段 | **已过时**：现为 22 个（新增 attach-doc-image/post-delivery 等） | `inbound_stages.go:116-143` |
| §十一-4 转人工三来源、队列、接管/指派/解决/关闭、SLA 扫描 | **成立**（SLA 解决类不通知见清单 10；handoff 页无“指派他人”，仅接管/解决） | `inbound_stages.go:347-355,441-453,674-684,890-902`；`tasks.go:272-381`；`handoff-requests/page.tsx:89-103` |
| §十一-4 内部备注 `message_notes`、话术、宏、`session_assignments` | **过时/部分不成立**：消息备注与指派实际走 `sessions` 列；两张表无消费者；宏只有 CRUD 无执行 | `tenantscope.go:81,114`；`misc2_handlers.go:187-255` |
| §十一-5 收件箱：服务端搜索、平台筛选、分页、归档、批量 | **部分成立**：搜索/归档/批量成立；**分页仅后端**、平台筛选是前端本地 | `inbox_handlers.go:102-168`；`inbox/page.tsx:353-360,376-398` |
| §十一-5 会话支持文本/媒体/语音/附件/翻译/按钮/WhatsApp 模板/消息反馈 | **成立** | `chat_handlers.go:694-815`；`inbox/page.tsx:1363-1490`；`widget_handlers.go:682` |
| §十一-7 “Webhook 订阅与投递（webhook_subscriptions / webhook_deliveries）” | **不成立**：只有订阅 CRUD，无投递 | `misc2_handlers.go:280-327`；迁移 030；无 `webhook_deliveries` 写入 |
| §二十八 Messenger 语音延迟修复（aux thinking budget、asrTimeout） | **成立** | `gemini.go:2137-2180`；`pipeline.go:914`；`inbound_stages.go:230-250` |
| §二十九-9 HUMAN_AGENT 属 Meta App Review、代码侧首次失败告警 | **成立** | `human_tag.go:300-306`；`pipeline.go:1947-1959` |
| §三十一 Telegram 排版（Markup）、Messenger 已读、三渠道 CSAT、Messenger 菜单、入站补齐 | **成立**（但 Meta reaction→CSAT 因缺 `message_reactions` 订阅不可达；IG 未在线验证） | `channel_telegram.go:84,184`；`channel_meta.go:158-175`；`verify.go:176-186`；`meta_oauth.go:256` |
| §三十二-三 评论转私信（默认关，需 App Review） | **部分不成立**：除审批外，OAuth 订阅未包含 `feed`/`comments`，即便审批通过也不投递 | `meta_oauth.go:256,262`；`webhooks.go:207-237` |
| §三十二-四 解决后主动跟进（默认关，ProactiveSend 渠道） | **成立** | `followup.go:56-150` |
| §三十二-五 “明确没做：多附件/编辑撤回” | **已过时**：同日 §三十三/§三十四 已实现，代码也有（迁移 084/086、相册/编辑/撤回） | `channel_extras_test.go:414-434,465-541`；`inbox_handlers.go:456-558` |
| §三十三 编辑/撤回（能力表门禁、渠道拒绝不改本地、UI 仅 Telegram） | **成立**（缺“已编辑”可见标记） | `inbox_handlers.go:488-546`；`inbox/page.tsx:1833-1856` |
| §三十四 多附件（media_items、相册、空文本块修复） | **成立** | `channel.go:17-23`；`channel_extras_test.go:465-541`；`capabilities.go:430-447` |
| §三十五 附件能力校验（渠道不支持返回 400）、收件箱 Markdown | **成立** | `inbox_handlers.go:57-95,364-372` |
| §三十六 明确未修 RLS 接线 | **成立**（仍是 scaffold） | `tenantscope`（文档已自述） |

## 未能核实的项（及原因）

1. **Meta/WhatsApp 生产订阅与审批状态**：无法访问生产 `.env-go`、Meta App 后台；无法确认 `META_WHATSAPP_EMBEDDED_SIGNUP_CONFIG_ID`、`META_OAUTH_*` 是否配置，也无法确认 App Review 是否已批准 `pages_read_engagement/pages_manage_engagement/instagram_manage_comments/message_reactions`。因此第 2、3 条的“生产影响”属**确认代码缺口 + 疑似环境缓解**（若商家在 Meta 后台手工补订阅字段，reaction/评论路径可部分恢复）。
2. **默认关闭开关的生产取值**：`FOLLOWUP_HOURS`、`META_COMMENT_DM_ENABLED`、`TTS_ENABLED`、`SAFETY_ENABLED` 等只看到代码默认值与 `.env.example`，生产真实值不可见（文档提到安全内容默认关）。
3. **Instagram 运行时路径**：文档 §三十一 自述部署里只有 Messenger 页面，无真实 IG 账号；本次只能静态确认代码同路径，**未验证** IG 的 typing/quick reply/reaction 实际表现。
4. **LINE/Zalo/Twilio 真实账号行为**：仓库内无凭据、无本地可运行 mock 环境，仅静态核对解析与签名逻辑；未做线上收发。
5. **收件箱 >100 会话的生产量级**：无法查生产 `sessions` 计数，故第 4 条的用户影响强度是“逻辑上必然、规模待数据确认”。
6. **`webhook_deliveries` 是否有外部（仓库外）消费者**：只审计本仓库；若另有独立投递服务不在其中，本结论需再核（文档/部署手册未提及此类服务）。
7. **meta_oauth 订阅后的投递链路**：`subscribed_fields` 的生效还依赖 Meta App 的 Webhook 回调配置（生产后台），本仓库不可见；代码侧缺口（无消费分支）已确认。

---
审计执行：只读代码检索/阅读，未改动代码、配置、数据库；报告文件为本任务唯一写入。

---

<a id="part4"></a>

# 第四部分 · 领域明细：AI / 模型服务商 / 人格 / 知识库 RAG / 文档图片 / 用量配额


---

# AI 对话 / 模型服务商 / 人格 / 知识库 RAG / 文档与图片 / 用量配额 功能完整性审计

- 仓库：`D:\桌面\2\khmer-ai-cs-go`，HEAD `231a755`（2026-10-17，与 origin/main 同步）
- 审计时间：2026-10-19；只读审计，未改代码/配置/数据
- 说明：`_audit/`、`_research/` 等未跟踪目录不在审计范围

## 范围与方法

**范围**：后端 `internal/{gemini,llm,anthropic,deepseek,rag,persona,replyscore,usage,textutil}`；`internal/api` 下 knowledge_handlers.go / knowledge_media.go / model_config.go / model_prompt_history.go / consume_doc_quota.go / translate_handlers.go / chat_handlers.go 的 AI 部分；迁移 048/058/063/067/073/079/080/082/083；`cmd/{rageval,jeveval,embedcmp,billingreconcile}`；前端 `app/{knowledge,ai-test,admin/personas,billing}` 与 `lib/api.ts`、`components/knowledge/upload-dialog.tsx`、`components/admin/admin-pages.tsx`。

**方法**：先按文档声明列「应有功能清单」，再在路由（router.go）、handler、迁移、前端调用点、测试中逐项查证；只有当代码/迁移/测试能证实时才记为「确认不完整」，需要生产环境或真实凭据的列入「未能核实」。文档（`docs/DEVELOPMENT.md`）只作线索。

**验证性执行**（只读，不改数据）：

```
go test -count=1 ./internal/rag/ ./internal/usage/ ./internal/persona/ ./internal/replyscore/ \
  ./internal/llm/ ./internal/anthropic/ ./internal/deepseek/ ./internal/gemini/
# 全部 ok（2026-10-19）
```

## 结论摘要

1. **AI 生成主体的三服务商路由是完整的**：默认行 `provider` 决定生成模型，Gemini 行承载 embedding/检索/重排/改写/图片/语音/TTS/Jev；Claude/DeepSeek 缺省时失败而不静默回退。该能力矩阵与文档一致，且 `llm.Model` 接口里没有 embedding 方法，决定了辅助能力只能留在 Gemini。
2. **人格（persona）是最大半成品**：`session → conversation → global` 三级解析写着，但生产唯一调用点把 conversationID 恒传空串 → **conversation 作用域绑定永远不生效**；人格只在平台渠道入站生效，**控制台 AI 测试（/ai-test → /chat）与网站挂件都不经过人格**；`error_reply` 有 UI 输入框但没有任何消费者；`tools` 白名单既无 UI 也无消费者。
3. **成本护栏有两处确认缺口**：`POST /rag/query`（知识库"试问"）直接调主模型，但既不 `usage.Record` 也不查 `usage.Budget`；`usage.Budget` 对整个 `token_usage` 做全局 SUM，**没有按租户上限**（文档 P0/P1 已承认，代码仍未做）。
4. **知识库衍生能力多数已接线**（AI 编译、矛盾检测、知识缺口+草稿、质量统计、图片回传、检索缓存失效都有生产者与前端入口），但 **近似重复检测已后端口算并返回 `similar_docs`、前端完全没读**；分类只能在"新建文件/URL"时设置，已有文档不能改分类、`tags` 完全无 UI；后端 `GET /knowledge` 仍无 `q`。
5. **模型配置存在一个死旋钮**：`model_configs.context_cache_ttl` 管理页可编辑、API 会 UPDATE、列表会回显，但 Gemini 客户端只读环境变量 `GEMINI_CACHE_TTL`，四个加载 SQL 都不选该列 → 改了不起作用。
6. 其余「不完整」多为文档已声明的边界（评测语料缺失、TTS/内容安全默认关、无按租户预算）或需生产凭据核实（真实 Anthropic/DeepSeek 端点、embedding 429 现状）。

## 已核对且完整的功能（简要）

| 功能 | 代码落点（已核对） |
|---|---|
| 三生成服务商路由；切 provider 不静默回退 | `internal/llm/llm.go:35-52,220-239`；`llm.Model` 接口 `:97-105` |
| Gemini-only 辅助能力矩阵（embedding/rerank/rewrite/图片/ASR/TTS/Jev） | `llm/llm.go:11-15` 包注释；`llm.Model` 无 embedding 方法；`gemini.go:2629+` |
| 温度字段真生效（§十三 的历史 bug 已修） | `gemini/gemini.go:1728-1731,1842`、`608-621`；`vertex_transport_test.go:566-606` |
| Claude 模型不发送 temperature（按目录 Sampling 判定） | `anthropic/catalog.go:27`（`Sampling:false`）、`anthropic.go:302-305` |
| DeepSeek thinking 关闭后 temperature 生效；价目/目录 | `deepseek/catalog.go`、`deepseek.go:270-294`、`usage/deepseek_cost_test.go` |
| 模型目录 / 测试连接 / 区域列表 / prompt 版本历史 | `router.go:159-175`；`model_config.go:442-490`；`model_prompt_history.go` |
| 文件摄入格式 txt/md/csv/pdf/docx/xlsx/wps/et + 图片 OCR | `rag/extract.go:34-78`；`api/knowledge_handlers.go:151-171,236-279` |
| 扫描 PDF OCR 回退；图片产品读图 | `knowledge_handlers.go:251-277`；`extractKnowledgeText` 路由到 `ExtractProductImage` |
| URL 导入（SSRF 守卫 + 空正文明确 422） | `rag/extract.go`（FetchURLContent）、`knowledge_handlers.go:281-328` |
| 文档配额原子门禁 + 失败退款 + enterprise 不限量 | `consume_doc_quota.go:25-33,56-90`；三个上传入口 `knowledge_handlers.go:79,173,314` |
| 消息配额：渠道/挂件/控制台三入口 | `platform/inbound_stages.go:794`、`widget_handlers.go:459`、`chat_handlers.go:246,370`；`usage/quota.go:38-103` |
| 向量+词法+trigram 融合、RRF、rerank 门控、降级告警 | `rag/service.go:673-800,978-1000`；`DenseUnavailable` `:118-123,679-680` |
| 高棉语分词列与分词器 | 迁移 `058_khmer_segmented_lexical.sql`；`rag/segment.go` + `segment_test.go` |
| 检索缓存 45s + 按租户版本失效（索引成功/删除时 bump） | `rag/search_cache.go:12-102`；`service.go:325,1823` |
| 知识库图片：media_key 三条检索腿 + attach-doc-image stage + 渠道 caption 规则 | `rag/service.go:1057,1095,1137`；`platform/attach_image.go:40-90`；`attach_image_test.go` |
| AI 编译（默认开启、可全局切换、编译子文档+矛盾落库） | `rag/service.go:348-382,435-600`；`GET/PUT /admin/rag/settings` `router.go:86-87` |
| 矛盾检测：Jev 复核 + 人工 resolve/dismiss + 前端队列 | `rag/contradiction_jev.go`；`knowledge_handlers.go:629-690`；`knowledge/page.tsx:179-186,557-596` |
| 知识缺口列表 + 一键草稿 + 前端入口 | `knowledge_handlers.go:568-622`；`rag.KnowledgeGaps`；`knowledge/page.tsx:216,332-346` |
| 文档质量统计（引用/👍👎）+ 前端入口 | `knowledge_handlers.go:511-563`；`knowledge/page.tsx:170,555` |
| ReplyGuard（Jev 语义检查 + 告警） | `platform/guard.go:42-150`；stage `inbound_stages.go:614-632`；`guard_test.go` |
| 会话记忆（跨 20 条窗口） | 迁移 083 / `platform/session_memory.go`；`session_memory_test.go` |
| 翻译（单条/批量、按 20 条/12k 切块、缓存、数字被改写标警） | `api/translate_handlers.go`；`translate_e2e_test.go` / `translate_handlers_test.go` |

## 不完整功能清单

### 1. 人格 conversation 作用域未接线（三级解析只生效两级）
- **状态**：未接线（半实现）
- **代码证据**：
  - `backend-go/internal/persona/persona.go:24` `var Scopes = []string{"session", "conversation", "global"}`
  - 唯一生产调用点 `backend-go/internal/platform/inbound_stages.go:281`：
    `per, ok, err := persona.NewStore(p.DB).ForTurn(ctx, t.Config.UserID, t.SessionID, "")` —— **conversationID 恒为 `""`**
  - 前端把服务端 scopes 原样渲染成下拉：`frontend/src/components/admin/admin-pages.tsx:99,330-338`；API 也返回三档 `persona_handlers.go:104`
- **缺什么**：入站 turn 没有 conversation 概念（只有 session），要么用 session/平台会话 id 填充，要么删掉这一档；当前 UI 会诱导用户绑定"本次对话"。
- **用户可感知影响**：商家为某次"对话"设定的人格永远不生效，只有"本次会话/全站"可用。
- **优先级**：P1　**工作量**：S

### 2. 人格只作用于平台渠道入站；控制台 AI 测试与网站挂件不受人格影响
- **状态**：半实现（覆盖路径不全）
- **代码证据**：
  - 全仓唯一 `ChatAs` 调用：`backend-go/internal/platform/inbound_stages.go:562` `result, err := p.serving().ChatAs(ctx, message, history, t.ReplyLang, systemPrompt)`
  - 控制台：`backend-go/internal/api/chat_handlers.go:294` `a.serving().Chat(...)`；`/ai-test` 页经 `lib/api.ts:428-429 sendTestMessage → POST /chat`
  - 网站挂件：`backend-go/internal/api/widget_handlers.go:578` `a.serving().ChatStream(...)`，无 persona 解析
- **缺什么**：`/chat`、`/chat/stream`、`/widget/chat` 三条生成路径都不解析 persona（只有渠道 inbound 的 resolve-persona stage 解析）。
- **用户可感知影响**：商家在「AI 测试」里验证人格看不到任何变化；网站挂件的客户也得不到人格设定，与实际渠道表现不一致（测试结果误导）。
- **优先级**：P1　**工作量**：M

### 3. 人格 `error_reply`：有输入框、有落库、无消费者
- **状态**：未接线（显式声明但无生产读者）
- **代码证据**：
  - UI 字段：`frontend/src/components/admin/admin-pages.tsx:103,213-220`（"失败时的回复"），i18n `dict-zh.ts:816-817`
  - 存储/回显：`persona_handlers.go:62,236,321`；`persona/persona.go:38-39,114,162,174`
  - 全仓 grep `ErrorReply` 只命中 persona 包与 persona handler/测试；`internal/platform` 0 命中
- **缺什么**：生成失败路径（`ErrNoReply`/模型错误）没有读取该字段，实际走各 surface 自己的兜底话术（如 chat_handlers.go:309-317 的转人工 ack）。
- **用户可感知影响**：商家配置"失败时的回复"不生效，客户收到的是产品默认话术。
- **优先级**：P2　**工作量**：S

### 4. 人格 `tools` 白名单：API 校验/存储存在，UI 与执行器都不存在
- **状态**：未接线（scaffold）
- **代码证据**：`persona_handlers.go:40-46,78-91,265-270`（校验、三值 NULL/[] 语义）；`persona/persona.go:36-37`（注释称工具白名单）；前端表单只有 name/system_prompt/begin_dialogs/error_reply（admin-pages.tsx:103）；全仓无任何代码读取 `Persona.Tools`
- **缺什么**：聊天管线没有 tool-calling，`tools` 字段没有任何执行器；UI 也不暴露该字段。
- **用户可感知影响**：目前不可见；但 DB/API 契约里存在一个无意义字段，后续误导实现者。
- **优先级**：P2　**工作量**：S（删字段或补齐执行器二选一）

### 5. `model_configs.context_cache_ttl` 只写不读（管理页死旋钮）
- **状态**：未接线（写而不读）
- **代码证据**：
  - 列：迁移 `001_init.sql:71 context_cache_ttl INT DEFAULT 3600`
  - 列表回显：`api/model_config.go:91-92,112`；更新写入：`model_config.go:275-276`
  - 前端输入框：`frontend/src/components/admin/admin-pages.tsx:1939`
  - 真正决定上下文缓存的只有环境变量：`gemini/contextcache.go:81-88` `GEMINI_CACHE_TTL`（0=关闭）
  - 四个行加载 SQL 都不选该列：`llm/llm.go:295,314,322,334`；`gemini/gemini.go:3096` 同理
- **缺什么**：客户端从未读取该列；改管理页"上下文缓存 TTL"既不会开启缓存也不会改变 TTL。
- **用户可感知影响**：运营改配置后成本/延迟无任何变化，且误以为缓存已打开（§二十五 的缓存成本取舍依赖真实启用）。
- **优先级**：P2　**工作量**：S

### 6. `POST /rag/query` 主模型调用不计费、不撞预算门
- **状态**：半实现（计量/护栏覆盖缺口）
- **代码证据**：
  - `api/knowledge_handlers.go:470-496`：`ctx := a.RAG.Ground(...)` 后 `result, err := a.serving().Chat(...)`，返回 `tokens`，但**没有 `usage.Record`**
  - 全仓 `INSERT INTO token_usage` 只有 usage.go:200 一处写入器；调用者仅 `chat_handlers.go:321,471`、`inbound_stages.go:590`、`widget_handlers.go:639`
  - `usage.Budget` 全仓调用点：`inbound_stages.go:536`、`chat_handlers.go:280,422`、`widget_handlers.go:564`、`translate_handlers.go:541`、`platform_admin_handlers.go:252`——**不含 ragQuery**
  - knowledge_handlers.go 虽 import usage（:15），但只用于 plan 常量（:25-27）
- **缺什么**：RAG 试问消耗的主模型 token 不进账本 → 平台成本面板/Gemini 预算门都看不见；无预算门可被反复调用绕过全局窗口。
- **用户可感知影响**：租户可无限次点"试问"消耗平台额度；运维排查"钱花在哪"时缺一块数据。
- **优先级**：P1　**工作量**：S

### 7. 控制台 /chat 的配额与用量记在调用者 `user_id`（席位成员时错账户）
- **状态**：半实现（多席位路径的配额归属缺陷）
- **代码证据**：
  - 配额：`api/chat_handlers.go:246,370` `usage.ConsumeMessageQuota(r.Context(), a.DB, user.UserID)`（非 `user.Tenant()`）
  - `tenant_billing` 以 `user_id` 为主键、默认 free 配额 500：`usage/quota.go:39`；`migrations/025_tenant_billing.sql:10-11`
  - 流式 token 记账：`chat_handlers.go:471` `usage.Record(persistCtx, a.DB, user.UserID, ...)`，与同文件 plain 路径 `:321` 的 `user.Tenant()` 不一致
- **缺什么**：调用者是成员（Tenant≠UserID）时，消息配额消耗在成员自己的 billing 行（懒创建=默认 free 500），不占租户配额；流式 token_usage 也记在成员行，平台按租户汇总会漏。
- **用户可感知影响**：租户的席位可通过"AI 测试"聊天绕过租户月度消息配额；平台成本/租户报表少计席位流式消耗。
- **优先级**：P2（金额有限但语义错误）　**工作量**：S

### 8. 近似重复检测：后端已实现并返回 `similar_docs`，前端完全未接线
- **状态**：未接线（前后端契约不完整）
- **代码证据**：
  - `rag/service.go:632-637`：上传后 `if similar := s.findSimilarDocs(...); len(similar) > 0 { doc["similar_docs"] = similar }`
  - `rag/service.go:640-667`：dense 相似度 >0.88、LIMIT 3，注释明确"warn about near-duplicate uploads"
  - 前端全仓 grep `similar_docs` **0 命中**
- **缺什么**：前端没有任何提示/去重入口；后端每次上传都额外付一次 query embedding + 查询（白花成本）。
- **用户可感知影响**：商家重复上传近似文档时没有任何提示，知识库碎片化（正是该功能想解决的问题）；文档 §十一 P2 仍写"无重复/近似重复检测"，与代码现状不符。
- **优先级**：P2　**工作量**：S

### 9. 知识库分类/标签编辑缺失（新建可设分类，已有文档不可改；tags 无 UI）
- **状态**：半实现（前后端契约不完整）
- **代码证据**：
  - 后端 `PUT /knowledge/{id}` 支持 category/tags：`knowledge_handlers.go:52-58,399`
  - 上传窗口有 category 输入：`components/knowledge/upload-dialog.tsx:208-211,247-250`
  - 预览编辑只提交 `{content}`：`app/knowledge/page.tsx:417` `updateKnowledgeDocument(previewDoc.doc_id, { content: editContent })`；页面无 category/tags 输入框
  - `uploadKnowledge` API 类型虽含 tags（lib/api.ts:443-449），dialog 未传；前端全仓无 tags 输入
- **缺什么**：已有文档的分类/标签不能编辑；tags 完全不可由控制台产生。
- **用户可感知影响**：42 篇存量文档只能靠重建来分类；按分类分组能力只能用新建时的那一次赋值；文档 §十一 P2 "分类编辑"缺口未闭合（但"新建时能设分类"部分已实现，文档已过期一半）。
- **优先级**：P2　**工作量**：M

### 10. 知识库文档内容搜索仍为前端本地过滤（后端无 q）
- **状态**：未实现（文档已声明）
- **代码证据**：`knowledge_handlers.go:345-358` 只解析 `page/page_size`，无 `q`；前端本地过滤 `app/knowledge/page.tsx:295`（`doc.title... || doc.category...`）
- **缺什么**：后端搜索参数/SQL；分页只拉当前页（page_size=20，lib/api.ts:523-525），本地过滤只能过滤已加载页 → 上千篇时"搜不到"。
- **用户可感知影响**：文档多时搜索不可靠，只能翻页。
- **优先级**：P2　**工作量**：M

### 11. 只有全局滚动预算门，没有按租户日/月上限
- **状态**：未实现（文档 §十一 P0/P1 已承认）
- **代码证据**：`usage/spend.go:337-341` `SELECT COALESCE(SUM(cost_estimate), 0) FROM token_usage WHERE created_at > $1`——**无 user_id/租户条件**；`usage.Budget` 的所有调用方都是同一全局函数
- **缺什么**：按租户 token/金额配额、异常突增告警；一个租户跑飞会让全体撞 503/转人工。
- **用户可感知影响**：单租户异常（攻击/循环）导致所有商家回复被预算门削掉。
- **优先级**：P1　**工作量**：M

### 12. 嵌入/检索只有 Gemini 一条腿，无跨 provider 回退；429 时语义召回永久降级
- **状态**：半实现（单点依赖；文档 §十一 P0 的 429 事故设计未变）
- **代码证据**：`llm.Model` 接口不含 embedding（`llm/llm.go:97-105`），所以切默认为 Claude/DeepSeek 也仍走 Gemini；`gemini/gemini.go:2629` `GenerateQueryEmbedding` 只走 `activeProvider()`（:388，AI Studio/Vertex 二选一传输，不是双活）。失败路径只告警：`rag/service.go:679-680 DenseUnavailable`
- **缺什么**：embedding 的 AI Studio 备用 key/跨传输自动回退、或本地/备用 embedding 模型；当前 429 时只剩词法，且需运维手改环境。
- **用户可感知影响**：一旦 embedding 配额耗尽，高棉语/跨语言语义召回悄悄丢失（回答仍在但质量下降），文档 §二十一 的告警只让运维知道、用户无感知。
- **优先级**：P1（需生产配额现状佐证影响面）　**工作量**：L

### 13. 评测闭环不可复现：工具在，语料不在
- **状态**：不可交付（文档 §十一 P2 已声明"rageval 语料随测试租户删除"；§二十三/二十四 的评测结果无法复跑）
- **代码证据**：`cmd/rageval/main.go:39,46-47` `-eval` 必填、`:50-52` `DATABASE_URL` 必填；`cmd/jeveval/reply.go:136-145` 同样要求 DB + `-eval`；全仓无 eval JSON/CSV（只有 `cmd/jeveval/turns.sql` 模板）
- **缺什么**：随仓库版本化的 eval 语料/期望集；当前只有工具链。
- **用户可感知影响**：模型/提示词/rerank 的任何改动都无法按文档回归验证（本次审计也只能跑单测，跑不了效果评测）。
- **优先级**：P2　**工作量**：M

### 14. TTS 语音回复：已实现但默认关闭 + 依赖 R2（dormant）
- **状态**：默认关闭-only
- **代码证据**：`internal/config/config.go:154-155` `Enabled bool // TTS_ENABLED — off by default`；`:294` `Enabled: envBool("TTS_ENABLED", false)`；`:407-408` `TTSActive() = Enabled && R2Enabled()`；调用点 `platform/inbound_stages.go:725`
- **缺什么**：默认不交付；租户侧无开关（纯部署环境变量）。
- **用户可感知影响**：语音客户默认仍收到文字回复；开启需运维同时配置 TTS_ENABLED + R2。
- **优先级**：P2　**工作量**：S（若产品决定默认开）

### 15. 内容安全 gate 默认关闭，且 6 个 `SAFETY_*` 键不在 `.env.example`
- **状态**：默认关闭-only + 文档仍成立
- **代码证据**：`platform/safety.go:176-195`——只有配了 `SAFETY_KEYWORDS` 或 `SAFETY_MODEL_ENABLED=1` 才启用，`SAFETY_INBOUND_ACTION` 默认 handoff；根目录 `.env.example` grep `SAFETY` 0 命中
- **缺什么**：默认开的产品决策与示例配置；B2B 签约通常要求默认开（文档 §十一 P1 已写）。
- **用户可感知影响**：默认部署没有任何模型/关键词内容安全门；运营不知道有这些键。
- **优先级**：P2　**工作量**：S

## 文档声明核对表

| 文档声明（行号） | 代码现状 | 判定 |
|---|---|---|
| §十一-2（1190）成本护栏"计量覆盖：主聊天+辅助生成+嵌入" | 主聊天/渠道/挂件/翻译有记账；**`POST /rag/query` 主模型调用漏记**（knowledge_handlers.go:470-496） | 部分过时（漏 RAG 试问） |
| §十一-2（1189）"内容安全默认关闭，6 个 SAFETY 键尚未写入 .env.example" | `safety.go:176-195` 默认关；`.env.example` 无 SAFETY | 仍成立 |
| §十一-3（1196）摄入文本/文件 txt-md-csv-docx-pdf/URL | `extract.go:34-78` 另含 xlsx/wps/et/jpg/jpeg/png/webp | **已扩展**（文档只列旧集合，非缺陷） |
| §十一-3（1197）双路检索/高棉分词/rerank 门控 | `service.go:673-800`、迁移 058、`segment.go`；相关单测通过 | 仍成立（已实现） |
| §十一-3（1198）AI 编译/矛盾/缺口/质量统计 | handler+前端入口齐全（见"已核对"） | 仍成立（已实现） |
| §十一-3（1199）语义试问 ragQuery | 端点与 UI 在，但漏记账/漏预算门（见不完整 #6） | 部分过时 |
| §十一 P2（1285）"知识库分类编辑：控制台无输入框" | 新建上传已有 category 输入（upload-dialog.tsx:208-211）；**已有文档仍不能改，tags 无 UI** | 部分过时 |
| §十一 P2（1286）"文档内容搜索：GET /knowledge 无 q" | `knowledge_handlers.go:345-358` 仍无 q；前端本地过滤 | 仍成立 |
| §十一 P2（1287）"去重：有矛盾检测，无重复/近似重复检测" | `rag/service.go:632-667` 已实现近似重复检测并返回 `similar_docs`；前端未消费 | **已过时 + 半实现**（见 #8） |
| §十一 P2（1289）"rageval 语料已随测试租户删除（当前跑不动）" | `cmd/rageval` 要求 `-eval`+DB，仓库无语料；`cmd/jeveval -mode reply` 同理 | 仍成立 |
| §十一 P0（1265）嵌入 429 → 纯词法降级 | `llm.Model` 无 embedding、`gemini` 单传输；无跨 provider 回退（#12）；生产配额未核实 | 设计仍未变（影响需生产数据） |
| §十一 P0/P1（1268/1269）无按租户成本护栏 | `spend.go:337-341` 全局 SUM | 仍成立 |
| §十三（1118-1142）temperature"只写不读" | `gemini.go:608-621,1728-1731,1842` 全部接通；测试钉住 | **已修复** |
| §十三（1108-1116）"还留着的"：事实覆盖受温度影响/母语措辞/topK+diversify 限制 | 温度已钉 1.0（文档同节）；`DefaultTopK=3`、`diversifyByDoc` 仍存在（service.go:33-41,794） | 仍成立 |
| §二十（1860-1865）/§二十二（1943-1948）服务商"尚未完成/已知边界" | 辅助能力仅 Gemini（`llm.Model` 接口为证）；Claude 目录 `Sampling:false` 故不发 temperature；DeepSeek 未接入多模态 | 仍成立（属声明边界） |
| §二十（1865）"未对真实 Anthropic 端点实际调用" | 无 key，无法核实 | 未能核实（文档自述） |
| §二十二（1945）"未对真实 DeepSeek 端点实际调用" | 同上 | 未能核实（文档自述） |
| §二十三/§二十四"工具链改造 + 多轮实测" | 工具代码在（claudeeval/rageval/jeveval/embedcmp），但语料不在仓库 | 工具在，结果不可复现 |
| §二十五（2187-2226）文件类型/图标/上传窗口/URL 实测 | `extract.go:34-78` 与 `upload-dialog.tsx` 两 tab 一致；pptx/dps 不在白名单；空正文 422、未知扩展名从 AcceptedExtensions 生成（knowledge_handlers.go:151-160） | 仍成立（已实现） |
| §二十七（2275-2360）图片入库/检索/回传/降级 | media_key 三条 SQL 腿（service.go:1057,1095,1137）；attach-doc-image stage（inbound_stages.go:139, attach_image.go）；失败降级 | 仍成立（已实现） |
| §二十九（2501-2510）"明确没做"（KB 排序加权、多产品照片追问等） | 代码无对应实现 | 仍成立（已声明未做） |

## 未能核实的项（及原因）

1. **Anthropic / DeepSeek 真实端点行为**：仓库无 API key，测试全部是 stub（文档 §二十/§二十二 自述）。连接测试端点 `/admin/models/{id}/test` 的真实响应无法验证。
2. **生产 embedding 配额现状（429）**：需要生产环境与 Google 项目指标；代码侧只能确认"只有一条 Gemini 管线、无自动回退"。
3. **TTS 端到端（Gemini TTS → R2 → 渠道音频）**：需要 `TTS_ENABLED=1`、R2 配置与真实渠道；代码路径已读，运行行为无法核实。
4. **Jev（typesafe）外部决策服务**：依赖 `TYPESAFE_API_KEY` 外部服务；ReplyGuard/路由/Jev 评测的运行效果无法在本地验证。
5. **近似重复检测阈值 0.88 的实际误报/漏报**：需要真实租户知识库数据（seed 数据不足）。
6. **席位（多成员）实际使用情况**：控制台 /chat 配额记到 `user_id` 的问题代码路径已确认，但生产是否存在活跃席位成员决定实际影响面（文档称生产仅 admin 与一个租户）。
7. **§二十三/§二十四 评测结果数字（24/24、judge 11.83 等）**：语料与服务器输出不在仓库（文档称在 `/root/khmer-deploy/arms/`），无法复跑。
8. **渠道图片真实发送**：文档 §二十七 自述"生产 outbox media 投递为 0"，三个渠道未真发过图；需生产数据与真实客户消息才能验证。

---

<a id="part5"></a>

# 第五部分 · 领域明细：前端全站 + 配置 / 迁移 / 部署 / CI / 测试


---

# 前端全站 + 配置/迁移/部署/CI/测试（横切）—— 功能完整性审计

## 范围与方法

- 代码基线：git HEAD `231a755`（2026-10-17，与 origin/main 同步）；工作树只读，未改任何代码/配置，不跑改数据命令。
- 前端全站：`frontend/src/app/**`（24 个 `page.tsx`）、`frontend/src/components/**`（含 admin 五个 Tab、knowledge 编辑器/上传、settings 卡片）、`frontend/src/lib/**`（`api.ts` 1911 行、`billing-api.ts`、`auth-client.tsx`、`realtime.tsx`、三语字典）、`next.config.ts`、`package.json`、`Dockerfile`。
- 横切：根 `.env.example`（440 行）↔ `backend-go/internal/config/config.go` 与全部 `os.Getenv/env*/config.Env*` 读取点；`backend-go/internal/migrations/migrations/066..086`；`.github/workflows/ci.yml`；`.pre-commit-config.yaml`；`deploy-khmer-ai-cs/SKILL.md` 与 `ops/`；`docs/DEVELOPMENT.md`（只作线索）。
- 方法：
  1. 机械对账**前端 → 后端**：抽取 `frontend/src` 里 118 条 `/api/v1` 调用（`api.ts` 字面量、`billing-api.ts`、各页裸 `fetch`、SWR fetcher），把 `${...}`/`{id}`/query 归一化后与 `router.go` 注册的 150 条路由求差集；反向求「后端有路由、前端无调用」。
  2. 逐页看数据源：24 个页面是否真有 API 请求；`grep TODO/FIXME/敬请期待/alert(/空 onClick/disabled={true}` 全站为 0；再按「按钮 → 调用的函数 → 后端端点」抽样核对。
  3. 配置双向差集：`.env.example` 全部键（含注释行）↔ Go 侧真实读取点；前端 `process.env` ↔ 示例文件。
  4. 迁移「新表/新列是否有生产读写」：066–086 逐条 grep 表名/列名，区分「有写有读」「只写不读」「只有迁移」。
  5. 门禁实测：`go build ./...`、`go vet ./...`（均 exit 0）、`npm run lint`（exit 1）、CI/pre-commit 内容核对、`t.Skip`/DATABASE_URL 门控统计。
- 判定口径：**确认不完整**＝有代码证据；**疑似**＝需生产数据/外部审批/运行环境才能定论（单列于末节）。

## 结论摘要（3-6 条）

1. **前端本身完成度很高**：24 个页面全部有真实 API 数据源，无 TODO/空实现/alert 占位/写死 mock；三语字典 1378 个键完全一致；导航与成员权限矩阵（`can(...)`、`PermissionRequired`）基本一致；前端调用的端点没有一个在 `router.go` 里不存在。缺口集中在**少数后端能力没有入口**与**知识库规模上限**。
2. **三处「后端已实现但产品里用不到」**：① API keys（3 个端点 + Bearer `kcs_` 认证）前端零入口，而**企业版套餐文案把「API 密钥（系统对接）」当卖点**；② `notification_pref`（列 + GET/PUT 端点 + 设置页三选项下拉）在通知发送路径**从不被读取**，设置无效；③ `/chat/stream` 与 chat sessions 的 3 个端点没有任何前端消费者（疑似遗留）。
3. **一个只写不读的迁移**：081 `turn_metrics` 每轮写入（`pipeline.go:598`）、保留策略清理（`cmd/retention/main.go:103`），但全仓库 **0 条 SELECT**，文档承诺的「p95 by platform 可查询」不成立。
4. **配置漂移两个方向都明显**：`.env.example` 里有 11 个没有任何消费者的僵尸键（5 个 REPLY_CACHE_* 所属功能已在迁移 076 删除；4 个 SMTP 子键与 TWILIO_AUTH_TOKEN 所属的 Email/Voice 渠道零实现；FRONTEND_PORT 无人读）；反向，**6 个 SAFETY_*、5 个 T2I_*、META_COMMENT_DM_ENABLED、FOLLOWUP_HOURS、7 个 JEV 阈值、SCHEDULER_TZ** 未写进模板。
5. **交付门禁形同虚设**：CI 的后端 job 不提供数据库，`go test ./...` 下 `internal/sqlcheck` 静默 skip、约 20 个 DB 测试全部 skip（54 处 `t.Skip`），CI 不跑 eslint，而 `npm run lint` 在 HEAD 上就 exit 1（2 error，来自仓库内 `frontend/verify-*.js`）；前端 **0 自动化测试**（`src` 下无测试文件、`package.json` 无 test script）。自动备份/外部监控/预发仍缺（已有的 readiness timer 是机内探针）。
6. **文档声明对比 10-04 版本有 5 处已过时**（分类/标签编辑已实现、营销活动表已删、零消费者导出已清理、"无支付"已被 PayPal 取代、free/pro 配额数字），同时 7 处仍成立（文档内容搜索、控制台语音输入、RLS 未上膛、自动备份、外部监控、无预发、退款/发票/试用缺失）。

## 已核对且完整的功能（简要）

- **页面与数据源**：`inbox`（2963 行，列表/会话/回复/翻译/摘要/copilot/客户360/标签/批量关闭与批量指派/编辑撤回）、`knowledge`（列表/上传/URL/编译/AI 矛盾/缺口草稿/试问/质量）、`platforms`（4 渠道 + OAuth + WhatsApp Embedded Signup + 连接自检 + 失败重试）、`admin/{analytics,operations,growth,reports,enterprise,users,personas}`、`platform-admin`（租户/支持/收入/渠道/缺口/模型/token/审计）、`billing`（PayPal 下单/回跳捕获/成员只读）、`settings`（资料/头像/改密/2FA/语言/通知偏好/Telegram 通知）、`widget`（公开 iframe 配置/消息/聊天/反馈/建议问题）、`widget-admin`、`handoff-requests`、`join`、`privacy(+deletion-status)`、`help`、`login`、`ai-test`，全部有真实请求；无一个页面是静态假数据。
- **前后端接口差集**：前端 118 条调用归一化后全部命中后端注册路由（0 条“前端调了不存在的端点”）。动态路径对账包括 `/inbox/sessions/{id}/*`、`/knowledge/{id}/*`、`/platforms/configs/{id}/{kind}/{id}/retry`、`/platform/tenants/{id}/*` 等，均一致。
- **i18n**：`dict-en/zh/km` 各 1378 键，键集完全相同（0 缺失）；`usage/plans_test.go` 还钉住套餐卡引用的 `bl.feature.*` 必须存在于字典。
- **权限一致性**：`layout.tsx:443-467` 按 `can("inbox_view"/"knowledge_view"/"channels_view"/"widget_view"/"settings_view"/...)` 隐藏入口；`admin-pages.tsx:490` `PermissionRequired` 兜住直接输 URL 的席位；收件箱/知识库/渠道/挂件页按同一套键隐藏写按钮（§三十五-一）。
- **计费**：`billing-api.ts` 三调用 + `billing/page.tsx` PayPal 主机白名单、服务端价目、幂等捕获、成员无购买按钮；`PUT /billing/plan` 对非 platform_admin 返回 403（`misc2_handlers.go:603`）。
- **2FA/资料/审计**：TOTP 绑定/替换/停用（QR + 当前码再验证）、头像上传、改密换 token、审计日志查看页（`platform-admin/page.tsx` AuditLogsPanel + `api.ts:1508`）、CSV 导出三报表（`reports-tab.tsx`，tokens 仅平台管理员）。
- **迁移 066–086 接线**：066 `scheduled_jobs`（`scheduler/postgres.go` 读写）、067 personas、068 `payments`（capture/webhook 双写）、070/071 invites、072 `agent_teams.permissions`（中间件解析 + 端点写）、073 embedding v2、074 `inbound_turn_ledger`（claim/refund）、076/078 删除、079 `file_ext` 与 080 `media_key`（后端读 + 前端类型/图标/图片预览均消费）、083 `memory_summary`（`platform/session_memory.go:37,68,119` 读+写）、084 `provider_message_id`（引用回复读写）、085 `followup_sent_at`（事务内写 + 扫描读）、086 `deleted_at/edited_at`（编辑撤回 + 读取侧过滤）——除第 2 项 `turn_metrics` 与已知的 061 RLS 脚手架外，均非僵尸。
- **构建基线**：`cd backend-go && go build ./... && go vet ./...` → BUILD_EXIT=0 / VET_EXIT=0。
- **默认关但已实现（dormant，不是未实现）**：`TTS_ENABLED`（Gemini TTS，需 R2）、`META_COMMENT_DM_ENABLED`（评论转私信，还依赖 Meta App Review）、`FOLLOWUP_HOURS=0`（解决后跟进）、`SAFETY_*`（内容安全默认关）——代码路径完整、有测试，只是默认不开。

## 不完整功能清单

> 状态：未实现｜半实现｜未接线｜默认关闭-only｜文档不符。P0=阻断成交/资金或数据错误；P1=客户/商家可感知；P2=产品化/技术债。

### 1. 「通知偏好」设置是空开关（写进去，没有读者）
- **状态**：未接线（配置/列/端点/UI 齐全，无生产消费者）
- **代码证据**：
  - 列与端点：`backend-go/internal/migrations/migrations/034_user_preferences.sql:8` `ALTER TABLE users ADD COLUMN ... notification_pref ...`；`backend-go/internal/api/auth_handlers.go:451` `UPDATE users SET notification_pref = $1 ...`；`router.go:61-62` GET/PUT `/api/v1/auth/preferences`。
  - 设置页 UI：`frontend/src/app/settings/page.tsx:117-126` `all / escalations / none` 三档下拉，保存后 toast「已保存」。
  - 发送路径不读它：`backend-go/internal/api/api.go:72-89` `notifyUser`（handoff/SLA/负反馈/告警共用）与 `backend-go/internal/platform/pipeline.go:1456-1469` `notifyUser` 均只做 INSERT + 实时事件 + Telegram 推送，**全仓库再无第二处读 `notification_pref`**。
- **具体缺什么**：在两条 `notifyUser`（及 Telegram 推送）前按 kind 过滤：`none`→不落站内/不推送；`escalations`→只发 handoff/SLA 类。并补测试。
- **用户可感知影响**：商家把通知调成「无」后，交接请求、SLA 违约、负反馈告警仍照常弹出/推送——一个明确的「设置了但没用」的控件，容易导致漏接客户。
- **优先级**：P1；**工作量**：S（<1d）。

### 2. API keys：后端完整、企业版套餐当卖点，前端 0 入口
- **状态**：前后端契约不完整（后端能力无前端入口）
- **代码证据**：
  - 三个端点：`backend-go/internal/api/router.go:210-212` `GET/POST /api/v1/api-keys`、`DELETE /api/v1/api-keys/{id}`；实现 `misc_handlers.go:194,261,274`。
  - 认证真实生效：`backend-go/internal/api/middleware.go:233`「auth parses the Bearer credential (JWT or kcs_ API key)」、`:321` 按 `api_keys` 表校验。
  - 商业承诺：`backend-go/internal/usage/plans.go:51` `FeatureIntegrations = "bl.feature.apikeys"`，`:87-91` 企业版 Included 含它；`frontend/src/lib/i18n/dict-en.ts:1416` `"API keys (system integration)"`（中：API 密钥（系统对接））。
  - 前端零入口：`grep -r "api-keys\|apiKeys" frontend/src` → **0 命中**；无任何页面/组件可创建或吊销。
- **具体缺什么**：设置页或运营页加「API 密钥」卡（列表/创建一次展示/吊销/过期时间），复用 `widget-admin` 的 token 展示模式即可。
- **用户可感知影响**：企业客户为「系统对接」付费却无法获得密钥，只能联系平台手工造；这是**已售功能不可用**。
- **优先级**：P1；**工作量**：S–M（half–1.5d，含三语文案）。

### 3. 081 `turn_metrics` 只写不读
- **状态**：未接线（迁移 + 生产者，无消费者）
- **代码证据**：
  - 写：`backend-go/internal/platform/pipeline.go:598` `INSERT INTO turn_metrics (event_id, user_id, platform, media_kind, total_ms, slow_stages)`；清理：`backend-go/cmd/retention/main.go:103` `table: "turn_metrics"`。
  - 迁移意图：`backend-go/internal/migrations/migrations/081_turn_metrics.sql:5-9`「keeps the answer queryable over time — p95 by platform, the stages that moved after a change」；文档 §二十九 同口径。
  - 读：全仓库 `FROM turn_metrics` **0 条 SELECT**（无 API、无报表、无 CLI 查询）。
- **具体缺什么**：至少一个查询入口（平台分析页 p95 慢阶段，或 `cmd` 报表）；否则这张表只增加写盘与保留成本。
- **用户可感知影响**：运营看不到“哪一段慢”，文档承诺的观测能力不存在；但客户侧无直接感知。
- **优先级**：P2；**工作量**：S（一条聚合 SQL + 平台面板一行）。

### 4. CI 的「数据库/SQL 门禁」静默通过
- **状态**：关键路径无测试（门禁在 CI 里恒 skip）
- **代码证据**：
  - CI 后端只有：`.github/workflows/ci.yml:20-22` `go build ./...`、`go vet ./...`、`go test ./...`，**没有 postgres service、没有 DATABASE_URL、没有 SQLCHECK_REQUIRED**。
  - sqlcheck 的跳过逻辑：`backend-go/internal/sqlcheck/sqlcheck_test.go:109-118` `dsn := os.Getenv("DATABASE_URL")`；未设且无 `SQLCHECK_REQUIRED=1` 时 `t.Skip("DATABASE_URL not set — skipping...")`。
  - 同类跳过：`t.Skip` 全仓库 54 处（24 个文件），其中约 20 个测试文件按 `DATABASE_URL` 跳过（如 `api/billing_seats_test.go:24-33`、`api/member_permissions_test.go:39-46`、`api/platform_ops_test.go:100-108`、`api/list_scope_test.go:124-130`、`migrations/rls_backstop_test.go:27-37`、`llm/llm_db_test.go:20-27`）。
  - `SQLCHECK_REQUIRED=1` 只出现在部署手册的手工步骤：`deploy-khmer-ai-cs/SKILL.md:82-90`。
- **具体缺什么**：CI 起一个 postgres:17 + pgvector 服务，先 `go run ./cmd/migrate`，再 `DATABASE_URL=... SQLCHECK_REQUIRED=1 go test ./...`；否则“CI 绿”只覆盖不碰库的代码。
- **用户可感知影响**：SQL 列名/占位符写错、迁移与代码不同步这类 bug（项目史上两次线上事故）可一路合入并发布。
- **优先级**：P1；**工作量**：M（1d，含修可能暴露的失败）。

### 5. 前端 lint 基线是红的，且 CI 不跑 lint；前端 0 自动化测试
- **状态**：关键路径无测试 / 交付门禁缺口
- **代码证据**：
  - `cd frontend && npm run lint` → exit 1：`frontend/verify-app.js:1`、`frontend/verify-theme.js:1` `A require() style import is forbidden (@typescript-eslint/no-require-imports)`（两文件都被 git 跟踪：`git ls-files` 列出）。
  - CI 前端只 `npm ci` + `npm run build`：`.github/workflows/ci.yml:36-39`；`.pre-commit-config.yaml` 只有 gofmt/go vet/migration-mirror/tenant-scope，无前端钩子。
  - `frontend/package.json` scripts 只有 dev/build/start/lint（无 test）；`frontend/src` 下 0 个 `*.test.*/*.spec.*`。
- **具体缺什么**：修 2 个 lint error（改 ESM 或在 eslint 配置里 ignore 这两个脚本）并让 CI 跑 lint；前端至少给 `lib/api.ts` 的契约（fetch 路径/错误处理）与 `billing` 结账加测试（否则 2963 行收件箱、结账路径全靠人工）。
- **用户可感知影响**：lint 门禁长期红=无人再看；前端回归只能上线后发现。
- **优先级**：P1（lint 红+P2，0 测试为 P1）；**工作量**：lint S（0.5d）、前端测试 L（>3d 逐步）。
- *注*：`npm run build` 本身包含 TS 类型检查，故类型错误仍会挡构建；缺的是 lint 与运行行为测试。

### 6. 自动备份 / 外部监控 / 预发（staging）仍缺
- **状态**：未实现（交付层，文档 §十一 P1 仍成立）
- **代码证据**：
  - 仓库无备份脚本/定时器：`glob **/*backup*` 仅命中 `node_modules`；`deploy/ops/` 只有 `readiness-check.{sh,service,timer}`、`notify.sh`、`khmer-alert@.service`。
  - 备份只是提醒：`deploy-khmer-ai-cs/SKILL.md:287`「Postgres 备份: `pg_dump -d khmer_ai_cs` 定时跑」——没有 cron/timer 落地。
  - 机内探针存在但非外部监控：`deploy-khmer-ai-cs/ops/readiness-check.sh:1-35`（systemd timer 每 5 分钟 curl 127.0.0.1:8081/ready），`khmer-readiness-check.timer:3-11`；注释自承「Cloudflare answers from cache … nothing else notices」。
  - 无 staging：`deploy/SKILL.md:18-52` 只有单台生产主机 + Cloudflare；仓库无 staging 配置/分支发布流程。
- **具体缺什么**：`pg_dump` systemd timer + 保留/异机校验 + 恢复演练记录；外部 uptime（Cloudflare Health Check / 第三方探针）指向 `/ready`；一个预发环境或至少“发布前在备份库上跑 migrate+sqlcheck”的自动化。
- **用户可感知影响**：数据库损坏/误删只能靠人工 dump；整机故障外部无告警；坏版本只能直接上生产再回滚。
- **优先级**：P1（文档自评 P1）；**工作量**：备份 S–M、外部监控 S、staging L。

### 7. 退款/发票/试用/自动续费（收入侧，与 T3 交叉）
- **状态**：未实现（文档 §十一/§十二「尚未做」仍成立）
- **代码证据**：
  - 只处理一个事件：`backend-go/internal/api/billing_paypal.go:358-360` `if event.EventType != "PAYMENT.CAPTURE.COMPLETED" { ... "ignored" }`——`PAYMENT.CAPTURE.REFUNDED` 被 200 忽略。
  - 无试用/停服字段：`grep -r "trial_ends_at\|suspended_at"` → 0；无退款/发票/收据代码（`invoice` 仅注释）。
  - 一次性 30 天：`billing_paypal.go:45` `GREATEST(...) + INTERVAL '30 days'`，到期由 `sqlExpiredPaidPlans` 降 free；无 Billing Plans/订阅。
- **具体缺什么**：退款/争议事件处理（revoke 或人工队列）、发票/收据（PDF 或邮件）、试用期与催缴/停服、自动续费（PayPal Subscriptions）。
- **用户可感知影响**：买家退款后权益仍保留、无收据；试用/欠费生命周期只能人工；续费靠用户手动回访。
- **优先级**：P1（收入）；**工作量**：L。*（本条属计费域，建议以 T3 报告为主，此处仅作横切记录。）*

### 8. 知识库文档库硬上限 100 / 全局搜索 200，无分页
- **状态**：半实现（后端支持分页，前端固定取一页且无翻页）
- **代码证据**：
  - `frontend/src/app/knowledge/page.tsx:237` `const response = await listKnowledge(1, 100);`；页面无任何分页控件（grep page/loadMore 只在注释）。
  - 顶栏全局搜索：`frontend/src/components/layout.tsx:214` `listKnowledge(1, 200)`，注释自承「a tenant's library is tens of rows」。
  - 后端支持 page/page_size：`backend-go/internal/api/knowledge_handlers.go:345-358`。
- **具体缺什么**：文档库分页/加载更多（或服务端搜索，见 #9）；超出 100 篇的租户目前**看不到也搜不到**第 101 篇起。
- **用户可感知影响**：文档增长到 >100 后“消失”（数据仍在、索引仍生效，但控制台不可见/不可编辑），排障极难。
- **优先级**：P2；**工作量**：S（复用现成 page_size 参数 + 翻页）。

### 9. 「文档内容搜索」仍是前端本地过滤（后端 `GET /knowledge` 无 `q`）
- **状态**：文档不符 / 半实现（与文档自述一致，但与“搜索”产品语义不符）
- **代码证据**：`backend-go/internal/api/knowledge_handlers.go:345-358` 只解析 `page/page_size`；前端 `frontend/src/app/knowledge/page.tsx:295` 用 `title.toLowerCase().includes(...)` 本地过滤；顶栏搜索也只匹配标题/分类：`layout.tsx:236` `doc.title... || (doc.category ?? "").includes(needle)`（不含正文）。
- **具体缺什么**：`GET /knowledge?q=`（title+content ILIKE/trgm，带租户列与 SQL 门禁）或改走 RAG 检索。
- **用户可感知影响**：文档正文里的关键词搜不到；只能靠记忆翻分类。
- **优先级**：P2；**工作量**：M（SQL + handler + 前端 + sqlcheck）。

### 10. Email / Voice(SMS) 渠道：只有配置和状态卡，零实现
- **状态**：未实现 / 配置误导（文档 §六 已自承“邮件渠道 config.go 有 SMTP 字段，零代码”）
- **代码证据**：
  - 配置：`backend-go/internal/config/config.go:252-261` 读 `EMAIL_ENABLED/SMTP_HOST/EMAIL_INBOUND_DOMAINS/VOICE_ENABLED/TWILIO_ACCOUNT_SID/TWILIO_FROM_NUMBER`。
  - 状态外显：`backend-go/internal/api/analytics_handlers.go:309-315` 把它们放进 integrations status；`frontend/src/components/admin/enterprise-tab.tsx:329` 渲染 `Email / Voice-SMS / SSO` 三个状态灯。
  - 零实现：`grep -r "net/smtp\|smtp\.|sendmail"` → 0；`TWILIO_AUTH_TOKEN` 全仓库无读取点；`cfg.Email`/`cfg.Voice` 除状态卡外无消费者（`grep "cfg.Email\|cfg.Voice"` 仅 config+analytics）。
  - `.env.example:290-302` 以「Enterprise channels (optional)」列出，其中 `SMTP_PORT/SMTP_USERNAME/SMTP_PASSWORD/SMTP_FROM/TWILIO_AUTH_TOKEN` **连 config 都不读**。
- **具体缺什么**：要么实现（SMTP 发信/收信 webhook、Twilio SMS 发送 + 能力表声明），要么从状态卡与 .env 模板移除，避免运营以为自己开了渠道。邀请链接也因此只能手工传播（无邮件投递）。
- **用户可感知影响**：运营配了 `EMAIL_ENABLED=true` 后状态卡显示 enabled，但没有任何邮件进出、邀请也发不出去。
- **优先级**：P2；**工作量**：实现 L，清理 S。

### 11. `.env.example` 有 11 个无消费者的僵尸键
- **状态**：未接线（配置项存在，无生产代码）
- **代码证据**（键名在非测试 Go 源码中**字面零出现**）：
  - `REPLY_CACHE_ENABLED / MIN_SIM / TTL_HOURS / MAX_PER_USER / MIN_RUNES`：`.env.example:261-266`；对应功能已被 `backend-go/internal/migrations/migrations/076_drop_reply_cache.sql` 删除。
  - `SMTP_PORT / SMTP_USERNAME / SMTP_PASSWORD / SMTP_FROM`：`.env.example:293-296`（见 #10）。
  - `TWILIO_AUTH_TOKEN`：`.env.example:301`。
  - `FRONTEND_PORT`：`.env.example:269`（部署用 3001 硬编码在 systemd unit，见 SKILL.md:143）。
- **具体缺什么**：删除这 11 个键，避免运维照模板配置却毫无效果（尤其 `REPLY_CACHE_*` 会让人以为回复缓存还在）。
- **用户可感知影响**：运维侧误导；无直接客户影响。
- **优先级**：P2；**工作量**：S（<0.5d）。

### 12. 新功能的 env 未写进 `.env.example`（至少 22 个）
- **状态**：文档不符（模板落后于代码）
- **代码证据**（Go 读取点有、`.env.example` 无，含注释行）：
  - `SAFETY_ENABLED/SAFETY_KEYWORDS/SAFETY_MODEL_ENABLED/SAFETY_INBOUND_ACTION/SAFETY_MODEL_MIN/SAFETY_JUDGE_BUDGET_MS`（`internal/platform/safety.go:44,178-190,266`）——文档 §十一 自承「6 个键尚未写入 .env.example」，**仍成立**。
  - `T2I_ENABLED/T2I_ENDPOINT/T2I_TOKEN/T2I_WORD_THRESHOLD/T2I_TIMEOUT_MS`（`internal/platform/t2i.go:67-81`，文字转图开关，模板完全没提）。
  - `META_COMMENT_DM_ENABLED`（`config/config.go:249`）、`FOLLOWUP_HOURS`（`api/followup.go:27`）、`SCHEDULER_TZ`（`api/tasks.go:93`）、`JEV_RULE_CONFIRM_MIN / JEV_RULE_SOLO_MIN / JEV_TURN_ESCALATE_MIN / JEV_GUARD_MIN / JEV_GUARD_HANDOFF_MIN / JEV_NOTIFY_WORTH_MIN / JEV_CONTRADICTION_MIN`、`GEMINI_PROVIDER_FALLBACK`（provider.go:200）。
- **具体缺什么**：把这些键补进模板（默认关的注明“默认关，需 XX 才生效”），特别是 SAFETY_*（B2B 签约常见要求）。
- **用户可感知影响**：新部署按模板配置永远打不开内容安全/跟进/评论转私信；只能读源码。
- **优先级**：P2；**工作量**：S。

### 13. `PUT /api/v1/billing/plan` 的配额表与 `usage.Plans` 不一致（且与注释矛盾）
- **状态**：半实现 / 契约漂移
- **代码证据**：
  - 单一事实源：`backend-go/internal/usage/plans.go:60-92`（free 200 msg / 10 docs / 1 channel / 1 seat；pro 5000 / 300 / 3 / 5；enterprise Unlimited）。
  - 正确实现：`backend-go/internal/api/platform_admin_handlers.go:220-242` `applyPlan` 注释「The quotas come from usage.Plans … shared by setPlan and setTenantPlan」，实现确实用 `usage.PlanByName`。
  - 另一条路径却硬编码旧数字：`backend-go/internal/api/misc2_handlers.go:623-629` `msgQ, docQ := int64(500), int64(20)` / `case "pro": 5000, 500` / `enterprise: 1<<62`，并直接 UPDATE `tenant_billing`，**没有调用 `applyPlan`**。
  - 前端会走这条路径：`frontend/src/components/admin/growth-tab.tsx:42,58`（仅 platform_admin 可见）、`:229-236` `await setPlan(p)` → `api.ts:1177` `PUT /billing/plan`。
- **具体缺什么**：`setPlan` 改为调用 `applyPlan(user.Tenant(), plan)`（或删除该路由，只留 `/platform/tenants/{id}/plan`）。否则平台管理员在“增长→计费”里把自己的租户升 pro，会拿到 500 篇文档配额（应为 300），free 会拿到 500 消息/20 文档（应为 200/10）。
- **用户可感知影响**：控制台显示配额与套餐卡/配额门禁不一致，配额判断随之漂移；仅影响平台管理员自有租户与测试租户，但属“两套数字”。
- **优先级**：P2；**工作量**：S。*（计费域，建议 T3 交叉确认。）*

### 14. `/platform-admin` 页面没有前端门禁，非平台管理员可直接输 URL 进入空壳页
- **状态**：半实现（权限一致性缺口；后端仍安全）
- **代码证据**：`frontend/src/app/platform-admin/page.tsx:42-58` 直接渲染 `PageHeader + Tabs`，无 `user.role === "platform_admin"` 判断、无 `PermissionRequired`（`grep "isPlatformAdmin" frontend/src/app` → 0）；导航入口只在 `layout.tsx:470-474` 按 `isPlatformAdmin` 隐藏。所有面板调用的是 `platformAdminOnly` 接口（`router.go:331-356`），非管理员看到的是各个面板的 403 toast/空态。
- **具体缺什么**：页面级 403 占位（与 admin-pages 的 `PermissionRequired` 同款），或让 AuthGuard 统一按路由表判断。
- **用户可感知影响**：普通店主/席位误入 /platform-admin 会看到一堆报错，像是系统坏了；后端不会泄露数据。
- **优先级**：P2；**工作量**：S（<0.5d）。

### 15. 后端遗留：`/chat/stream` 与 chat sessions 的 3 个端点没有前端消费者
- **状态**：疑似未接线（需生产流量确认是否被外部 API-key 客户端使用）
- **代码证据**：`backend-go/internal/api/router.go:120-127` 注册 `POST /chat`、`POST /chat/stream`、`GET/POST /chat/sessions`、`GET/PATCH/DELETE /chat/sessions/{id}`、`GET .../messages`；前端只有 `POST /chat`（`api.ts:429`）、`DELETE /chat/sessions/{id}`（`:812`）、`GET .../messages`（`:816`），**`/chat/stream`、`GET/POST /chat/sessions`、`GET /chat/sessions/{id}` 在全前端 0 命中**（grep `chat/stream`、`createSession`、`listSessions` → 0）。文档 §六 曾称这些是“前端零消费者导出，是现成 API 客户端层”，但相应导出已从 `api.ts` 删除。
- **具体缺什么**：要么确认它们是外部 API-key 客户端的公开面并写入文档（路径/鉴权/示例），要么删除；`nginx` 还专门为 SSE 关 buffering（SKILL.md:146），但站内没有任何 SSE 调用者。
- **用户可感知影响**：若被外部客户使用则无影响；否则是无人维护的公开面（鉴权、限流、SQL 变更都会漏测）。
- **优先级**：P2；**工作量**：S（决策+文档或删除）。

### 16. 控制台语音输入仍无录音 UI（文档 §十一 P2 仍成立）
- **状态**：未实现（明确记录的缺口）
- **代码证据**：`grep -r "MediaRecorder\|getUserMedia\|navigator.mediaDevices"` 于 `frontend/src` → 0 命中；只有播放向的 `components/ui/voice-message-bubble.tsx`。后端 ASR 仅服务入站：`backend-go/internal/platform/pipeline.go:793-810` `transcribeAudio`（webhook 侧），无“控制台上传录音”端点（`router.go` 无 `/chat/voice`，文档已注明该端点随清理删除）。
- **具体缺什么**：浏览器录音 + multipart 上传 + 复用 `TranscribeAudio`；或明确不做并从帮助文档移除暗示。
- **用户可感知影响**：客服只能打字/转写，不能直接对客户语音留言一键转文字。
- **优先级**：P2；**工作量**：M。

### 17. 文档声明/配置细节过时（低影响，汇总）
- **状态**：文档不符
- **代码证据**：
  - `deploy-khmer-ai-cs/SKILL.md:171`「仓库 `.gitignore` 里**没有**对应规则」——实际 `.gitignore:39-45` 已有 `*-sa.json / vertex-sa.json / service-account*.json` 三条规则。
  - `docs/DEVELOPMENT.md:1412`「该类文件共 59 个键」——`.env.example` 现在 65 个生效键（含注释共 125 个变量名）。
  - `docs/DEVELOPMENT.md:340`「前端 13 个零消费者导出」——实测 `api.ts`/`billing-api.ts` 137 个导出**没有一个**只有定义无外部引用（`streamChat/chatVoice` 等已删除）。
- **具体缺什么**：随下次文档更新一并改；不影响功能。
- **优先级**：P2；**工作量**：S。

## 文档声明核对表

| # | 文档声明（docs/DEVELOPMENT.md / 部署手册） | 现状 | 代码证据 |
|---|---|---|---|
| 1 | §十一:1168「缺口①收不到钱（无支付/订阅/自助升降级）」 | **已过时**（一次性 PayPal 已上线；订阅/降级仍无） | `billing_paypal.go:222-320` 下单/捕获；`frontend/src/app/billing/page.tsx:76-93`；§十二 即为上线记录 |
| 2 | §十一:1168「无自动备份、无预发、无外部监控」 | **仍成立** | 仓库无备份脚本/cron；ops 只有机内 readiness timer（`ops/khmer-readiness-check.timer`）；单主机部署 `SKILL.md:18-52` |
| 3 | §十一:1189 / §三十六「6 个 SAFETY_* 开关未写入 .env.example」 | **仍成立** | `.env.example` 无 SAFETY；`safety.go:44,178-190,266` 实际读 6 个 |
| 4 | §十一:1214「收件箱：…归档、批量」 | **仍成立** | 前端批量关闭/指派：`frontend/src/app/inbox/page.tsx:295-296,496-504`（`bulkMode/bulkClose`） |
| 5 | §十一:1237「API keys 已实现（安全）」 | **文档不符** | 后端 3 端点 + Bearer 认证在（`router.go:210-212`、`middleware.go:233,321`），前端 0 入口（见清单 #2） |
| 6 | §十一:1245「营销：活动（`marketing_campaigns`）」 | **已过时** | 迁移 `078_drop_marketing_campaigns.sql` 已删表，全仓库无读写 |
| 7 | §十一:1257 / 1274-1276「备份=手动 dump（缺口）」 | **仍成立** | `SKILL.md:287` 只是提醒；仓库无自动化 |
| 8 | §十一:1266「无订阅自动续费/自助升降级；退款/争议、发票/收据、税费全无」 | **仍成立** | `billing_paypal.go:358` 只处理 CAPTURE.COMPLETED；`grep trial_ends_at\|suspended_at`=0；无 invoice/refund 代码 |
| 9 | §十一:1285「知识库分类编辑：控制台无输入框」 | **已修复** | `frontend/src/components/knowledge/markdown-editor.tsx:151-162` 分类+标签输入；`upload-dialog.tsx:209-211,248-250` 上传分类；`knowledge/page.tsx:764` 展示徽章 |
| 10 | §十一:1286「文档内容搜索：后端 `GET /knowledge` 无 `q`，前端本地过滤」 | **仍成立** | `knowledge_handlers.go:345-358` 只读 page/page_size；`knowledge/page.tsx:295` 本地过滤 |
| 11 | §十一:1290「控制台语音输入：前端无录音 UI」 | **仍成立** | 零 `MediaRecorder/getUserMedia`；后端仅入站 ASR |
| 12 | §十一:1291 / §二十九 / §三十六「RLS 061 已建策略但未设 `app.user_id`，fail-open」 | **仍成立** | `cmd/server/main.go:54-81` 启动告警「RLS tenant backstop (061) is inert: no production code sets the app.user_id GUC」 |
| 13 | §十一:1292「前端构建若在脏工作树执行会带入未入库代码」 | **仍成立**（流程风险，无代码可修） | 部署手册 step 0 版本戳核对 `SKILL.md:56-77` |
| 14 | §十一:1227 与 §十二:1390「free 500/20、pro 5000/500」 | **已过时** | `usage/plans.go:60-92` 现为 free 200/10、pro 5000/300；§十二:1390 的 300 与代码一致 |
| 15 | §二十九:2501-2509「明确没做：RLS 接线 / KB 排序加权 / 多产品追问 / HUMAN_AGENT 权限 / 摘要预生成」 | **仍成立** | `main.go:81`；HUMAN_AGENT 仍需 App Review（`capabilities.go`/policy 注释）；摘要仍现场生成（`inbox_handlers.go` summary 缓存未命中路径） |
| 16 | §三十二:2669-2676「明确没做：相册/多图、编辑/撤回、Telegram Business」 | **部分过时** | 多图/相册已实现（§三十四，`media_items`，`api.ts:290` 类型）；编辑/撤回已实现（§三十三，`router.go:144-145`、`inbox_handlers.go:456-558`）；Telegram Business 仍无 |
| 17 | §三十六:2825「明确未修：RLS 接线、注册用户名/邮箱枚举」 | **仍成立** | 同上；枚举行为见 `auth_handlers.go` 冲突分支 |
| 18 | `.env.example:1-15`「无 docker-compose.yml、bare-metal」 | **仍成立** | 仓库无 compose；`frontend/Dockerfile` 存在但部署走 systemd |
| 19 | §六:340「前端 13 个零消费者导出（现成 API 层，非缺陷）」 | **已过时** | 137 个导出无零消费者；`streamChat/chatVoice` 已删，后端 `/chat/stream` 反成无消费者（清单 #15） |
| 20 | §十一:1319「迁移当前 67 个」 | **已过时** | `backend-go/internal/migrations/migrations/` 实际 86 个（001-086） |

## 未能核实的项（及原因）

1. **`/chat/stream` 与 chat sessions 端点是否有外部消费者**：仓库内无调用者；需生产访问日志/API-key 客户端清单才能判定“遗留”还是“对外接口”。
2. **生产是否真的没配 Email/Voice**：`.env-go` 在服务器上（不进仓库），无法确认 `EMAIL_ENABLED/VOICE_ENABLED` 是否为 true；代码侧零实现已确认。
3. **PayPal live 模式/退款是否人工处理**：需要生产 `payments` 行与 PayPal 后台；代码侧只处理 CAPTURE.COMPLETED 已确认。
4. **文档量是否已超过前端 100 条上限**：文档记录生产 42 篇（§十一:1315），当前值需查生产库；上限风险已被代码确认。
5. **Khmer 文案母语质量**：三语键集一致（1378/1378/1378）已核实，但译文是否准确需母语者复核（文档 §十一 P1 亦自承）。
6. **备份/监控的实际部署状态**：仓库无自动化脚本已确认；服务器 crontab/timer 与是否存在外部探针需上机查看。
7. **平台管理员直接输 `/platform-admin` 的真实观感**：结论由代码推得（无前端门禁 + 所有接口 platformAdminOnly），未在运行环境实拍。
