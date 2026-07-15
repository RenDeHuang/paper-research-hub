# 论文聚合门户产品设计

## 1. 产品定位

本项目建设一个公开的论文聚合与趋势门户，产品形态参考 AgentSkillsHub，但目录实体从 Skill/MCP 替换为：

- 论文
- 研究主题
- 研究方法
- 数据集
- Benchmark
- 模型
- 代码仓库

第一阶段只覆盖 `AI Agent / LLM Agent / Agentic AI`，在范围内追求完整收录，而不是立即承诺覆盖全部学科。

核心用户问题：

1. 最近发布了哪些论文？
2. 哪些论文正在快速变热？
3. 哪些研究主题正在增长？
4. 最近常用的研究方法是什么？
5. 哪些论文提供代码、数据或 Benchmark？
6. 一篇预印本、投稿版和正式出版版是否属于同一项研究？

## 2. 第一阶段范围

### 包含

- OpenAlex、arXiv、Crossref、OpenReview 的论文元数据接入框架
- 论文标准化、确定性去重与版本建模
- 论文类型、主题、方法、数据集、Benchmark 和代码实体
- 最新论文、热门论文、热门主题、热门方法
- 搜索、筛选、论文详情页
- 数据来源、抓取时间和许可证信息
- 响应式公开网页
- 可复现的本地开发与容器部署配置

### 暂不包含

- 全学科全量 OpenAlex 快照
- 未获再分发许可的 PDF 镜像
- 用户付费系统
- 复杂社交社区
- 个性化推荐模型
- 依赖标题模糊匹配的自动论文合并

## 3. 信息架构

### 首页

- 全站搜索
- 数据规模统计
- 今日热门论文
- 本周热度增长榜
- 最新论文
- 热门研究方向
- 热门研究方法
- 最新 Benchmark
- 有代码论文
- 分类入口

### 论文列表页

筛选维度：

- 时间
- 论文类型
- 研究主题
- 方法
- 是否有代码
- 是否有数据
- 是否有 Benchmark
- 发表状态
- 数据来源

### 论文详情页

- 标题、作者、机构、摘要
- 论文类型和研究主题
- 方法、数据集、Benchmark、指标
- 代码、模型和原始论文链接
- 预印本、投稿版、正式版版本关系
- 引用与平台趋势快照
- 数据来源、字段来源、许可证和更新时间

### 趋势页

- 论文热度榜
- 引用增长榜
- 代码增长榜
- 主题增长榜
- 方法采用率榜
- 7/30/90 天时间窗口

## 4. 数据架构

PostgreSQL 是唯一事实源。搜索索引、榜单 JSON、静态页面和缓存都是可以重建的派生数据。

核心实体：

```text
work
paper_version
source_record
external_identifier
field_assertion
author
institution
topic
method
dataset
benchmark
model
code_repository
metric_snapshot
ranking_snapshot
```

数据处理链：

```text
官方 API / OAI / Dump
→ 保存不可变原始记录
→ 标准化字段
→ 确定性版本关联
→ 分类与实体抽取
→ 指标快照
→ 榜单计算
→ API 与页面
```

自动合并只允许使用确定性证据：

- DOI 完全一致
- arXiv 基础 ID 一致
- OpenReview forum ID 一致
- 数据源明确提供同一外部标识符或版本关系

标题、作者和年份相似只能生成待审核候选，不能直接合并。

## 5. 榜单设计

热度、质量、相关度必须分开。

第一版提供以下独立榜单：

- 最新论文：按公开发表或版本更新时间排序
- 平台热度：平台收藏、有效阅读和外链点击
- 引用加速：在相同学科、论文类型和发表月份内比较引用增长
- 代码增长：仓库 Star、Fork、Release 和提交活动的时间快照
- 主题增长：主题论文数量相对历史基准的变化
- 方法采用率：使用该方法的新论文占同主题新论文的比例

如果提供综合榜，只能命名为“平台热度分”，必须公开公式、时间窗口、数据覆盖率和缺失信号。

## 6. 许可与数据治理

产品分为三层：

1. 元数据聚合层
2. 原始来源链接层
3. 许可全文层

默认不镜像 PDF。只有 `CC0`、`CC BY` 或其他明确允许再分发的许可证才可进入公开对象存储。

每个字段保存：

```text
source
source_record_id
source_url
retrieved_at
source_license
content_license
parser_version
```

论文状态必须支持：

```text
active
corrected
expression_of_concern
retracted
withdrawn
desk_rejected
superseded
```

撤稿或撤回论文保留目录记录，但从热门和推荐榜中移除，并突出状态。

## 7. 技术方案

### 前端

- Next.js App Router
- TypeScript
- Tailwind CSS
- Lucide 图标
- Server Components 优先
- 静态生成与动态搜索混合

### 后端与数据

- FastAPI
- PostgreSQL
- PostgreSQL Full Text Search
- `pg_trgm`
- `pgvector`
- SQLAlchemy 2
- Alembic
- Pydantic

### 数据任务

- Python 数据源连接器
- 幂等任务
- 原始响应内容哈希
- 游标和时间水位
- 失败任务可重放
- 定时同步与每日指标快照

### 部署

- 前端：Cloudflare Pages 或 Vercel
- API/Worker：Cloud Run 容器
- 数据库：托管 PostgreSQL
- 对象存储：R2/S3
- 调度：Cloud Scheduler
- 队列：生产环境使用 SQS 或 Cloud Tasks

第一版开发环境使用 Docker Compose 启动 PostgreSQL 和应用服务，但接口边界必须与生产部署保持一致。

## 8. 视觉设计

设计系统来源：

`design-system/paper-research-hub/MASTER.md`

设计方向：

- Marketplace / Directory 信息架构
- 搜索框作为首页主操作
- 学术可信、内容密集但层级清楚
- Institutional Navy 为主色
- 浅色和深色主题
- 375/768/1024/1440 响应式
- 所有交互目标至少 44×44px
- 明确键盘焦点和 WCAG AA 对比度
- 不使用 Emoji 作为结构图标
- 动画仅用于状态变化，支持 `prefers-reduced-motion`

## 9. 测试策略

- Python：pytest，覆盖连接器、标准化、确定性去重、榜单计算和 API
- 前端：Vitest + Testing Library，覆盖筛选、状态和组件行为
- 端到端：Playwright，覆盖首页、搜索、筛选、详情页和移动端
- 数据契约：固定样本和 JSON Schema
- 视觉验证：375、768、1024、1440 四个视口
- 可访问性：语义结构、键盘导航、焦点、对比度和 reduced motion

## 10. 成功标准

公开 MVP 必须满足：

1. 可以重复执行演示数据同步，不产生重复论文。
2. 同一 arXiv 论文的不同版本映射到同一 `work`。
3. 首页展示真实结构化数据，而不是硬编码卡片。
4. 搜索和筛选可以组合使用。
5. 榜单结果包含计算时间、窗口和来源说明。
6. 论文详情展示来源、许可证和版本。
7. 全部自动化测试、构建和端到端检查通过。
8. 提供本地启动、Docker 和公开部署文档。
