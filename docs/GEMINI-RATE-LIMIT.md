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
- **Vertex AI**：换了配额体系（按区域 QPS 而非消费速率）。**不是改 `GEMINI_API_BASE` 就能切**——Vertex 需要 OAuth2 服务账号鉴权，URL 结构也不同（`{region}-aiplatform.googleapis.com/v1/projects/{p}/locations/{l}/publishers/google/models/{m}:generateContent`），`x-goog-api-key` 那套要改代码。（**已开工，实测结论见 §十二**。）
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
  （计量口径与 provider 无关，一直有效；但**上限的含义**与 provider 强相关，见 §十三。）
- **闸门**：超过 `GEMINI_SPEND_LIMIT_USD × GEMINI_SPEND_GATE_RATIO`（默认 $10 × 0.85）时主动让路 —— 平台渠道与挂件转人工并告警，认证接口返回"繁忙稍后重试"。**在撞墙前放行，而不是撞墙后收 500。**
- **失败开放**：DB 或 Redis 不可用时视为"未超限"，计量故障绝不能阻断客户回复。
- 新增 `GET /api/v1/platform/spend`（platform_admin）返回窗口消费、上限、比例与闸门状态，供运维看板使用。
- ⚠️ **升到 Tier 2 后必须把 `GEMINI_SPEND_LIMIT_USD` 改成 50**，否则闸门会按 Tier 1 的阈值提前转人工。
  （这条只适用于 **studio**；`GEMINI_PROVIDER=vertex` 时该变量的含义完全不同，且这种"靠人记得改"的
  做法已被代码取代——见 §十三。）

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

多 GCP 项目轮换、Batch API、Vertex AI —— 前两者需实测确认粒度/异步改造，Vertex 需服务账号鉴权与 `:predict` 嵌入重写，都不适合本轮。（**Vertex 后来已开工，实测结论见 §十二**。）

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

---

## 十二、Vertex 迁移进展（实测结论，2026-09-25）

> §六 那条「**不是改 `GEMINI_API_BASE` 就能切**」已被实测证实：需要 OAuth2 服务账号 +
> 完全不同的 URL 形状。代码已就位（`internal/gemini/provider.go` 起，**默认仍是 studio**）。
> 只记四条影响决策的实测结论：

1. **`:predict` 不支持 task 条件化** —— 平台上的嵌入统一走 `:predict`，`taskType` 那套字段不被接受（3 个模型 × 5 种拼写，并做了双向对照），所以嵌入只有 `instances` + `parameters` 一种报文形状。
2. **亚洲区没有 lite 档** —— `gemini-2.5-flash-lite` / `gemini-3.5-flash-lite` 在亚洲各区**全部 404**。于是不再分主/辅档位：**全线 `gemini-3.5-flash`**（3.6 / 3.7 / 3.8 与任何 `-latest` 别名在平台上都不存在，浮动别名还会随上游漂移）。
3. **预览 TTS 在所有测试区域不可用** → `TTS_ENABLED` **保持关闭**（本就是默认值，平台没有可指向的 TTS 模型）。
4. **嵌入 `:predict` 返回 768 维，但必须显式发 `outputDimensionality`** —— 不发就是模型默认的 3072，而 `knowledge_chunks` 是 `vector(768)`。代码按 768 发，并对非 768 的响应直接报错（宁可失败，也不写入错维向量）。

配额口径随之改变（§六：按区域 QPS，而非消费速率）。**计量（`token_usage` / `usage.Record` /
窗口求和）与平台无关，迁移后照旧**；但 **§11.6 那条闸门的"上限"语义与平台强相关**——studio 下它
是 Google 的墙，vertex 下它只能是我们自设的预算。这个差异已在 `internal/usage` 与 §十三 里写死，
不再依赖运维记得改环境变量。

---

## 十三、消费护栏的语义重定义（增量 4，2026-09-25）

> §11.6 的护栏是为**对齐 AI Studio 的消费速率上限**而写的：`GEMINI_SPEND_LIMIT_USD` 默认 10，
> 就是 Tier 1 那堵 $10/10 分钟的墙（§三）。迁到 Vertex 后，那堵墙**不存在**了——Vertex 按
> 「项目 × 区域」的 RPM/TPM 配额计量，提额靠工单而不是靠多花钱，上游没有任何美元天花板可供对齐。
> 于是同一个变量在两种 provider 下含义不同，而**危险的恰恰是它看起来仍然在生效**。

### 13.1 同一个变量，两种含义

| | studio（AI Studio，当前生产） | vertex（Gemini Enterprise Agent Platform） |
|---|---|---|
| `GEMINI_SPEND_LIMIT_USD` 的含义 | **Google 的墙**在本地的一面镜子：Tier 1 $10 / Tier 2 $50 / Tier 3 $200（每 10 分钟） | **我们自己定的预算**，上游没有任何东西与它对应 |
| 变量未设时的默认值 | **10**（实测的 Tier 1 墙，§三） | **0 = 不设上限**（闸门与告警都不启用） |
| 触发时谁在施压 | 上游真会在窗口满时回 429 | 没有任何人；甩掉的是我们自己定的规则 |
| 判定式 | `limit × GEMINI_SPEND_GATE_RATIO`（默认 0.85） | 同左 |

窗口长度（滚动 10 分钟）、`token_usage.cost_estimate` 的求和口径、Redis 20s 缓存、比例旋钮，
以及**失败开放**（DB/Redis 故障一律视为未超限）在两种 provider 下完全一致——变的只有"上限从哪来"。

### 13.2 事故模型：不能靠运维记得改环境变量

危险配置只有一种：**provider 切成 vertex，而 `GEMINI_SPEND_LIMIT_USD` 还留着 studio 的档位数字**。
在这种误配下，护栏会在一堵不存在的墙前面继续甩客户的回合——客户被拒，而上游毫无压力；
`/api/v1/platform/spend` 汇报"接近上限"，日志一切安静，唯一症状是客户被转人工。**这就是增量 4
之前的形态**：护栏忠实地执行了它被告知的规则。

这与迁移 061 的 RLS 策略是**同一类失效**（见 `cmd/server/main.go` 的 RLS 警告与
`docs/DEVELOPMENT.md`「十」）：控制看起来是生效的，其实对谁都不生效。旧设计对这一风险的答案写在
§11.6 的一行提示里（"升到 Tier 2 后必须把 `GEMINI_SPEND_LIMIT_USD` 改成 50"）——也就是说，它依赖
运维在忙别的事情时**记得**去改一个环境变量。那不是控制，是祈祷，而 061 已经证明这类祈祷会落空。

因此护栏现在**拒绝猜**：

| provider | `GEMINI_SPEND_LIMIT_USD` | 生效上限 | 行为 |
|---|---|---|---|
| studio | 未设 | 10 | 与迁移前**逐字节相同** |
| studio | 任意（含解析失败） | 值本身 / 回退 10 | 同上（保留历史的宽松解析） |
| vertex | 未设、空、0、负数 | 0 | 闸门关闭；不设上限 |
| vertex | **10 / 50 / 200** | 0（**拒绝**） | 视为 AI Studio 遗留档位：不武装闸门 + 启动自检报错 + 运行期 ERROR 日志 |
| vertex | 非数字 | 0（**拒绝**） | 同上（"运维以为有预算，其实没有"） |
| vertex | 其它正数（如 25） | 值本身 | 视为**明确的自设预算**，闸门照常工作，日志说明"这是我们自己定的" |

> **为什么是"拒绝"而不是"照用 + 告警"**：照用意味着事故照旧发生（客户继续被拒），只是日志里多了
> 一行。拒绝让"无意义的阈值"在物理上无法拒绝客户——**即使启动自检那行接线没做（见 13.4）**。
>
> **为什么恰好是这三个数字**：10/50/200 是 AI Studio 的三档天花板，也是本仓库运维手册（§11.6）
> 教人写进 `.env-go` 的三个值——也正是一个环境文件最可能残留的值。任何**其它**数字都是合法预算；
> 把这三个数字留给"AI Studio 档位"这一个含义，是为了让遗留值不可能被误读成预算。

### 13.3 启动自检：具体条件

`usage.ValidateSpendConfig()`（`internal/usage/spend.go`）与 `gemini.ValidateProviderConfig()`
同一形态：**studio 下是 no-op**（只读环境变量、不联网），所以接线不会改变今天的生产行为。

**返回 error（`main` 应记录并 `exit 1`）仅当**：

1. `GEMINI_PROVIDER=vertex` 且 `GEMINI_SPEND_LIMIT_USD` ∈ {10, 50, 200}（含 `10.00`、`1e1` 这类
   写法）——该值在 vertex 下没有对应物，继续武装闸门只会在无意义的阈值上拒绝客户；
2. `GEMINI_PROVIDER=vertex` 且该变量**设了但不是数字**——没有任何预算生效，而运维以为有。

**返回 nil**：studio 的任何取值（包括 `nonsense` 与 `-5`）；vertex 下变量未设 / 为 0 / 为负数 /
为其它正数。

> 选**失败**而不是警告：这两种取值都不是有效配置，且只影响 opt-in 的 vertex 部署，studio（默认）
> 永远不触发——与 `providerFromEnv` 拒绝半吊子 vertex 配置的态度一致。错误信息里写明了补救动作
> （unset，或改成不是 10/50/200 的数字）。

### 13.4 接线状态：**未完成的一步**

`ValidateSpendConfig()` **尚未被 `cmd/server/main.go` 调用**——本轮改动的文件范围被限定在
`internal/usage/**` 与本文档，而 `main.go` 不在其中。需要补的接线（放在
`gemini.ValidateProviderConfig()` 那段旁边）：

```go
if err := usage.ValidateSpendConfig(); err != nil {
    logger.Error("invalid Gemini spend configuration", "error", err.Error())
    os.Exit(1)
}
slog.Info("gemini spend guardrail",
    "limit_usd", usage.SpendLimitUSD(), "basis", string(usage.SpendLimitBasis()))
```

**在接线之前也不会静默**：`Budget()` 遇到被拒绝的配置时会打一条 ERROR（同一配置 5 分钟一次）
并**放弃武装闸门**，所以误配既不会持续拒绝客户，也不会无声无息。

### 13.5 可观测：谁触发的，一眼可辨

护栏触发时由 `internal/usage` 自己打一条 WARN（同一窗口内**每分钟最多一条**，防止风暴刷屏）：

```
level=WARN msg="Gemini spend gate shed a turn" basis=studio-tier-ceiling
  meaning="Google's upstream wall: AI Studio's spend-based rate limit for the account's tier is
  real, so shedding here is what keeps the 429 off the customer's screen"
  spent_usd=8.63 limit_usd=10 gate_ratio=0.85
```

```
level=WARN msg="Gemini spend gate shed a turn" basis=vertex-self-budget
  meaning="self-imposed budget only: Vertex enforces no spend-based rate limit, so no upstream
  pressure forced this shed — raise GEMINI_SPEND_LIMIT_USD or unset it to stop shedding"
  spent_usd=21.3 limit_usd=25 gate_ratio=0.85
```

`basis` 取值：`studio-tier-ceiling`（上游的墙）、`vertex-self-budget`（自设预算）、
`vertex-no-budget`（未设上限）、`vertex-legacy-studio-value-refused` /
`vertex-invalid-value-refused`（被拒绝，闸门关闭）。同一个值可由 `usage.SpendLimitBasis()` 取到，
供调用方自行措辞。

> **后续跟进（不在本轮范围）**：`internal/platform/quota_alert.go` 的 `AlertSpendGate` 文案仍写着
> "避免撞上 429 / 升档或降载后自动恢复"，在 vertex 下不准确——它应当按 `usage.SpendLimitBasis()`
> 分成"上游的墙"与"自设预算"两种说法。

### 13.6 迁移动作（studio → vertex）

1. 切 `GEMINI_PROVIDER=vertex` 时，**要么删掉 `GEMINI_SPEND_LIMIT_USD`**（推荐：vertex 下默认不设
   上限），**要么**把它改成不是 10/50/200 的数字，作为明确的自设预算。
2. 忘了第 1 步也不会伤客户：启动自检会失败（一旦 13.4 接线完成）并打印补救方法；**即使没接线**，
   闸门也不会武装，且每 5 分钟打一条 ERROR。
3. 复核：

```bash
# 生效中的旋钮与 provider
grep -E "GEMINI_PROVIDER|GEMINI_SPEND_LIMIT_USD|GEMINI_SPEND_GATE_RATIO" /opt/khmer-ai-cs/.env-go

# 闸门是否武装：vertex 下默认应当是 limit_usd=0、over_gate=false
curl -s -H "Authorization: Bearer $TOKEN" https://<host>/api/v1/platform/spend

# 被拒绝的配置长这样（"NOT armed"）；闸门触发的行则带 basis=
journalctl -u khmer-ai-cs -g "spend (configuration refused|gate shed)" --since "-1h"
```

4. 反向（vertex → studio）同样安全：把变量留空即解析回 10（Tier 1 默认），正是 studio 一直以来的
   行为；直接把 10 写回去也一样合法——**studio 下 10 的含义从未改变**。

5. `.env.example` 的建议改法（该文件由另一个任务维护，本轮未改）：

```bash
# studio：Google 的消费速率上限（Tier 1 $10 / Tier 2 $50 / Tier 3 $200 每 10 分钟）。
# vertex：这是我们自设的预算；留空/0 = 不设上限。10/50/200 在 vertex 下会被拒绝（见 §十三）。
# GEMINI_SPEND_LIMIT_USD=10
# GEMINI_SPEND_GATE_RATIO=0.85
```

### 13.7 变量去留

| 变量 | studio | vertex | 结论 |
|---|---|---|---|
| `GEMINI_SPEND_LIMIT_USD` | Google 的墙的本地镜像 | 自设预算；未设 = 不设上限；10/50/200 被拒绝 | **不废弃**，语义按 provider 区分（见下） |
| `GEMINI_SPEND_GATE_RATIO` | 提前量（默认 0.85） | 同义 | 保留，**无需任何迁移动作** |

不建议在 vertex 下"废弃" `GEMINI_SPEND_LIMIT_USD`（即：不引入第二个变量名、也不把这个变量删掉）：

- **删掉它**会让 studio 失去唯一在 429 之前让路的手段，而 studio 的墙是真的；
- **换成另一个名字**（例如 `GEMINI_BUDGET_USD`）确实能靠变量名消除歧义，但代价是又多一个旋钮、
  又要两处文档同步，而遗留值误读的风险已经被 13.2 的"拒绝 + 启动自检"彻底堵住——收益不抵成本。
  若将来真的需要在 vertex 下把预算设成 **10/50/200 本身**（这三个数字当前被保留给"AI Studio 档位"
  这一个含义），再引入独立变量名是正确做法，届时应连同本表一起改。

**为什么是"按 provider 区分"而不是"统一为自设预算"**：统一只能往两个方向走，都不成立——
向上统一（两者都叫"自设预算"）会让 studio 失去那堵真实的墙，等于为了措辞一致而改变生产行为；
向下统一（vertex 也沿用 $10）正是本文档要修的那个 bug。而两种 provider 的**机制**（窗口、求和口径、
比例、失败开放）本来就完全相同，唯一分歧点只是"上限从哪来"——把它写成一次分支、并把分歧讲清楚，
比假装它们一样更诚实，也更少代码。

---

## 十四、切流手册（studio → vertex 两步）与发布门禁（增量 5，2026-09-25）

> 代码已就位（`internal/gemini` 的 provider 层，**默认仍是 studio**），§十二 记的是"能不能迁"的实测结论，
> 这一节只讲**怎么切、怎么验、怎么退**。命令级完整版（含预期输出、退出码、收尾清理）在
> `deploy-khmer-ai-cs/references/deploy-commands.md` **§10**；写给凌晨三点的那份就在那里。

### 14.1 为什么能分两步

| 步 | 动作 | 为什么可以这样分 |
|---|---|---|
| **1** | DB `model_configs` 里 `is_default = true` 那行 → `gemini-3.5-flash`，**仍在 studio 上跑一段** | `gemini-3.5-flash` 在 **AI Studio 侧也返回 200** ⇒ 先换名字**不存在"新名字没人认"的窗口期**；这一步出问题一眼归因到换模型，回滚只是把名字改回去，与传输层无关 |
| **2** | `.env-go` 加 `GEMINI_PROVIDER=vertex` + `GEMINI_VERTEX_PROJECT` / `_REGION=asia-southeast1` / `_SA_FILE`，重启 | 传输层（端点 / 鉴权 / 报文形状）一次换掉；回滚 = 改回**这一条变量** + 重启 |

**主模型在 DB，不在 `.env-go`**：只要 `is_default` 行存在，`GEMINI_MODEL` 就被完全忽略
（启动日志 `Gemini configured from database model config model=…`）。管理后台保存会热加载（不必重启），
`psql` 直接 UPDATE **必须重启**——否则就是"以为切了，其实没切"。

### 14.2 切流前门禁：`vertexprobe`（必需项失败 = `exit 1`，不许进第 2 步）

```bash
cd /root/khmer-deploy
./vertexprobe -sa /opt/khmer-ai-cs/vertex-sa.json \
              -project <GCP project id> -region asia-southeast1 \
              -models gemini-3.5-flash,gemini-2.5-flash
echo "EXIT=$?"     # 门禁只看这个数字
```

期望：`✓ embedding :predict: gemini-embedding-001  dim=768 (want 768)  [required]` + `EXIT=0`。
两个必须知道的坑：

- 必须带 `-models`：内置候选表把 `gemini-flash-lite-latest` 也标成 `[required]`，而平台**没有 lite 档也没有这个别名**（全 404）——
  裸跑会在平台完全健康的情况下 `exit 1`。
- **`-tasktype` 必然 `exit 1`**：那正是 §十二 第 1 条要报的结论（无 task 条件化），它是诊断工具，**永远不要放进发布门禁**。

`EXIT=2` = 根本没到 API（缺 `-sa`、SA JSON 读不了或缺字段、key 里没 `project_id`）。

### 14.3 ⚠️ 时序陷阱：`GEMINI_API_BASE` 必须**最后**再清

- 这台机器到 AI Studio 的**唯一通路**就是 `GEMINI_API_BASE` 指向的 Cloudflare AI Gateway 中继（Google 按出口 IP 封锁直连）。
- **vertex 路径根本不读这个变量** ⇒ 清早了**当天完全没有症状**，废掉的是**以后才需要的东西**：回滚通路
  （把 `GEMINI_PROVIDER` 改回 studio）与 studio 侧对照工具（`embedcmp` 路径 A 需要它才能跑——实测正是在这里踩出来的）。
- 清之前先确认"不再需要回滚"。正向证据：CF 面板 AI → AI Gateway 里那台网关的请求数**归零**
  （**日志里没有任何一行会打印当前 provider**，这是最直接的"确实不再走 studio"证据）。

### 14.4 切流后验证清单

| # | 检查 | 命令 / 方式 | 通过判据 |
|---|---|---|---|
| 1 | 六项能力（目标模型） | `./vertexprobe -sa … -region asia-southeast1 -caps gemini-3.5-flash` | `6/6 request shapes accepted.` —— 文本+systemInstruction、内联图片、内联音频、内联 PDF、`thinkingConfig`、SSE 全部 OK |
| 2 | 音频容器 (ogg / m4a) | `… -caps gemini-3.5-flash -audiodir /root/khmer-deploy/audio-samples` | ≥1 行 `audio/ogg` + ≥1 行 `audio/mp4`，`N/N accepted`（**`0/0` = 空目录假通过**）。高棉语语音是真实客户路径，必须过 |
| 3 | 服务端到端 | 后台「模型」页的测试按钮；再发一条真实消息 | 返回 `reply` + `model_name=gemini-3.5-flash`；journal 无 `gemini returned HTTP …` |
| 4 | 检索基线自检 | `rageval -eval /root/khmer-deploy/rag_eval.json -user 7 -limit 5 -pipeline` | `LEG dense recall@5 ≥ 37/45` 且 `MRR ≥ 0.655`（实测路径 B：`38/45`、`0.705`）。**对不上 = 语料或配置有问题**，先分诊再怀疑闸门 |
| 5 | 相似度闸门复核 | `grep -E '^RAG_(SIMILARITY_FLOOR\|SIMILARITY_RATIO\|RERANK_SKIP)=' /opt/khmer-ai-cs/.env-go` + `rageval -configs` 扫描 | 见下——**这是本次迁移唯一需要动配置的地方** |
| 6 | 429 是否消失 | `journalctl -u khmer-ai-cs-go --since "24 hours ago" --no-pager \| grep -c "spend-based rate limit"` | `0`（**迁移的主要动机**）。若出现其它 429：那是平台按区域 QPS 的配额，与消费速率无关，去 GCP 提额，不是回滚信号 |
| 7 | 计量仍在记 | `curl -s …/api/v1/platform/spend` + `token_usage` 近一小时行数 | 有数（护栏没被 provider 改动破坏） |

**第 5 项为什么必须做**：实际切点是 `max(RAG_SIMILARITY_FLOOR, RAG_SIMILARITY_RATIO × 该查询最高相似度)`。
`ratio` 随尺度自适应，**`floor` 是绝对值**——而实测相似度尺度整体下移（mean top1 `0.730 → 0.713`），
于是 `floor` 会更频繁地成为真正的切点，dense 腿可能被掏空，**而排序一点没变**（§十二 的 recall/MRR 是在
`floor` 之上的排序指标，它证明不了闸门安全）。另有 `RAG_RERANK_SKIP` 与 `service.go` 里写死的 `0.60`
两处绝对比较判据会轻微移动，复核时把 `rerank(skip/run)` 计数一起看。

动作：先用 `rageval -configs "<floor>:<ratio>:<skip>,0.30:0.70:<skip>,0.33:0.75:<skip>"` 扫描，
**选能保住 dense 腿 recall 的最高 floor**（最保守的一档）→ 写进 `.env-go` → 重启 → 重跑第 4 项。
**没有 sweep 证据就不要调**。

### 14.5 回滚（一条环境变量 + 重启）

```bash
cd /opt/khmer-ai-cs
grep -q '^GEMINI_API_BASE=' .env-go || echo "⚠️ 中继地址不在 .env-go 里 —— studio 回滚会失败，先补回来"
sed -i 's/^GEMINI_PROVIDER=.*/GEMINI_PROVIDER=studio/' .env-go   # 删掉该行也行：不设 = studio
systemctl restart khmer-ai-cs-go && sleep 2 && systemctl is-active khmer-ai-cs-go
curl -s http://127.0.0.1:8081/ready
```

回滚**不动** DB 的模型名——第 1 步可以保留（`gemini-3.5-flash` 在 studio 上也 200）。
⚠️ 但第 2 步一旦写错（`GEMINI_PROVIDER=vertex` 而 project/SA 缺失或服务用户读不到 key），
进程会**启动即退出** ⇒ 第一动作是回滚把服务拉起来，再修 `.env-go`（客户侧不能长时间没有服务）。

### 14.6 收尾清理（迁完之后）

| 资产 | 处置 | 注意 |
|---|---|---|
| SA 密钥 `/opt/khmer-ai-cs/vertex-sa.json` | **保留**（它是生产凭据），只收紧权限 `chown khmerai:khmerai` + `chmod 600` | 绝不进仓库；轮换 = 先落新 key、重启验证、再删旧 key |
| AI Studio key | 留作回滚余量，或到控制台删除 | ⚠️ 删 key **且**清了 `GEMINI_API_BASE` 之后 studio 这条腿彻底死了，而 `GEMINI_PROVIDER` 一旦在后续编辑中丢掉，**缺省值就是 studio** ⇒ 生产指向一个不通的端点。要么三样一起留到不再需要回滚，要么明确记档"studio 已退役"并盯住 `/ready` 与失败率 |
| `GEMINI_API_BASE` | **最后**清（§14.3） | 清掉后 `embedcmp` 路径 A 与 studio 对照都跑不了 |
| **决定不迁** | `rm /opt/khmer-ai-cs/vertex-sa.json` **+** 到 GCP 控制台删除该服务账号 | 只删文件 = 留一把仍在授权中的钥匙副本；只删账号 = 留一个没人再用的密钥文件。**两个一起做** |

### 14.7 与 §十三 的接口

在 `.env-go` 里加 `GEMINI_PROVIDER=vertex` 的同一次编辑里，顺手处理 `GEMINI_SPEND_LIMIT_USD`：
vertex 下它不再是 Google 的墙的镜像，**要么删掉**（= 不设上限），**要么改成一个明确的预算数字**——
**不要留 studio 的档位 `10 / 50 / 200`**（那三个数字是 AI Studio 的天花板，在平台上没有对应物，
留着只会让闸门在一堵不存在的墙前面继续甩客户回合）。判定细节与观测方式见 §十三。
