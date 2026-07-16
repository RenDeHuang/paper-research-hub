# Opportunities Page Information Architecture

> **项目：** Paper Research Hub
> **日期：** 2026-07-16
> **页面角色：** 研究机会发现与证据审查
> **全局规则：** 遵循 `design-system/paper-research-hub/MASTER.md`

本文件不定义页面 palette、状态颜色、矩阵配色或独立组件外观。

## 用户任务

1. 浏览“值得做”“谨慎做”“当前不建议做”和“证据不足”。
2. 按 Topic、窗口、数据可得性、可复现性和竞争密度筛选。
3. 从矩阵或列表进入一个机会，核查公式、证据论文和限制。
4. 区分真实负向结论与单纯证据不足。

## 信息架构

1. **页面标题与决策边界**
   - `h1`、当前分析快照、覆盖率、公式版本；
   - 明示该页面提供研究情报，不替代正式研究评审。
2. **状态与筛选**
   - 四种状态的 segmented control 或 tabs；
   - Topic、窗口、竞争密度、数据可得性、可复现性和证据量；
   - 当前筛选 chips。
3. **机会概览**
   - 桌面优先 Opportunity Matrix；
   - 移动端优先按状态分组的机会列表；
   - 矩阵与列表共享同一筛选和选择状态。
4. **机会列表**
   - Topic、状态、增长、竞争密度、数据可得性、可复现性；
   - 证据论文数、覆盖率、生成时间；
   - 默认按可解释综合顺序，允许单指标排序。
5. **证据详情**
   - 状态和一句原因；
   - Growth Window、Growth Score、Competition Density、
     Data Availability、Reproducibility；
   - Evidence Bars、公式版本、confidence、missing signals；
   - 证据论文、Limitations、Generated At。
6. **方法与限制**
   - 状态阈值、排除规则、数据覆盖边界；
   - “证据不足不得升级为推荐”的固定说明。

## 组合规则

- 四种状态全部保留；“证据不足”不得并入“当前不建议做”。
- 矩阵横轴为竞争密度、纵轴为增长信号；其他指标进入相邻证据面板。
- 推荐状态必须同时显示文字、形状和 semantic status，不只使用颜色或象限。
- 机会卡片先显示状态原因，再显示数值；不得用单一大分数掩盖缺失信号。
- 证据论文按相关性与时间排序，并可进入论文详情核查 provenance。
- confidence 与 data coverage 分开显示，避免把样本覆盖误当模型置信度。

## 必要响应式行为

- `375px`：列表为默认视图；证据详情进入全宽 drawer 或页面内展开区。
- `768px`：状态摘要两列；矩阵可用但必须保留列表切换。
- `1024px`：矩阵与列表使用 `7 + 5` 或上下布局，避免过窄双栏。
- `1440px`：矩阵和列表占主区域，`300px` contextual aside 显示选中机会证据。
