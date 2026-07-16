# Paper Research Hub Go + Nuxt 重构设计

**状态：** 已批准  
**日期：** 2026-07-16  
**决策：** 清退现有 FastAPI、Alembic、Python 测试和 Next.js 实现，以 Go 模块化单体后端、Nuxt/Vue 前端和 PostgreSQL 重建论文聚合门户。

## 1. 目标

建设一个面向公众的 AI Agent / LLM Agent 论文聚合门户，持续回答：

1. 最近发布了哪些论文？
2. 哪些论文、主题和方法正在升温？
3. 哪些方向值得继续研究，证据是什么？
4. 哪些方向已经拥挤、证据不足或当前不可行？
5. 一篇论文使用了什么方法、数据集、Benchmark、模型和代码仓库？

长期维护成本优先于复用已有实现。旧代码只由 Git 历史保留，不迁移旧运行时、旧数据库迁移链或旧测试结构。

## 2. 设计原则

- **模块化单体优先：** 一个 Go Module，共享领域模型和数据访问层，不拆分微服务。
- **前后端严格解耦：** Nuxt 只通过版本化 HTTP API 读取数据，不直接访问数据库。
- **单一事实源：** PostgreSQL 保存规范化数据、来源证据和榜单快照。
- **确定性优先：** DOI、arXiv ID、OpenAlex ID 等确定性标识用于论文归一；标题相似度不能自动合并论文。
- **证据可追溯：** 推荐、趋势、分类和“值得做”判断必须能追溯到论文、时间窗口、计算公式或模型版本。
- **不引入 Python Worker：** 第一阶段不维护第二套后端运行时。
- **不做兜底路径：** 数据源错误、解析错误和契约错误显式失败并记录，不使用静默降级或猜测性修复。
- **删除而非兼容旧实现：** 不保留 FastAPI/Next.js 兼容层，不双写旧表，不维护双版本 API。

## 3. 总体架构

```text
Browser / Search Engine
          |
          v
Nuxt 4 / Vue 3 Web
          |
          | HTTPS + OpenAPI
          v
Go API ----------------------+
          |                  |
          v                  v
PostgreSQL              Object Storage
          ^
          |
Go Worker / Scheduler
          |
          v
OpenAlex / arXiv / Crossref / OpenReview / GitHub
```

`api` 与 `worker` 是同一 Go Module 构建出的两个二进制：

- `paper-hub-api`：公开查询 API、健康检查和管理端任务触发接口。
- `paper-hub-worker`：抓取、规范化、实体抽取、指标快照和榜单生成。

它们共享领域层、仓储层、配置、日志、遥测和数据库迁移，不通过消息队列互相调用。第一阶段使用 PostgreSQL 任务表协调异步任务，避免额外维护 Redis、RabbitMQ 或 Kafka。

## 4. 仓库结构

```text
paper-research-hub/
├── apps/
│   └── web/
│       ├── app/
│       ├── public/
│       ├── tests/
│       ├── nuxt.config.ts
│       └── package.json
├── services/
│   └── core/
│       ├── cmd/
│       │   ├── api/
│       │   ├── worker/
│       │   └── migrate/
│       ├── internal/
│       │   ├── config/
│       │   ├── database/
│       │   ├── httpapi/
│       │   ├── ingestion/
│       │   ├── paper/
│       │   ├── ranking/
│       │   ├── researchmap/
│       │   ├── source/
│       │   └── telemetry/
│       ├── migrations/
│       ├── go.mod
│       └── go.sum
├── contracts/
│   └── openapi.yaml
├── deploy/
│   ├── compose/
│   ├── docker/
│   └── production/
├── design-system/
├── docs/
├── .github/workflows/
├── Makefile
├── package.json
└── pnpm-workspace.yaml
```

## 5. 后端组件

### 5.1 技术基线

- Go 1.26
- PostgreSQL 18
- `net/http` 兼容 HTTP Router
- `pgx` 数据库驱动
- SQL-first 查询与代码生成
- SQL 文件迁移
- OpenAPI 3.1
- 结构化 JSON 日志
- OpenTelemetry 指标与追踪接口

只引入能够显著降低维护成本的依赖。领域规则、榜单公式和归一算法不依赖大型框架。

### 5.2 领域边界

`paper`：

- 论文、版本、作者、机构和外部标识
- DOI/arXiv/OpenAlex/OpenReview 标识规范化
- 论文状态和版本关系

`source`：

- 数据源配置
- 原始响应元数据、内容哈希、抓取时间和许可
- 字段级来源证据

`ingestion`：

- 增量游标
- 幂等抓取任务
- 原始记录写入
- 规范化与投影
- 失败重放

`ranking`：

- 最新论文
- 引用加速
- 代码增长
- 主题增长
- 方法采用率
- 榜单快照和公式版本

`researchmap`：

- Topic、Method、Dataset、Benchmark、Model 和 Code Repository
- “值得做”“谨慎做”“暂不建议做”的结构化证据
- 研究空白、竞争密度、数据可得性和可复现性指标

## 6. 数据模型

第一版核心表：

```text
works
paper_versions
source_records
external_identifiers
field_assertions
authors
work_authors
institutions
topics
work_topics
methods
work_methods
datasets
work_datasets
benchmarks
work_benchmarks
models
work_models
code_repositories
work_code_repositories
metric_snapshots
ranking_snapshots
ingestion_cursors
ingestion_jobs
analysis_runs
```

关键约束：

- 外部标识符以规范化后的 `(scheme, value)` 唯一。
- 来源记录以 `(source, source_record_id, content_hash)` 唯一。
- 抓取任务必须携带幂等键。
- 榜单快照必须包含公式版本、窗口、生成时间和数据覆盖率。
- 撤稿、撤回、拒稿和被替代论文保留记录，但不能进入推荐榜。

本次重构不迁移旧 Alembic 数据库。Go 迁移从 `000001` 开始建立全新 schema。

## 7. API 契约

统一前缀：

```text
/api/v1
```

第一阶段公开接口：

```text
GET /health
GET /api/v1/stats
GET /api/v1/papers
GET /api/v1/papers/{id}
GET /api/v1/topics
GET /api/v1/topics/{slug}
GET /api/v1/methods
GET /api/v1/methods/{slug}
GET /api/v1/trends/papers
GET /api/v1/trends/topics
GET /api/v1/trends/methods
GET /api/v1/research-opportunities
```

契约规则：

- OpenAPI 文件是前后端契约事实源。
- Nuxt 客户端类型由 OpenAPI 生成，不手写重复 DTO。
- 列表接口使用稳定游标分页。
- 筛选参数使用明确枚举，不接受任意 SQL 风格表达式。
- 错误统一使用 RFC 9457 Problem Details。
- 写操作只用于内部管理接口，并与公开接口隔离。

## 8. 数据抓取与分析

第一阶段数据源：

1. OpenAlex：论文主体、作者、机构、主题和引用计数。
2. arXiv：版本和预印本信息。
3. Crossref：DOI、出版状态和出版元数据。
4. OpenReview：论坛、评审与投稿状态。
5. GitHub：明确关联代码仓库的活动快照。

每个连接器都实现相同生命周期：

```text
fetch → validate → persist raw metadata → normalize → project → snapshot
```

每一步都保存状态；后一步失败不能回滚已确认的原始来源记录。任务重试从失败阶段继续，但只能依赖持久化状态，不依赖进程内缓存。

AI 分析通过 Go 中的提供商接口调用模型 API：

- 输入使用固定版本提示词和结构化论文证据。
- 输出必须通过 JSON Schema 校验。
- 结果保存模型、提示词版本、输入哈希和生成时间。
- AI 结果不直接修改规范论文事实。
- 推荐结论由确定性指标和经过验证的分析结果共同生成。

## 9. Nuxt 门户

页面：

- 首页：搜索、最新论文、热门论文、主题趋势、方法趋势和研究机会。
- 论文目录：多维筛选、排序和游标分页。
- 论文详情：摘要、作者、来源、方法、数据、Benchmark、代码和趋势。
- 趋势中心：7/30/90 天论文、主题和方法趋势。
- 研究机会：值得做、谨慎做、当前不建议做，并展示判断依据。
- Topic/Method 专题页：时间序列、代表论文、活跃机构和研究缺口。

渲染策略：

- 首页、专题页和论文详情使用 SSR。
- 稳定专题入口允许预渲染。
- 搜索和筛选保留 URL 状态。
- SEO 元数据、Canonical URL、Open Graph 和结构化数据由服务端生成。
- API Base URL 只通过 Nuxt Runtime Config 注入。

## 10. UI/UX 与视觉资产

正式实现前必须：

1. 使用 `ui-ux-pro-max --design-system` 生成新的设计系统。
2. 删除旧的 `design-system/paper-research-hub`，不继承旧页面结构。
3. 使用 `imagegen` 生成品牌视觉、空状态或专题封面所需资产。
4. 将最终使用的图像复制到 `apps/web/public/images` 并纳入 Git。

视觉目标：

- 参考 AgentSkillsHub 的目录密度和发现效率，但不复制品牌。
- 学术可信、数据密集、层级明确。
- 趋势图、矩阵图和证据条优先于装饰性大图。
- 同时支持 375、768、1024、1440 四类视口。
- WCAG AA 对比度、键盘操作、可见焦点和减少动态效果。

## 11. 错误处理与可观测性

- HTTP 错误返回稳定错误码、请求 ID 和 Problem Details。
- 数据源错误记录源、请求参数摘要、响应状态和重试资格。
- 数据校验失败进入失败任务，不写入猜测字段。
- 所有任务记录开始时间、结束时间、游标、读取数、写入数、排除数和失败数。
- API 和 Worker 使用同一日志字段与追踪上下文。
- 健康检查区分进程存活和依赖就绪。

## 12. 部署

本地：

```text
Docker Compose
├── web
├── api
├── worker
└── postgres
```

生产：

- Web、API、Worker 独立容器部署。
- API 和 Worker 使用同一个 Go 镜像，不同启动命令。
- PostgreSQL 使用托管数据库。
- 前端可运行在 Node 容器或支持 Nuxt 的托管平台。
- 数据库迁移由独立一次性任务执行，应用启动时不自动迁移。
- 生产环境使用滚动发布；迁移必须遵循 expand/contract 兼容规则。

## 13. 测试

Go：

- 单元测试：标识规范化、状态转换、榜单公式。
- 仓储测试：真实 PostgreSQL。
- 连接器契约测试：固定官方响应样本。
- API 测试：`httptest` 加真实数据库。
- 并发与竞态：`go test -race ./...`。

Nuxt：

- Vitest：组件、组合式函数和 API 客户端。
- Nuxt Test Utils：SSR 页面与路由。
- Playwright：搜索、筛选、详情、趋势、移动端。
- 类型检查、ESLint 和生产构建必须通过。

契约：

- CI 验证 OpenAPI。
- 生成客户端后必须保持工作区无差异。
- 后端响应通过契约测试。

## 14. 旧实现清退

清退范围：

- 删除 `services/api` 下全部 Python、FastAPI、Alembic、pytest 和 `uv` 文件。
- 删除 `apps/web` 下全部 Next.js App Router、Next 配置和 Next 测试。
- 删除旧 `artifacts` 抓取结果。
- 删除旧设计系统并重新生成。
- 删除过时的 FastAPI/Next.js 设计和实施文档。
- 替换根目录脚本、环境变量示例和锁文件。

清退通过 Git 变更完成，不执行历史重写；任何旧实现仍可从此前提交中审计或恢复。

## 15. 验收标准

- 仓库不存在 Python、FastAPI、Alembic 或 Next.js 运行依赖。
- `go test -race ./...` 通过。
- Nuxt 单元测试、类型检查、Lint 和生产构建通过。
- Docker Compose 能从空数据库启动并完成迁移。
- 能抓取一批实时 OpenAlex 论文并在首页、搜索、详情和趋势页面展示。
- API 与 Worker 可独立扩缩容。
- 所有公开 API 都记录在 OpenAPI 中。
- 前后端只通过 API 契约耦合。
- GitHub Actions 可从干净检出完成测试和镜像构建。
- GitHub 公开仓库创建并推送成功。
