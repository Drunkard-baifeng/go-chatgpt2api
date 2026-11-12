<a id="third-party-notices"></a>

# 第三方声明

控制台布局、主题色值、`src/App.vue` 的 SVG 背景，以及
`src/components/MetricCard.vue` 的指标装饰波形，改编自 CPA-Manager-Plus，
对应提交 `64ce94e6aa1af691555429a3211e67c59c0480ad`：
https://github.com/seakee/CPA-Manager-Plus

上游 `apps/web/src/` 中的参考文件：

- `styles/layout.scss`、`themes.scss`、`global.scss` 和 `components.scss`
- `components/layout/MainLayout.tsx`
- `components/common/AppBackground.tsx` 和 `AppBackground.scss`
- `components/config/VisualConfigEditor.module.scss` 和 `ConfigSection.module.scss`
- `features/config/ConfigPage.module.scss`
- `features/dashboard/DashboardPage.module.scss`
- `features/dashboard/components/UsageMetricsCard.tsx` 和 `UsageMetricsCard.module.scss`
- `features/dashboard/components/VersionCard.module.scss` 和 `TrafficOverviewCard.module.scss`
- `features/dashboard/components/TrafficOverviewCard.tsx` 和 `utils/dataPalette.ts`
- `features/accounts/AccountsPage.module.scss` 和 `AccountsPage.tsx`（分组行及快捷操作布局）
- `components/ui/Drawer.module.scss`（右侧几何布局、动效与固定头尾）
- `features/logs/LogsPage.module.scss`（筛选间距、搜索宽度、紧凑行及悬浮效果）
- `features/monitoring/styles/_monitoring-summary-table.scss`（紧凑监控排版和身份元数据）
- `features/accounts/AccountsPage.module.scss`（`configurationSection`、`detailEvidenceGrid`、`detailCandidateItem`：监控详情分区和慢请求指标布局）

概览图表色、虚线坐标轴、渐变填充、设置导航悬浮效果和开关卡片间距，均适配为 Vue 展示组件。
统计数据和设置行为继续使用本应用既有契约。

代理管理将配置分区图标块、监控表格元数据间距和账号详情分区适配到 Vue。
多出口分组流程及后端契约仍由本应用拥有。

<a id="cpa-manager-plus--mit-license"></a>

## CPA-Manager-Plus — MIT 许可证原文

Copyright (c) 2026 Seakee

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
