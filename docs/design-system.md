# DRAG（Design-RAG）游戏策划知识库视觉系统

## 视觉规格来源

- 主对话屏：`docs/design/concept-main.jpg`，原生规格 1600 × 1000。
- 资料与索引屏：`docs/design/concept-settings.jpg`，原生规格 1600 × 1000。
- 两张规格图由当前 renderer 的确定性预览生成，仅用于视觉与布局参考；真实功能验收以 Electron 运行结果为准。

## 信息架构

主界面采用三栏桌面工作台，而非营销页或通用 SaaS 仪表盘：

1. 270–304 px 会话侧栏：品牌、新建对话、搜索、最近 thread、归档入口、资料位置与索引。
2. 自适应对话主栏：thread 标题、账户/模型状态、用户问题、检索轨迹、回答、输入框。
3. 420–480 px 证据栏：查询、来源过滤、按新到旧排列的命中文档、选中引用详情。

设置态保留侧栏，主栏改为开放式来源列表、索引策略和阶段表格，右侧为来源详情。禁止把列表改成卡片网格。

## Color lock

- 页面背景：true white `#ffffff`。
- 次级背景：cool gray `#f7f8fa`，不得替换为米白或暖灰。
- 主文字：`#171a1f`；次文字：`#69707c`。
- 边框：`#dde1e7`；弱分隔：`#eceef2`。
- 主强调：vermilion `#f04420`；hover `#d93618`；浅选中 `#fff0eb`。
- 成功：`#23a55a`；警告：`#d97706`；错误：`#c9362b`。
- 阴影只用于浮层，常规 rail/list 使用 1 px 边框。

## Typography

- 字体：`Inter`, `PingFang SC`, `Microsoft YaHei`, `Noto Sans CJK SC`, sans-serif。
- 正文 14–15 px / 1.7；控件不低于 13 px / 1.4；caption 不低于 12 px。
- 页面标题 24 px/700；对话标题 17 px/650；小节标题 15–18 px/650。
- 所有按钮、输入、表格和侧栏显式定义字号，不依赖浏览器默认值。

## Geometry 与 motion

- 控件圆角 8 px；对话气泡 10 px；重要输入容器 10–12 px；不使用巨型圆角框。
- 8 px spacing 基线；栏间以 1 px 竖线分隔。
- 交互过渡 120–180 ms；检索阶段用状态点和文本变化，不用无限装饰动画。
- `prefers-reduced-motion` 下关闭位移与旋转动画。

## 允许的首屏文案

`DRAG`、`游戏策划知识库`、`新建对话`、`搜索对话`、当前 thread 标题、账户/模型状态、用户消息、`检索证据`、`策划案`、`配表`、`加入上下文`、`资料位置与索引`、输入框 placeholder 与必要的可访问性 label。

不得加入 hero eyebrow、营销口号、虚假指标、无关导航、紫色渐变、glassmorphism 或装饰性 badge。

## 组件族

- `Sidebar`：thread row 的 default/hover/selected 状态；更多菜单提供归档、恢复和带确认的删除。
- `TopBar`：标题、由 app-server `model/list` 动态提供的 model/reasoning、账户状态。
- `RetrievalTrail`：正在检索、已有部分证据、完成、错误；只显示真实事件，补检索期间不隐藏已返回来源。
- `Message`：user/assistant/error；assistant 支持 Markdown，但禁用 raw HTML。
- `EvidenceList` / `EvidenceDetail`：行式布局、选中态、引用定位；引用显示文件名与真实 locator，不显示内部 chunk ID。
- `Composer`：只保留真实 scope 状态、固定来源状态、发送、停止；不展示未实现的附件按钮。
- `SourceRow` / `StrategyRow` / `IndexStageTable`：设置页开放列表。
- `Dialog`：登录、目录选择失败、重建确认等真正需要阻断的操作。

## 响应式

- ≥ 1280 px：三栏完整显示。
- 900–1279 px：证据栏作为右侧 drawer，侧栏保留紧凑宽度。
- < 900 px：侧栏与证据栏均为 drawer；对话与 composer 不得横向溢出。
