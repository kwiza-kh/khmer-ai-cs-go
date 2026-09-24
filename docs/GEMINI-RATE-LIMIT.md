# Gemini 消费速率限制（spend-based rate limit）排查与解决

面向运维与后续开发者。本文记录**一次真实的生产失效**：从症状、证据链、Google 官方规则，到可执行的解法与代码位置。

> ⚠️ **数据说明**：本文所有数字来自 2026-09-24 的一次生产压测与库内实测，
> 不是估算。但**账户档位（Tier）本身未经官方页面确认**——它是从"失效点 RPM ×
> 单轮成本"反推出来的（见 §3），吻合度很高，仍建议到 AI Studio 页面核对一次。

---

## 一、症状

压测加压到 10 租户 / 150 并发时，`POST /api/v1/chat` 开始返回 500：

```
500 {"error":"生成回答失败"}
```

| 阶段 | 并发 | 请求 | 失败 | RPM | p50 |
|---|---|---|---|---|---|
| 5 租户 | 60 | 826 | 0 | 416 | 4,043 ms |
| 10 租户 | 150 | 1,807 | 246（13.6%） | **1,075** | 4,535 ms |
| 20 租户 | 300 | 3,636 | 2,723（74.9%） | 2,171 | 4,430 ms |

**关键判据：失败时服务器完全没到极限**——CPU 仍空闲 96%，load1 仅 1.43（4 核），内存余 7GB，后端进程峰值只用了 **9.5% CPU / 73MB 内存**。同时 p50 延迟没有恶化（4,043 → 4,430 ms），说明**不是本机过载、也不是超时**，而是被上游明确拒绝。

---

## 二、根因

`gemini.Client` 拿到的真实响应是：

```
429 You exceeded your spend-based rate limit. Your spending rate has exceeded
    the allowed limit for your account's billing history and tier.
    status: RESOURCE_EXHAUSTED
```

直接探测（一句 `hi`）同样返回 429，证明这是**账户级**限制，与请求内容、并发模型无关。

### 排查为什么多花了一轮

`chat_handlers.go` 原本把上游错误**整个丢弃**：

```go
result, err := a.Gemini.Chat(r.Context(), message, history, language)
if err != nil {
    return nil, ErrInternal("生成回答失败")   // err 被扔掉
}
```

`gemini.go` 明明构造了 `gemini returned HTTP 429: {...}`，但三处调用点（`chatPlain` / `ragQuery` / `widgetChat`）全部丢掉。**2,969 次失败，日志里查不出任何原因。** 该缺陷已修复（提交 `53f4465`），修复后同一故障立刻定位。

---

## 三、为什么天花板是 $10 / 10 分钟

Google 官方对消费速率限制的规则（[rate-limits](https://ai.google.dev/gemini-api/docs/rate-limits)）：

| 档位 | 消费速率上限（每 10 分钟） | 升级条件 | 账单档位上限 |
|---|---|---|---|
| 免费 | 无 | 激活项目 / 免费试用 | — |
| **Tier 1** | **$10** | 绑定有效结算账户 | $250 |
| **Tier 2** | **$50** | **累计消费 $100 + 首次成功付款后 3 天** | $2,000 |
| **Tier 3** | **$200** | **累计消费 $1,000 + 首次成功付款后 30 天** | $20,000–$100,000+ |

**判定用的是排除法**，不是单点拟合：

库内实测单轮平均成本 **$0.001042**（909 轮全量），`gemini-3.8-flash` 为 **$0.001065**。据此，各档位理论上能支撑的速率是：

| 档位 | 消费上限 | 按 $0.00104/轮 可持续 | 与实测是否相符 |
|---|---|---|---|
| 免费 | 无消费限制，但有极低 RPM/RPD | — | ❌ 账户实测跑过数千请求 |
| **Tier 1** | $10 | **≈ 962 RPM** | ✅ **与实测失效点吻合** |
| Tier 2 | $50 | ≈ 4,808 RPM | ❌ 我们从未接近 |
| Tier 3 | $200 | ≈ 19,231 RPM | ❌ 同上 |

实测：Stage C（**416 RPM**）全部通过，Stage D（**1,075 RPM**）开始失败——失效点落在 416–1,075 之间。**只有 Tier 1 的 ≈962 RPM 落在这个区间内**，Tier 2/3 分别高出一个数量级，不可能触发。故判定账户在 **Tier 1**。

> 注：压测时 22 个租户中只有 1 个持有知识库，多数轮次没有 RAG 注入，因此**实测期的平均单轮成本应低于** $0.00104（该值是含厚知识库问答的全量均值）。若实测成本更低，Tier 1 的阈值会**高于** 962 RPM，与"1,075 RPM 附近开始失败"依然自洽。

### 几个容易踩的细节

- **限额按「项目（project）」计算，不是按 API key。** 同一个项目换 key 无用。
- **评估窗口是滚动 10 分钟**，不是自然分钟。所以它是**限流不是封禁**：加压于 06:14 停止，最后一次 429 记录在 **06:17:08**，而 06:21:47 已有成功生成（`token_usage` 实证）——即**停止加压后约 8 分钟内恢复**。恢复快慢取决于窗口内累积的消费额，不是固定值。
- 升级到 Tier 2/3 判定的是**该结算账户在全部 Google Cloud 服务上的累计支出**（不只 Gemini）。如果你在这个账单上有其他 GCP 消费，可能已经接近 $100。
- 达标后**自动升级**，10 分钟内生效。
- 另有官方的[提额申请表](https://forms.gle/ETzX94k8jf7iSotH9)，Google 明确不保证批准。
- 附加限制：Priority inference 默认为标准限额的 **0.3 倍**；Batch API 有**独立**额度（Tier 1 并发批次 100）。

---

## 四、解法一：升档（根本解，需账户持有人操作）

Tier 2 把天花板从约 1,000 RPM 抬到约 5,000 RPM，是实测最坏情况（2,171 RPM）的 2 倍以上余量，**且不需要改任何代码**。

- 查看当前档位：<https://aistudio.google.com/projects> 与 <https://aistudio.google.com/rate-limit>
- 若累计消费未达 $100：补一笔，**3 天后自动升 Tier 2**
- 同时提交提额表（免费、立即，但无保证）

---

## 五、解法二：降本（不需等待，代码改动）

### 5.1 缓存目前**完全没生效**——最大的浪费

库内实测（`token_usage` 全量）：

| 指标 | 数值 |
|---|---|
| 累计轮次 | 909 |
| prompt tokens | 2,146,541 |
| completion tokens | 123,160 |
| **cached tokens** | **0** |
| **缓存占比** | **0.0%** |
| 平均 prompt / completion | 2,361 / 135 |
| 累计成本 | $0.9472 |

**2,146,541 个 prompt token 全部按全价计费，零折扣。**

两个原因：

1. **隐式缓存够不着门槛。** Gemini 2.5+ 的隐式缓存对**稳定前缀**生效，但前缀需达到模型的最小可缓存长度。当前 system prompt 只有几百 token，而 RAG 内容与用户消息每轮都变——**没有任何一段足够长且稳定的前缀**。
2. **显式缓存没实现。** `model_configs.context_cache_ttl`（生产值 `3600`）**只被读写、从未发出去**：`admin_handlers.go:283` 写库，但 `gemini.go` 构造请求体时不含任何 `cachedContent` 字段。这个设置目前是死的。

**建议**：
- 把每轮都要用的**稳定大块内容**（价格表、交期、条款等）从"每轮检索"挪进 system instruction——既缩小 RAG 载荷，又让前缀长到可缓存。
- 实现显式 `cachedContent`（缓存 token 通常便宜 75–90%，TTL 可设 1 小时）。DB 里的 `context_cache_ttl` 已经预留，属于半成品。

### 5.2 RAG 注入没有体积上限

`AugmentMessage`（`rag/service.go:1244`）直接拼接 `ctx.ContextStr`，**没有任何预算控制**，只受 `topK × 分块大小`约束。

知识库变厚后的实际影响：

| 时期 | 平均 prompt | 单轮成本 |
|---|---|---|
| 薄知识库（旧） | 2,361 | $0.00093 |
| 厚知识库（30 篇 / 621 块） | **3,400–4,200** | **$0.0013（+40%）** |

内容变好必然涨价，但可以压：
- `RAG_TOP_K` 5 → 3
- 给每个注入分块加上限——另一条路径已有 `truncateRunes(chunk, 400)`，chat 路径没有
- `max_tokens` 2048 → 512（生产配置值 2048，而实际 completion 均值仅 **135**）

### 5.3 回复缓存：命中省 65×，但认证接口是盲区

命中只需**一次 embedding（约 $0.00002）**，而一次完整生成约 $0.0013。

**当前接线状态**：

| 路径 | 是否使用回复缓存 | 位置 |
|---|---|---|
| Widget（网站挂件） | ✅ | `widget_handlers.go:486, 585` |
| 平台渠道（Meta/Telegram/LINE） | ✅ | `pipeline.go:514, 592` |
| **认证接口 `/api/v1/chat`** | ❌ **完全没用** | — |

`reply_cache` 表实测 **0 行**——因为真实流量与压测都走的是没接缓存的路径。把它接上，高频问法直接省 65×。

调参见 `replycache.go:58-61`：`REPLY_CACHE_MIN_SIM`（默认 0.92，偏严）、`TTL_HOURS` 72、`MAX_PER_USER` 500、`MIN_RUNES` 12。

### 5.4 对 429 重试纯属浪费

`postWithRetry`（`gemini.go:249-289`）对 429 重试 **3 次**，退避 `400ms / 800ms`。

对一个**滚动 10 分钟**的消费限制，1 秒内重试必然还是 429——只是把墙上时间翻三倍，并继续冲击 API。**应快速失败**（真正的瞬时抖动才值得重试一次，且退避要长）。

---

## 六、解法三：架构层面的容量扩展

- **多 GCP 项目**：限额按项目算，同一结算账户下多个项目各有独立额度。合法，但增加密钥管理复杂度；**需实测确认**消费上限是否也按项目独立。
- **Vertex AI**：换了配额体系（按区域 QPS 而非消费速率）。**不是改 `GEMINI_API_BASE` 就能切**——Vertex 需要 OAuth2 服务账号鉴权，URL 结构也不同（`{region}-aiplatform.googleapis.com/v1/projects/{p}/locations/{l}/publishers/google/models/{m}:generateContent`），`x-goog-api-key` 那套要改代码。
- **Batch API**：独立额度且约半价，但异步。适合知识库编译、摘要、翻译回填这类非交互任务——把它们移出交互预算，等于给实时对话腾额度。
- **Cloudflare AI Gateway**：已在使用（`GEMINI_API_BASE` 指向它）。它能做缓存与限流观测，但**不会提高 Google 上游的限额**。

---

## 七、解法四：不涨价也要不宕机（护栏）

当前失效形态是最糟的组合：**客户看到 500、运维看不到原因、没有任何告警**。

1. **429 时优雅降级**：返回「客服繁忙，正在为您转接人工」并**自动创建 handoff**，而不是裸 500。注意 RAG 检索路径在 429 时**已经**会降级为纯词法检索（`service.go` 的 `vector knowledge search failed; retaining lexical results`，实测 331 次），设计正确——生成环节应比照处理。
2. **加生成失败率告警**：复用 `platform/jev_health.go` 的 `PlatformAlert` 模式（Redis 去重 + Telegram 投递），对连续 N 次生成失败告警。
3. **可选：内部消费预算闸**。在撞上 Google 的墙之前主动限流，让客户拿到干净的"繁忙"提示，而不是随机的 500。

---

## 八、建议执行顺序

> **落地状态（2026-09-25）**：下表第 3–5 项已实现，见 §十一。第 1–2 项仍需账户持有人操作，
> 且**升档后必须同步 `GEMINI_SPEND_LIMIT_USD`**，否则本地闸门会在旧档位的阈值上提前放行转人工。

| 优先级 | 动作 | 执行方 | 见效 | 改动量 |
|---|---|---|---|---|
| 1 | 查档位 + 提交提额表 | 账户持有人 | 立即 | 无 |
| 2 | 补足 $100 累计消费 → 3 天后自动升 Tier 2 | 账户持有人 | 3 天，天花板 ×5 | 无 |
| 3 | 修 §5.1 缓存 + §5.4 快速失败 | 开发 | 立竿见影 | 小 |
| 4 | 把回复缓存接到 `/api/v1/chat`（§5.3） | 开发 | 高频问法省 65× | 小 |
| 5 | §七 护栏 | 开发 | 防止再次 500 | 中 |

**最省事的组合是 1+2**：Tier 2 到位后，实测最坏情况就有 2 倍余量，不用改代码。

但 §5.1 值得单独重视：**"缓存 0 生效"意味着一直在为 100% 的 prompt token 付全价**，这部分省下来可能比升档更划算；§5.4 则是纯粹的无效开销。

---

## 九、如何复核本文结论

```bash
# 1. 账户当前是否被限流（连一句 hi 都能验证是账户级限制）
set -a; . /opt/khmer-ai-cs/.env-go; set +a
curl -s -X POST \
  "https://generativelanguage.googleapis.com/v1beta/models/gemini-3.8-flash:generateContent" \
  -H "Content-Type: application/json" -H "x-goog-api-key: $GEMINI_API_KEY" \
  --data '{"contents":[{"parts":[{"text":"hi"}]}]}'

# 2. 单轮真实成本结构（判断距离 $10/10min 还有多远）
psql "$DATABASE_URL" -c "select count(*) turns,
  round(avg(prompt_tokens)) avg_prompt, round(avg(completion_tokens)) avg_completion,
  round(avg(cached_tokens)) avg_cached, round(avg(cost_estimate)::numeric,6) avg_cost
  from token_usage"

# 3. 缓存是否仍然零生效（cached 应显著大于 0，否则 §5.1 未解决）
psql "$DATABASE_URL" -c "select sum(prompt_tokens) prompt, sum(cached_tokens) cached,
  round(100.0*sum(cached_tokens)/nullif(sum(prompt_tokens),0),1) cache_pct from token_usage"

# 4. 回复缓存是否开始落数据（0 行 = 认证接口仍未接线）
psql "$DATABASE_URL" -c "select count(*), coalesce(sum(hit_count),0) from reply_cache"
```

**换算公式**（窗口是 10 分钟，所以要乘 10）：

```
可持续 RPM = 消费上限($) ÷ (10 × 单轮成本($))
```

按厚知识库单轮成本 **$0.0013** 计算：

| 档位 | 上限 | 可持续 RPM |
|---|---|---|
| Tier 1（当前） | $10 | **≈ 769** |
| Tier 2 | $50 | ≈ 3,846 |
| Tier 3 | $200 | ≈ 15,385 |

这解释了为什么失效点落在 1,075 RPM 附近：压测时只有 1/22 的租户持有知识库，**平均单轮成本低于 $0.0013**（多数轮次没有 RAG 注入），所以实测阈值比 769 高。若全部流量都是接地问答，阈值会更低。

**反过来说**：把 §5.2 的注入体积压下来（topK 5→3、输出上限收紧），等于**直接抬高可持续 RPM**——降本与扩容是同一件事。

---

## 十、一句话总结

> 服务器不是瓶颈（峰值只用 9.5% CPU）。瓶颈是 **Gemini 账户的 Tier 1 消费速率上限 $10/10 分钟**，
> 而**缓存零生效**意味着每一分钱都按全价在烧。**先升 Tier 2（不用改代码），同时把缓存和无效重试修掉**，
> 就能同时拿到 5 倍天花板和更低的单位成本。

---

## 十一、已落地的改动（2026-09-25）

以下改动都在本文档的同一分支上完成，`go build ./... && go test ./...` 全绿。除标注外均**默认生效**。

### 11.1 无效重试已消除（§5.4 + 队列层）

| 位置 | 改动 |
|---|---|
| `gemini.go postWithRetry` | 429 **不再重试**，直接带状态和响应体返回；5xx 与传输错误仍重试 3 次 |
| `gemini.go chatWithModel` | 降级到快模型的条件加上 `fast != model`：未设 `GEMINI_FAST_MODEL` 时二者同名，旧逻辑等于把刚被拒的请求重发一遍 |
| `pipeline.go processInboundEvent` | 配额类错误**不再走 5 次队列重试**（5s/15s/60s/300s/900s，10 分钟窗口内的必然全败）：改为告警 + 转人工 + 给客户回执，事件直接置 `completed` |

单回合在最坏情况下的上游请求数从 6–9 次降到 1 次。测试：`TestPostWithRetryFailsFastOn429`、`TestPostWithRetryStillRetries5xx`。

### 11.2 429 时的优雅降级（§7.1）

- **平台渠道**（`pipeline.go`）：配额耗尽 → `PlatformAlert` 告警 + `escalateToHuman(ai_decision)` + 客户收到 `HandoffAcknowledgement`，会话进入人工队列。
- **网站挂件**（`widget_handlers.go`）：同上；已流出的半截内容照常入库，随后补一条转人工说明而不是抛错。
- **认证接口** `/api/v1/chat`、`/chat/stream`（`chat_handlers.go`）：这是租户自测台，转人工无意义，因此只告警 + 记日志（此前该路径**连日志都没有**）+ 返回可重试错误。

### 11.3 回复缓存接入认证接口（§5.3）

`chatPlain` 与 `chatStream` 都接上了 `replycache`：命中直接回放（流式路径把整段答案作为单个 token 事件发出，前端协议不变），生成成功后异步写回。写回在正文定稿之后、跳过 mock 回复，与 widget / 平台渠道一致。

### 11.4 small_talk 零成本（新增，文档未提）

Jev 判定 `small_talk` 且概率过阈时，不再调用模型，改由多语言模板直接作答（回执文案可改，`JEV_CHITCHAT_CANNED=0` 可关闭）。问候与道谢是系统里最重复的回合类型，这一类流量现在成本为 0；平台渠道和挂件都走同一开关（`platform.SmallTalkCanned`）。

### 11.5 注入体积与输出上限（§5.2）

| 项 | 改动 |
|---|---|
| 注入总预算 | `Ground` 新增 `RAG_CONTEXT_BUDGET_RUNES`（默认 4800）：逐个来源分配，**排序靠前的来源拿满额度**，只裁尾部的第 3 个及以后 |
| `RAG_TOP_K` | 默认 5 → **3**（`DefaultTopK`，仍可 env 覆盖；`rageval` 用同一常量衡量 recall） |
| `GEMINI_MAX_TOKENS` | 默认 2048 → **1024**。注意：**上限不影响平均账单**（按实际产出计费，均值 135），它只限制失控长回答；生产实际值来自 `model_configs.max_tokens`，需在管理后台同步改 |

### 11.6 消费计量与准入闸（§7.3）

- `usage.Budget(ctx, db, redis)` — 对 `token_usage.cost_estimate` 求最近 10 分钟之和（Redis 缓存 20s），即 **Google 在量的同一个数字**。
- **闸门**：超过 `GEMINI_SPEND_LIMIT_USD × GEMINI_SPEND_GATE_RATIO`（默认 $10 × 0.85）时主动让路 —— 平台渠道与挂件转人工并告警，认证接口返回"繁忙稍后重试"。**在撞墙前放行，而不是撞墙后收 500。**
- **失败开放**：DB 或 Redis 不可用时视为"未超限"，计量故障绝不能阻断客户回复。
- 新增 `GET /api/v1/platform/spend`（platform_admin）返回窗口消费、上限、比例与闸门状态，供运维看板使用。
- ⚠️ **升到 Tier 2 后必须把 `GEMINI_SPEND_LIMIT_USD` 改成 50**，否则闸门会按 Tier 1 的阈值提前转人工。

### 11.7 思考 token 不再隐形

`usageFromValue` 现在把 `thoughtsTokenCount` 计入 completion —— 思考 token 按**输出价**计费却不计入库，这意味着 §三 反推 $10 档位所用的成本数字是偏低的，成本看板也一直少算。另新增 `GEMINI_THINKING_BUDGET` 旋钮（不设 = 模型默认；0 = 关闭思考，更快更省但难题质量下降）。

### 11.8 显式上下文缓存（§5.1）

已实现完整机制：`cachedContents` 注册、按 system instruction + 语言做缓存键、TTL 早一分钟过期、API 报缓存失效时**自动去掉缓存重试一次**（不让优化变成故障）、最小前缀守卫（`GEMINI_CACHE_MIN_TOKENS`，默认 1024）。

**默认关闭（`GEMINI_CACHE_TTL=0`）**，因为算下来它不一定划算：

```
缓存读取省  $0.27/1M（$0.30 → $0.03）
缓存存储    $1.00/1M/小时
盈亏平衡 ≈ 1.00 / 0.27 ≈ 3.7 次请求/小时（与前缀长度无关）
```

当前生产是"一天几十条消息"，远低于该门槛，开着就是净亏；压测或活动期间（数百 RPM）则收益巨大。**需要时设 `GEMINI_CACHE_TTL=3600` 即可启用**，代码路径已就绪。

### 11.9 未做（有意排除）

多 GCP 项目轮换、Batch API、Vertex AI —— 前两者需实测确认粒度/异步改造，Vertex 需服务账号鉴权与 `:predict` 嵌入重写，都不适合本轮。

### 11.10 复核方式

```bash
# 生效中的旋钮
grep -E "GEMINI_MAX_TOKENS|GEMINI_CACHE_TTL|GEMINI_SPEND_LIMIT_USD|JEV_CHITCHAT_CANNED" /opt/khmer-ai-cs/.env-go

# 闸门当前状态（也可用 /api/v1/platform/spend）
psql "$DATABASE_URL" -c "select round(sum(cost_estimate)::numeric,4) spent_10m from token_usage
  where created_at > now() - interval '10 minutes'"

# 小聊模板与缓存命中是否生效：model_name 直接写在消息行上
psql "$DATABASE_URL" -c "select model_name, count(*) from chat_messages
  where created_at > now() - interval '1 day' group by 1 order by 2 desc"
```
