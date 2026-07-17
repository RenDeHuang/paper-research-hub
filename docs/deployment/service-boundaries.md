# Go API 与 Nuxt Web 部署边界

本文给出 `medpaperhub` 当前可执行部署的端口、进程和路由事实，用于判断
`localhost:8080` 的 404 到底是正常 API 行为、陈旧容器，还是部署错误。

## 结论

本地 Compose 没有反向代理，也没有让 Go API 托管 Nuxt 静态文件：

| 对外地址 | 进程 | 响应类型 | 用途 |
| --- | --- | --- | --- |
| `http://localhost:3000/` | `node .output/server/index.mjs` | `text/html` | Nuxt Web 首页 |
| `http://localhost:3000/health` | Nuxt/Nitro | JSON | Web 存活检查 |
| `http://localhost:8080/` | `/app/paper-hub-api` | JSON | Go API discovery；不是前端 |
| `http://localhost:8080/health` | Go API | JSON | API 存活检查 |
| `http://localhost:8080/api/v1/*` | Go API | JSON/problem+json | 公开 Catalog API |

因此，浏览器要看页面必须访问 `http://localhost:3000/`。访问
`http://localhost:8080/` 只能得到 API discovery JSON，不能得到、重定向到或伪装成
Nuxt 页面。

## 为什么曾经看到 `localhost:8080` 404

需要区分两个已经被运行证据确认的原因：

1. **历史 API 契约没有注册 `/`。**
   `8080` 一直是 Go API 端口；在 discovery route 加入前，只注册了 `/health` 和
   `/api/v1/*`，所以 `GET /` 由 Go `http.ServeMux` 返回 404。该 404 不是“前端丢失”，
   而是请求发到了 API 端口。
2. **容器镜像可能早于当前工作树。**
   当源码已经有 `/api/v1/home`、Nuxt `/subjects` 等路由，但运行容器仍使用旧镜像时，
   这些新路由仍会返回 404。只刷新浏览器不会重建镜像；必须重新运行
   `make compose-up`。

当前契约下的严格预期：

- `GET :8080/`：`200 application/json`，service 为 `medpaperhub-api`；
- `GET :8080/health`：`200 application/json`；
- 未知 API/非 Web 路径：404，且不能返回 HTML；
- Catalog 尚未显式发布时，`GET :8080/api/v1/home` 返回
  `503 catalog_not_published`，不是路由 404；
- `GET :3000/`、`GET :3000/subjects`、`GET :3000/journals`：Nuxt HTML。

## 本地启动与强验证

从仓库根目录执行：

```bash
make compose-up
```

该命令会：

1. 校验 Compose model；
2. 重建 Core 和 Web 镜像；
3. 等待 PostgreSQL、迁移、API 和 Web 达到各自声明的状态；
4. 自动执行 `make verify-local`。

也可以只复验已经运行的服务：

```bash
make verify-local
```

`verify-local` 不以“端口可连接”作为成功标准，而是严格验证：

- API/Web service identity；
- API root 是无重定向的 JSON discovery；
- Web root 是包含 `medpaperhub` shell 的 HTML；
- 未知 API 路径不会返回前端 HTML；
- Web origin 能获得 API 的精确 CORS 许可；
- Catalog 只能处于已发布 `200 + X-Catalog-Generation` 或未发布
  `503 catalog_not_published` 两种合法状态。

默认情况下脚本从运行中的 Compose 服务发现宿主机发布端口。验证非 Compose 地址时，
必须显式提供两个不同 origin：

```bash
API_URL='https://api.example.com' \
WEB_URL='https://medpaperhub.example.com' \
make verify-local
```

## 诊断命令

确认宿主机端口和容器生命周期：

```bash
docker compose ps --all
docker compose port api 8080
docker compose port web 3000
```

确认容器执行的是两个独立进程：

```bash
docker inspect paper-research-hub-api-1 \
  --format 'command={{json .Config.Cmd}} ports={{json .NetworkSettings.Ports}}'

docker inspect paper-research-hub-web-1 \
  --format 'command={{json .Config.Cmd}} ports={{json .NetworkSettings.Ports}}'
```

查看启动日志：

```bash
docker compose logs --no-color --tail=100 api web migrate
```

如果源码中存在路由、但运行结果仍为 404，先比较镜像和容器启动时间，再重新执行
`make compose-up`。不要通过在 API 根路径复制前端 HTML、增加临时重定向或把 Nuxt
塞进 Go 镜像来掩盖陈旧部署。

## 生产部署

生产环境仍保持同一边界：

- API workload 使用 Core 镜像，命令为 `/app/paper-hub-api`；
- Web workload 使用 Web 镜像，命令为 `node .output/server/index.mjs`；
- Migrate 和 Worker 是独立的一次性 Job；
- `INTERNAL_API_BASE_URL` 指向私网 API origin；
- `NUXT_PUBLIC_API_BASE_URL` 指向浏览器可解析的外部 API origin；
- `API_CORS_ALLOWED_ORIGINS` 精确列出外部 Web origin。

若生产入口需要同域名路由，应由独立 ingress/reverse proxy 明确把 Web 路由交给 Nuxt、
把 `/api/` 和 API health 路由交给 Go API；这不改变两个应用的镜像和生命周期，也不让
Go API 根路径假装成前端。
