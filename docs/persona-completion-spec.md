# 人格（persona）收口规格 — 2026-10-26

对应审计 P1-9「persona 半成品」。四条缺口，逐条给出**决策**（不是选项），实现按本文执行。

## 缺口与决策

### 1. conversation 作用域永不生效

**现状**：`persona.Resolve` 支持 session → conversation → global 三级，但唯一生产调用点
（`platform/inbound_stages.go` 的 `stageResolvePersona`）恒传 `""`，且库里根本没有
conversation 这个实体（sessions 表只有 session_id / platform / platform_user_id）。

**决策：conversation = 客户在某个渠道上的稳定身份**，定义

```
conversationID = "<platform>:<platform_user_id>"     例：telegram:123456789、web:9f2c…
```

- 跨 session 稳定：同一个客户开新会话，绑在 conversation 上的 persona 仍然生效（这才是
  "本次对话"区别于"本次会话"的意义）；
- `platform_user_id` 为空的历史行 → 退化为 `"<platform>:session:<sessionID>"`（不跨会话，但
  不报错、不串味）；
- 控制台会话（`sessions.platform IS NULL`，即 `/ai-test` 与后台 chat）→
  `"console:<sessionID>"`：控制台没有"同一客户"的概念，conversation 就等于 session，如实暴露。

实现要点：
- `internal/persona` 新增纯函数 `ConversationID(platform, platformUserID, sessionID string) string`；
- `stageResolvePersona` 传入真实 conversation id；
- 会话详情 API 带上 `conversation_id`，运营能直接复制去绑定（前端在会话页显示 + 可复制）。

> **实现后的精确规则**（以代码为准，`persona.ConversationID`）：
> - `platform` 为空（`/ai-test` 会话，`sessions.platform IS NULL`）→ `console:<sessionID>`；
> - `platform` 非空且 `platform_user_id` 非空 → `<platform>:<platform_user_id>`；
> - `platform` 非空但 `platform_user_id` 为空（历史行/未落库访客）→ `<platform>:session:<sessionID>`；
> - 三者都空 → `""`（不可寻址，直接落到租户默认）。
>
> 控制台普通会话（`/chat`、非 test）落库时 `platform='web'`，因此它的 conversation 是
> `web:session:<sessionID>` —— 与 inbox 列表里显示的是**同一个字符串**，运营复制的 id 一定
> 能绑上。`conversation_id` 出现在 `GET /inbox` 列表项、`GET /chat/sessions/{id}`、
> `POST /chat/sessions` 响应。

### 2. `/ai-test` 与网站挂件不应用人格

**现状**：人格只经 `ChatAs` 用在渠道入站（`inbound_stages.go` 的 `stageGenerate`）；
`/chat`（`chatPlain`）、`/chat/stream`（`chatStream`）、挂件（`widgetChat`）都直接
`serving().Chat/ChatStream`，用租户默认提示词。

**决策**：三条路径都按同一套三级解析取人格，并用它的 `system_prompt` + `begin_dialogs`：

| 路径 | 会话 | conversation |
|---|---|---|
| 渠道入站 | sessions 行 | `<platform>:<platform_user_id>` |
| 控制台 `/chat`、`/chat/stream`、`/ai-test` | 控制台会话行 | `console:<sessionID>` |
| 网站挂件 | 挂件会话行 | `web:<visitor_id>`（与入站同规则，落库在 platform_user_id） |

新增 `gemini.ChatStreamAs(...)`（流式版带 systemPrompt），与既有 `ChatAs` 同签名风格。
没有任何绑定时行为与现在**逐字节一致**（systemPrompt 传空 → 走租户提示词）。

### 3. `error_reply` 无消费者

**现状**：字段有 UI、有落库，全后端无人读。

**决策**：用在"客户最终收不到回复"的两处，且**只在这两处**：

1. **入站事件重试耗尽**（`pipeline.go:retryInbound` 把事件标 `failed` 的分支）：解析该事件
   会话的人格，若有 `error_reply` → 通过该渠道发给客户（走正常出站队列），并记一条
   `notifications`/日志说明"用了人格兜底文案"。没配 `error_reply` 的人格保持现状（只告警，
   客户无回复）——**不发明默认文案**，避免给所有租户凭空多一条消息。
2. **控制台 / 挂件生成失败**：返回该文案（控制台走既有错误信封的 `message`，挂件走聊天流
   里的错误帧），没有配置时保持现有错误文案。

不做：`error_reply` 不用于"配额耗尽/无回复"这两条已有人工接管的分支（那里已经有更好的
处理：转人工），也不用于内容安全拦截（那是产品行为，不是故障）。

### 4. `tools` 无消费者

**现状**：`personas.tools` 三态（NULL=全部、[]=无、白名单），API 收、库里存、UI 没有、执行器
没有。而我们的模型客户端（`internal/gemini`）**没有 function-calling 层**，所以"模型自己调
工具"这条路现在做不了，装了也是假的。

**决策：把 tools 落地为"这个人格被允许触发的平台能力"**，先只定义两个真实存在的能力：

| 能力名 | 含义 | 执行点 |
|---|---|---|
| `handoff` | 允许把会话转人工（**只限 AI 判断触发的路径**，见下） | `escalateToHuman` 的 `ai_decision` / `negative_feedback` / AI 自称转人工分支 |
| `image` | 允许长回复渲染成图片（T2I） | pipeline 里调用 `T2I` 的位置 |

**安全网不受 `tools` 控制**（人格调的是语气和行为，不能拔掉底线）：配额/余额耗尽、模型无回复或
空回复、媒体不可读、回复守卫（reply-guard）拦截、内容安全拦截、人工在控制台手动转人工——
这些一律照旧转人工，与人格无关。允许租户用 `tools: []` 关掉全部转人工，等于让客户投诉永远
到不了人，这条边界必须守住。

- `nil`（NULL）= 两个都允许 → **存量人格行为不变**；
- `[]` = 两个都禁（安全网除外）；
- 白名单 = 只允许列出的；
- 能力被禁时的行为：`handoff` 被禁 → AI 判断路径不转人工、正常回复（回复守卫仍会拦"承诺转
  人工"的话术）；`image` 被禁 → 发文本；

**HTTP 上的三态（前后端共同的契约）**：

| 意图 | 读（GET / 写响应） | 写（POST/PUT 请求体） |
|---|---|---|
| 全部允许（默认） | `"tools": null` | `null` 或字段缺省 |
| 全禁 | `"tools": []` | `[]` |
| 白名单 | `"tools": ["handoff"]`（固定顺序 handoff→image） | 数组，大小写/空白/重复会被规范化 |

读路径**必须直出 `null`**：历史实现把 nil 压成 `[]`，会让"没碰过这个开关"的人格显示成"全禁"，
运营一保存就真的降级（已修，并有往返测试钉住）。未知能力名 → 400：`未知的工具能力：search，可用：handoff、image`。
- **API 校验**：`createPersona` / `updatePersona` 收到未知能力名 → 400，报文列出可用集合；
- 顺序固定 `["handoff","image"]`，比较大小写不敏感、去空白、去重。

不做：假的 function-calling、给模型暴露平台 API。

## 兼容与验收

- 存量数据：`persona_bindings` 里已有的 conversation 绑定（target 任意字符串）从此可能**首次
  生效**；这正是修复目的，但要在发布说明里写清"此前 conversation 绑定是死的"。
- 验收（本地，无需 DB 的都要有测试）：
  1. `ConversationID` 三条规则（含空 platform_user_id 退化、console）；
  2. `Resolve` 三级优先级 + "conversation 绑上了就压过 global、但仍输给 session"；
  3. 无绑定时三条生成路径的 systemPrompt 为空（行为不变）——源码守卫；
  4. `error_reply`：配了就用、没配就保持原样（两条路径各一个测试）；
  5. `tools`：nil/[]/白名单三态 × handoff/image 两个门；未知能力名 400；
  6. 前端：tools 勾选框三态可保存可回读；三语键集一致。
- 门禁：`go build/vet/test ./...`、`npm run build`、`npm run lint` 全绿；`tenantscope` / `sqlcheck`
  静态门禁不许放宽。
