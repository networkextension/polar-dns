# polar-dns 开发计划

> 配套设计稿:[design.md](./design.md)(产品形态:统一 DNS Control Plane)。
> 本文把 design.md 的「通用独立版」对齐到 **Polar 平台模块规范**(参照系:`modules/polar-hosts`),
> 给出可执行的分阶段计划。状态:计划稿,待拍板第 9 节的开放决策。

---

## 0. 定位与平台对齐(先纠偏)

design.md 写的是 Fiber + React + 独立服务的通用版。作为 **Polar 模块**,有几处必须改:

| design.md 原案 | 平台规范(对齐后) | 原因 |
|---|---|---|
| Fiber / FastAPI | **Gin** | 全平台 + `polar-sdk` 都是 Gin;复用中间件 |
| `id UUID` | **TEXT 前缀 id**(`dp_`/`dz_`/`dr_`) | 与 hosts/agents 命名一致,日志可读 |
| 表无租户字段 | **每张用户可见表带 `workspace_id TEXT NOT NULL`** | 平台多租户硬约束 |
| `credential JSONB` 明文 | **AES-256-GCM 加密落库**(`DNS_CRED_KEY`) | DNS token 是高危密钥,等同 hosts 的 `HOSTS_SKILL_KEY` |
| 自管理 UI | **模块自带独立 `web/`(产品级,简约高效,自有设计)** | 已定:作为独立产品模块,不与现有模块设计统一;tab 模式下 dock 侧栏直接打开模块自有 URL |
| 自管理用户登录 | **不做登录**,会话经 `Dock.AuthVerifyWS` 校验 | 平台身份单点在 dock |
| 独立 DB,直连 | **自有库 `polar_dns`,只连自己**,跨域引用走 TEXT 指针 + SDK | database-ownership.md |

**不变的核心**(design.md 已对):Provider 抽象层、per-provider HTTP 代理、统一 API 屏蔽底层、审计日志、DNS-as-Code/批量为进阶。

---

## 1. 模块骨架

```
modules/polar-dns/
├── cmd/dns-svc/main.go            # 入口:env → Config → dns.New → RegisterRoutes → Start
├── internal/dns/
│   ├── plugin.go                  # Plugin{DB,Dock,Name,Listen,Ver}; New(); RegisterRoutes(); Start()(heartbeat)
│   ├── auth.go                    # requireAuthViaDock() / requireAdminViaDock()
│   ├── config.go                  # Config struct + env 读取
│   ├── crypto.go                  # AES-256-GCM 封装(DNS_CRED_KEY),复用 hosts 思路
│   ├── provider.go                # Provider 接口 + Zone/Record 领域类型 + 注册表
│   ├── provider_cloudflare.go     # CloudflareProvider
│   ├── provider_namecom.go        # NameProvider
│   ├── providers_handlers.go      # /api/dns/providers CRUD + sync
│   ├── providers_store.go
│   ├── zones_handlers.go          # /api/dns/zones, records 读
│   ├── zones_store.go
│   ├── records_handlers.go        # records 写(create/update/delete,写穿 provider)
│   ├── records_store.go
│   ├── audit_store.go             # dns_audit_log 写入 + 查询
│   ├── iac_handlers.go            # /api/dns/zones/:id/apply (DNS-as-Code:diff/apply)
│   └── internal_*.go              # /internal/v1/dns/* (dock→plugin,如 workspace 删除级联)
├── web/                           # 独立产品级前端(React/Vite,简约高效,自有设计系统)
│   ├── src/                       # provider / zone / record / audit / IaC 视图
│   └── dist/                      # 构建产物;dns-svc 以 go:embed 或静态目录托管
├── cmd/dnsctl/main.go             # CLI:dnsctl apply zone.yaml(IaC 客户端,M6)
├── scripts/migrate/dns-schema.sql # 幂等建表
├── go.mod                         # module github.com/networkextension/polar-dns; replace polar-sdk => ../polar-sdk
├── Makefile
└── README.md
```

默认端口 `127.0.0.1:8096`(hosts 占 8095)。

---

## 2. 数据库设计(对齐后 DDL)

库:`polar_dns`(operator 建库 + 跑 `dns-schema.sql`,幂等)。

```sql
-- 账户/凭据:credential 密文落库
CREATE TABLE IF NOT EXISTS dns_provider (
    id            TEXT PRIMARY KEY,              -- dp_<rand>
    workspace_id  TEXT NOT NULL,
    name          TEXT NOT NULL,                 -- 展示名,如 "cf-main"
    provider_type TEXT NOT NULL,                 -- cloudflare | namecom | (future) route53...
    cred_cipher   TEXT NOT NULL DEFAULT '',      -- AES-256-GCM(JSON credential)
    cred_plain    TEXT NOT NULL DEFAULT '',      -- 仅当 DNS_CRED_KEY 未配时降级明文 + encrypted=false
    encrypted     BOOLEAN NOT NULL DEFAULT FALSE,
    proxy_url     TEXT NOT NULL DEFAULT '',      -- 例 http://127.0.0.1:7890
    created_by    TEXT NOT NULL,                 -- user_id(经 dock 解析)
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, name)
);

-- 区(zone),远端为准,本地缓存映射
CREATE TABLE IF NOT EXISTS dns_zone (
    id             TEXT PRIMARY KEY,             -- dz_<rand>
    workspace_id   TEXT NOT NULL,
    provider_id    TEXT NOT NULL REFERENCES dns_provider(id) ON DELETE CASCADE,
    zone_name      TEXT NOT NULL,                -- example.com
    remote_zone_id TEXT NOT NULL,               -- CF: 0234af…; Name: example.com
    last_synced_at TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, provider_id, zone_name)
);

-- 记录:本地缓存(远端为 source of truth)
CREATE TABLE IF NOT EXISTS dns_record (
    id               TEXT PRIMARY KEY,           -- dr_<rand>
    workspace_id     TEXT NOT NULL,
    zone_id          TEXT NOT NULL REFERENCES dns_zone(id) ON DELETE CASCADE,
    remote_record_id TEXT NOT NULL DEFAULT '',
    type             TEXT NOT NULL,              -- A/AAAA/CNAME/TXT/MX/…
    name             TEXT NOT NULL,              -- 子域(或 @)
    content          TEXT NOT NULL,
    ttl              INTEGER NOT NULL DEFAULT 300,
    priority         INTEGER,                    -- MX/SRV
    proxied          BOOLEAN NOT NULL DEFAULT FALSE,  -- CF 专属;其他 provider 忽略
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (zone_id, remote_record_id)
);

-- 审计:谁、何时、改了什么、旧值/新值
CREATE TABLE IF NOT EXISTS dns_audit_log (
    id           BIGSERIAL PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    actor_user_id TEXT NOT NULL,
    action       TEXT NOT NULL,                  -- record.create|update|delete|provider.add|zone.sync
    target       TEXT NOT NULL DEFAULT '',       -- 受影响记录/区标识
    old_value    JSONB,
    new_value    JSONB,
    at           TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_dns_audit_ws_at ON dns_audit_log(workspace_id, at DESC);
CREATE INDEX IF NOT EXISTS idx_dns_record_zone ON dns_record(zone_id);
```

**跨库引用**:`created_by`/`actor_user_id`(=user_id)、`workspace_id` 都是 TEXT,不做外键,需要展示名时经 `Dock.UserGet`/`Dock.TeamGet`(SDK 缓存 30s)。

---

## 3. Provider 抽象

```go
// internal/dns/provider.go
type Record struct {
    RemoteID string
    Type     string // A/AAAA/CNAME/TXT/MX...
    Name     string
    Content  string
    TTL      int
    Priority *int
    Proxied  bool   // 仅 CF 有意义
}
type Zone struct {
    RemoteID string
    Name     string
}
type Provider interface {
    ListZones(ctx context.Context) ([]Zone, error)
    ListRecords(ctx context.Context, zoneRemoteID string) ([]Record, error)
    CreateRecord(ctx context.Context, zoneRemoteID string, r Record) (Record, error) // 回填 RemoteID
    UpdateRecord(ctx context.Context, zoneRemoteID string, r Record) (Record, error)
    DeleteRecord(ctx context.Context, zoneRemoteID, recordRemoteID string) error
}

// 工厂:provider_type + credential(解密后) + proxy → Provider
type Factory func(cred map[string]string, proxyURL string) (Provider, error)
var registry = map[string]Factory{} // "cloudflare"/"namecom" 注册
```

**代理**:每个 provider 的 `http.Client` 用 `&http.Transport{Proxy: http.ProxyURL(u)}`,`proxyURL` 空则直连。
**能力差异**(关键设计点):`proxied` 仅 CF;Name.com 无。抽象层用「能力位」声明,UI/校验按 provider 隐藏不支持字段,避免漏抽象。

---

## 4. API 设计(平台前缀 `/api/dns/*`)

dock 反代 `/api/dns/*` → `dns-svc:8096`。除 `/healthz`、`/metrics` 外全部走 `requireAuthViaDock()`(Bearer + workspace 访问门禁 `Dock.WorkspacePluginAccess(ws,"dns")`)。

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/healthz` | 无鉴权 |
| GET | `/metrics` | `DNS_METRICS_TOKEN` Bearer |
| POST | `/api/dns/providers` | 加 provider(凭据加密落库) |
| GET | `/api/dns/providers` | 列(脱敏,不回吐凭据) |
| DELETE | `/api/dns/providers/:id` | 删(级联 zone/record) |
| POST | `/api/dns/providers/:id/sync` | `ListZones`+`ListRecords` → 写缓存 |
| GET | `/api/dns/zones` | 列 zone |
| GET | `/api/dns/zones/:id/records` | 列记录(读缓存) |
| POST | `/api/dns/zones/:id/records` | 建记录(写穿 provider → 回填缓存 → 审计) |
| PATCH | `/api/dns/records/:id` | 改记录(写穿) |
| DELETE | `/api/dns/records/:id` | 删记录(写穿) |
| GET | `/api/dns/audit` | 审计查询(按 ws + 时间) |

**写穿语义**:写操作先打 provider API,成功后才更新本地缓存 + 写审计。provider 失败 → 返回错误、缓存不动(远端始终是 source of truth)。

---

## 5. 平台集成触点(MVP 必做)

1. **env**(`cmd/dns-svc/main.go` 读):
   ```
   POLAR_DNS_DB_DSN     postgres://…/polar_dns?sslmode=disable
   POLAR_DOCK_BASE      http://127.0.0.1:8080
   POLAR_PLUGIN_NAME    dns
   POLAR_PLUGIN_TOKEN   <dock /admin-plugins.html 明文>  # 缺失则 fatal
   POLAR_DNS_LISTEN     127.0.0.1:8096
   POLAR_DNS_VERSION    0.0.1
   DNS_CRED_KEY         <64 hex = 32 byte AES key>        # 缺失则降级明文 + 告警
   DNS_METRICS_TOKEN    <随机>
   ```
2. **SDK**:`sdk.NewClient(DockBase,"dns",sdk.DeriveHMACKey(token))`;启动 ping `/internal/v1/ping`。
3. **鉴权**:`Dock.AuthVerifyWS(bearer, X-Workspace-Id)`(缓存 30s)→ 注入 `workspace_id`/`user_id` 到 ctx。
4. **heartbeat**:每 60s `Dock.Heartbeat`,带 `UIRoutes: [{Path:"/dns/",Label:"DNS",Icon:"globe",Order:50,DisplayMode:"tab"}]`;tab 模式下 dock 侧栏点击直接打开模块自有 UI(`dns-svc` 托管的 `web/dist`)。
5. **注册**:operator 在 dock `plugin_modules` 建行(明文 token 一次性),记 `plugin_key_hash=sha256(token)`。
6. **workspace 删除级联**:`/internal/v1/dns/workspace-deleted` 收 dock 通知 → `DELETE … WHERE workspace_id=$1`。

---

## 6. 安全要点

- **凭据加密**:provider credential 走 `crypto.go` AES-256-GCM;`DNS_CRED_KEY` 未配时降级明文落库并打 `encrypted=false` 告警(与平台 graceful-degradation 一致,但生产必须配)。
- **凭据不回吐**:`GET providers` 永不返回 token,只回 `provider_type`/`name`/是否加密。
- **代理出网**:`proxy_url` 由用户填,需校验 scheme(仅 http/https/socks5),防 SSRF 到内网元数据。
- **审计完整性**:写操作即便 provider 成功、缓存更新失败,也要落审计(记录实际远端结果)。
- **/internal 二道墙**:生产 nginx 屏蔽 `/internal/`,HMAC 为第二层。

---

## 7. 进阶功能(已确认全量,M6 起按计划推进)

定位是产品级开放平台,以下为承诺目标,非可选取舍:

- **DNS-as-Code**(M6 核心):`zone.yaml` → diff(对比缓存/远端)→ apply(幂等 upsert)。
  先做 server 端 `POST /api/dns/zones/:id/apply`(收 yaml/json),再出 `dnsctl apply zone.yaml` CLI(`cmd/dnsctl`),对标 `kubectl apply`。
- **批量生成**:`*.foo.com` 模板 → 展开 `a/b/c.foo.com` 批量建记录(一次事务 + 多次写穿,部分失败要可回报)。
- **漂移检测**:定时 `sync` 对比缓存与远端,UI 标记 drift。
- **多 provider 同域**:同一 zone 多账户管理时的去重/优先级(排在 DNS-as-Code 之后)。

---

## 8. 里程碑(建议顺序,每个可独立出 PR)

| # | 里程碑 | 出口标准 |
|---|---|---|
| **M0** | 骨架 + 平台接线 | 编译通过;`/healthz` OK;连 DB;ping dock 成功;heartbeat 上报;`dns-schema.sql` 建表 |
| **M1** | Provider 抽象 + **Name.com**(首发) | `crypto.go` 加解密;`provider.go` 抽象 + **能力位**(`proxied` 等差异预留,即便 Name.com 不用);Name.com List/CRUD 跑通(真实账户手测);代理生效 |
| **M2** | sync + 读 API | `providers` CRUD、`sync`、`zones`/`records` 列表;缓存正确 |
| **M3** | 写 API + 审计 | 建/改/删记录写穿 Name.com + 缓存回填 + `dns_audit_log` |
| **M4** | **Cloudflare** provider | 第二 provider 验证抽象;CF 引入 `proxied` 能力位,差异处理正确 |
| **M5** | 独立 UI(`web/`) | 自有产品级前端(简约高效);provider/zone/record/audit 全视图;tab 模式经 dock 侧栏进入 |
| **M6** | DNS-as-Code(全量) | `POST /zones/:id/apply`(yaml/json diff + 幂等 apply)→ `dnsctl apply` CLI → 批量模板生成(`*.foo.com` 展开) |
| **M7** | 硬化 | 重试/限流、metrics、单测(provider 用 httptest mock)、`doc/deploy.md` |

**MVP = M0–M3 + M5**(单 provider **Name.com** 全链路 + 独立 UI)。M4 起验证可扩展性;
**M6 DNS-as-Code 为已确认的全量目标**,按里程碑逐步推进(非「可选取舍」)。

---

## 8b. M5 集成要点(dev 部署实测发现,2026-06-07)

一期(M0–M2)上 dev 后实测,sidebar 看不见 dns、且 `dns.dev.4950.store/` 误服务了 dock-ui 的
`dashboard.html`。根因厘清如下,作为 **M5 的硬性验收项**(现在不改,按计划到 M5 一起做):

- **dock-ui sidebar 是消费 nav API 的**:新版 `polar-dock-ui/src/lib/sidebar.ts` 拉 `/api/nav`
  (失败回退到硬编码静态 `NAV`)。**但当前 dev 上的 dock 没有 `/api/nav` 路由**,且部署的 dock-ui
  构建(Jun 2)既不调 `/api/nav` 也不调 `/api/plugin-ui-routes` —— 它的 sidebar 完全是 bundle 里
  写死的静态 NAV。所以 dns 不可能自动出现。
- dock **有** `/api/plugin-ui-routes`(已验证会返回 dns,root/已授权 workspace 可见;个人 workspace
  因 closed-by-default 返回 `[]`),但部署的 dock-ui 没用它。
- dock-ui sidebar **已原生支持外部子域链接**:`{href:"https://…", external:true}`(已有先例
  `https://hosts.4950.store:2443/hosts.html`)——这正是 dns 独立 UI 的接入方式。

**M5 需要做到(验收项):**
1. dns-svc 托管自有 `web/dist`,`dns.dev.4950.store/` 服务**自己的 UI**;nginx 把该子域 `location /`
   指向 dns-svc(:8101),**不再 `root` 到 dock-ui**(否则就是现在这个误服务 dashboard.html 的 bug)。
2. sidebar 出现 DNS 入口,点击打开 `https://dns.dev.4950.store/`(external/tab)。两条路二选一:
   - 平台正解:给 dock 加 `/api/nav` + `plugin_modules.public_base_url`,插件 heartbeat 自报外部 URL,
     dock-ui 动态渲染(改动跨 dock + dock-ui,属平台能力,非 dns 单模块范围)。
   - 临时:在 dock-ui 静态 NAV 里加一条 dns 外链 + 重新 build/部署。
3. workspace 访问门禁:非 root workspace 需 `workspace_plugin_access` 授权 dns 才在 sidebar 出现且 API 放行
   (`PUT /api/admin/workspace-plugin-access {workspace_id,plugin:dns,enabled:true}`)。
4. heartbeat 里补 `PublicBaseURL`(SDK `HeartbeatOpts` 有该字段,M0 未设)。

## 9. 决策记录

**已定(2026-06-07):**
1. **首发 provider**:**Name.com 先**(M1),Cloudflare 第二(M4)。抽象层在 M1 即预留能力位,M4 由 CF 引入 `proxied`。
2. **UI**:**模块自带独立 `web/`**,产品级、简约高效、自有设计,不与现有模块统一;tab 模式经 dock 侧栏进入(M5)。
3. **DNS-as-Code**:**全量纳入**,作为产品级开放能力,按 M6 计划逐步推进(非可选)。

**待拍板:**
4. **`DNS_CRED_KEY` 是否强制**:默认「未配降级明文 + 告警」(平台惯例);DNS token 高危,生产是否强制必配?
5. **代理粒度**:per-provider(已设计)够用,还是要全局默认代理 + per-provider 覆盖?
6. **租户模型**:provider 凭据 workspace 私有(默认),还是允许 platform「system」workspace 配公共 provider 给多 ws 共享?

---

## 附:参照实现

- 模块骨架/鉴权/heartbeat:`modules/polar-hosts`(最干净的参照)。
- 凭据 AES 加密:hosts 的 `HOSTS_SKILL_KEY` + `host_skill_credentials` 表。
- 平台规则:`doc/arch/open-platform.md`、`internal-api-v1.md`、`database-ownership.md`、`auth-and-tokens.md`。
- SDK:`modules/polar-sdk`(`NewClient`/`DeriveHMACKey`/`AuthVerifyWS`/`Heartbeat`/`UserGet`/`WorkspacePluginAccess`)。
