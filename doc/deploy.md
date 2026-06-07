# polar-dns 部署

记录 dns-svc 的部署方式(以 dev `local@10.88.0.5`,headless macOS arm64 为例;其它环境同构)。
平台惯例与 `polar-hosts` 等模块一致。

## 组件与端口

| 组件 | 位置 | 说明 |
|---|---|---|
| 二进制 | `~/.local/bin/dns-svc` | `CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -o bin/dns-svc ./cmd/dns-svc` |
| 进程管理 | launchd `~/Library/LaunchAgents/com.polar.dns-svc.plist` | KeepAlive;`set -a; source <env>; exec dns-svc` |
| env | `~/.config/polar/dns-svc.env`(0600) | 见下 |
| 日志 | `~/polar/dns-svc.log` | |
| DB | `polar_dns`(owner=`polar`) | schema `scripts/migrate/dns-schema.sql` 以 polar 身份执行 |
| 监听 | `127.0.0.1:8101` | dev 上 8090–8100 已占,故 8101 |
| 对外 | `https://dns.dev.4950.store` | nginx 终止 TLS,`/` 与 `/api/dns` → dns-svc,其余 `/api` → dock |

## 环境变量

```
POLAR_PLUGIN_TOKEN        # 必填;dock /admin-plugins.html 一次性明文(缺失则 fatal)
POLAR_DNS_DB_DSN          # postgres://polar:<pw>@127.0.0.1:5432/polar_dns?sslmode=disable
POLAR_DOCK_BASE           # http://127.0.0.1:8080
POLAR_PLUGIN_NAME         # dns(须与 plugin_modules.name 一致)
POLAR_DNS_LISTEN          # 127.0.0.1:8101
POLAR_DNS_VERSION         # 0.x
POLAR_DNS_PUBLIC_BASE_URL # https://dns.dev.4950.store(heartbeat 上报 → dock /api/nav 拼侧栏外链)
DNS_CRED_KEY             # 64 hex(32 byte)AES-256-GCM;缺失则凭据明文存储 + 告警
POLAR_DNS_METRICS_TOKEN  # /metrics 的 Bearer(留空则 /metrics 返回 404)
```

## 首次部署

```bash
# 1. 库(owner=polar,表归 polar 所有)
psql -d postgres -c "CREATE DATABASE polar_dns OWNER polar;"
psql -d polar_dns -c "ALTER SCHEMA public OWNER TO polar; GRANT ALL ON SCHEMA public TO polar;"
PGPASSWORD=<pw> psql -U polar -h 127.0.0.1 -d polar_dns -f scripts/migrate/dns-schema.sql

# 2. 在 dock 注册插件(取得明文 token)
#    推荐:dock /admin-plugins.html 创建 name=dns,记下一次性明文 token。
#    或直接插库(plugin_key_hash = hex(sha256(token)),dock 用它当 HMAC key):
#    INSERT INTO plugin_modules (name,display_name,enabled,endpoint,version,plugin_key_hash,config)
#      VALUES ('dns','DNS Control Plane',true,'http://127.0.0.1:8101','0.x','<hash>','{}');

# 3. env 文件(0600) + launchd plist(镜像 com.polar.projects-svc.plist)→ launchctl load

# 4. nginx server block(dns.dev.4950.store):见下
```

## nginx(dns.dev.4950.store)

`/opt/homebrew/etc/nginx/servers/plugins-dev.conf` 追加一个 server 块;master 以 root 运行,
reload 用 `sudo nginx -s reload`。备份放 `~/polar/nginx-backups/`(**勿**放进 `servers/`,
因为 `include servers/*` 会把备份也加载进去)。

```nginx
server {
    listen 80; listen 443 ssl;
    server_name dns.dev.4950.store;
    ssl_certificate /Users/local/polar/tls/dev.crt; ssl_certificate_key /Users/local/polar/tls/dev.key;
    location ~ ^/api/dns(/|$) { proxy_pass http://127.0.0.1:8101; proxy_set_header Host $host; }  # dns API
    location /healthz { proxy_pass http://127.0.0.1:8101; }
    location /metrics { proxy_pass http://127.0.0.1:8101; }
    location /api/   { proxy_pass http://127.0.0.1:8080; proxy_set_header Host $host; }            # auth/session → dock
    location /       { proxy_pass http://127.0.0.1:8101; proxy_set_header Host $host; }            # 模块自有 UI
}
```

## 升级(redeploy)

```bash
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -o /tmp/dns-svc ./cmd/dns-svc
scp /tmp/dns-svc <host>:~/.local/bin/dns-svc.new
ssh <host> 'chmod +x ~/.local/bin/dns-svc.new; \
  launchctl unload ~/Library/LaunchAgents/com.polar.dns-svc.plist; \
  mv -f ~/.local/bin/dns-svc.new ~/.local/bin/dns-svc; \
  launchctl load ~/Library/LaunchAgents/com.polar.dns-svc.plist'
curl -sk https://dns.dev.4950.store/healthz   # {"plugin":"dns","db_ok":true,...}
```

## 侧栏入口(dock /api/nav)

dns-svc heartbeat 上报 `PublicBaseURL` + UIRoute `/`;dock `GET /api/nav` 据此拼出
`https://dns.dev.4950.store/` 外链(workspace 未授权则不显示,见下)。dock-ui 渲染该 nav。
改 dock/dock-ui 后需各自 rebuild + 重发(dock-ui 需 node:`npm install && npm run build` → 部署 `dist/` 到 `~/www/dock-ui`)。

## workspace 访问门禁

`/api/dns/*` 默认按 workspace 关闭(`workspace_plugin_access`);root/site-admin 工作区直通,
其它需授权:`PUT /api/admin/workspace-plugin-access {workspace_id, plugin:"dns", enabled:true}`(管理员),
或在控制台用管理员身份点「授权此工作区」。

## 观测

`/metrics`(Bearer `POLAR_DNS_METRICS_TOKEN`):`polar_dns_up`、
`polar_dns_provider_requests_total{provider,op,status}`、`polar_dns_provider_request_duration_seconds`。
上游 provider 的幂等 GET(sync 等)失败会自动重试(429/5xx/网络错,最多 2 次,线性退避);写操作不重试。

## 构建提示

本仓 go 的 GOROOT 可能被错设,统一用 `env -u GOROOT GOCACHE=/tmp/polar-go-cache go ...`。
