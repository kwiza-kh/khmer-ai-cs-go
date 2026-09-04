# Cloudflare 域名接入与 521 排查 (cs 子域名)

## 架构（橙色云朵 = Cloudflare 代理）

```
浏览器 ──HTTPS──> Cloudflare 边缘 (托管证书) ──HTTPS:443──> 源站 nginx ──> 8081/3001
```

⚠️ **一个 zone 挂两个站点**: `wanfanginsulationmaterial.com` (WMS) 与 `cs.wanfanginsulationmaterial.com` (本项目) 共用同一 zone, **SSL 加密模式是 zone 级设置**, 改它会同时影响 WMS。

| 加密模式 | Cloudflare→源站 | 对本项目的后果 |
|---|---|---|
| **Full** (现行, 必须) | HTTPS :443 (源站自签) | 正常 |
| Flexible | HTTP :80 | **重定向死循环** — cs 的 :80 块有 `return 301 https://$host`, CF 每次都被 301 回 https, 浏览器永远转圈 |
| Full (strict) | HTTPS :443 校验 CA | 526 — 源站是自签证书, 不要用 |

结论:**永远保持 Full**。源站证书直接复用 WMS 的自签 `/etc/nginx/ssl/wms.crt` (SAN/CN 不匹配没关系, Full 不校验)。

## 诊断顺序（521 / 打不开时）

1. **服务器侧确认源站活着** (SSH):
   ```bash
   ss -tlnp | grep -E ":80|:443|:8081|:3001"     # 4 个都在
   curl -s -o /dev/null -w "%{http_code}" -H "Host: cs.wanfanginsulationmaterial.com" http://127.0.0.1/    # 301 = 正常(:80 设计就是跳转)
   curl -sk -o /dev/null -w "%{http_code}" https://127.0.0.1/ -H "Host: cs.wanfanginsulationmaterial.com" # 301/200 均可
   curl -s http://127.0.0.1:8081/ready            # 后端本体
   ```
2. **本地绕过 Cloudflare 直连 IP**:
   ```bash
   curl -sk -o /dev/null -w "%{http_code}" https://38.55.192.90/ -H "Host: cs.wanfanginsulationmaterial.com" --max-time 15
   ```
   直连不通 → 源站/防火墙问题; 直连通但域名挂 → CF 配置。
3. **CF 侧两查**: ① SSL 模式 = Full; ② `cs` A 记录 content = `38.55.192.90` (不能是 CF 边缘 IP, 会自环)。

## DNS 记录

- 记录: `cs` A → 38.55.192.90, Proxied (橙云)
- nslookup 看到的永远是 CF 边缘 IP (代理所致), **不能**用它判断 A 记录内容; 用面板或下面的 API 查。

## Cloudflare API 操作

```bash
CF_TOKEN="<API Token>"
# 验证 token (cfat_ 开头的是 Access Service Token, 不能用于 API!)
curl -s https://api.cloudflare.com/client/v4/user/tokens/verify -H "Authorization: Bearer $CF_TOKEN"

# zone (wanfanginsulationmaterial.com): id=9de0237704a80eead01abbfd1bd4488b, 账户 b86b1f31914f5b090031b778e2e064d5, 状态 active
ZONE_ID="9de0237704a80eead01abbfd1bd4488b"

# 读 DNS 记录 (找 name=cs 的那条拿 record_id)
curl -s "https://api.cloudflare.com/client/v4/zones/$ZONE_ID/dns_records?per_page=100" \
  -H "Authorization: Bearer $CF_TOKEN" | grep -o '"name":"[^"]*","content":"[^"]*"'

# 建/改 cs 记录
curl -s -X POST "https://api.cloudflare.com/client/v4/zones/$ZONE_ID/dns_records" \
  -H "Authorization: Bearer $CF_TOKEN" -H "Content-Type: application/json" \
  -d '{"type":"A","name":"cs","content":"38.55.192.90","proxied":true,"ttl":1}'
# 已存在则 PATCH .../dns_records/$RECORD_ID

# 读 SSL 模式 (只读确认; 改模式影响 WMS, 动手前问用户)
curl -s "https://api.cloudflare.com/client/v4/zones/$ZONE_ID/settings/ssl" \
  -H "Authorization: Bearer $CF_TOKEN"
```

Token 权限坑 (WMS 时期踩过, 仍有效):
- 创建: My Profile → API Tokens → "Edit zone DNS" 模板, Zone Resources = Include → Specific zone → `wanfanginsulationmaterial.com`
- 同账户还有 `wanfanginsulation.com` (initializing), 别拿错 zone
- 能列 zone ≠ 能读写该 zone DNS, 权限逐 zone 绑定; 报 `10000 Authentication error` / `9109 Unauthorized` = token 没覆盖这个 zone

## 交付前给用户的 Cloudflare 手动清单（首次接入）

1. DNS → Records: `cs` A 记录 → `38.55.192.90`, 橙色云朵 Proxied
2. SSL/TLS → Overview: 加密模式确认 **Full** (不要 Flexible / strict)
3. 生效后本地验证: `curl -s -o /dev/null -w "%{http_code}" https://cs.wanfanginsulationmaterial.com/` = 200
