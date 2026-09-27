# aw-watcher-herdr

> Herdr 终端工作区焦点与 AI Agent 状态的 ActivityWatch 遥测采集器。

---

## 为什么需要它？(The Problem & The Hook)

在日常使用现代终端管理器（Herdr）和 AI 编程 Agent（如 Codex、Claude）时，传统的窗口追踪器（如 `aw-watcher-window`）存在严重的观测盲区：

```text
[传统窗口采集器的视角]
14:00 ─── Terminal / iTerm (持续 4 小时) ──────────────────────────►
* 无法获知你切了哪个 Workspace，正在做哪个 Project，甚至不知道你是否在跑 AI。

[aw-watcher-herdr 的视角]
14:00 ─── [Project: api] main.go ──────────► (Focus: 人类在前台聚焦编写)
14:00 ─── [Project: api] codex: working ───► (Agent: 后台自主运行 35 分钟)
14:35 ─── [Project: api] codex: blocked ───► (Agent: 卡在等待用户输入 10 分钟)
```

**`aw-watcher-herdr` 深入 Herdr 内部通信协议，将黑盒的“终端前台时长”拆解为：**
1. **你的真实注意力**：你在哪个工作区（Workspace）、哪个标签页（Tab）、哪个分屏（Pane）以及哪个本地工程目录（CWD）。
2. **AI Agent 的实际产出**：后台启动的 AI 模型是处于 `working`（推理/执行）、`blocked`（等待确认/输入）还是 `done`（完成）。
3. **人机协作的真实关系**：当前 Agent 是在被你实时盯着（`supervised` 监督协同），还是完全被扔在后台自跑（`autonomous` 离线自主）。

所有数据自动对齐 [ActivityWatch](https://activitywatch.net) 原生分类体系（注入标准 `app` 与 `title`），无需配置复杂脚本，开箱即可分类和统计。

---

## 30 秒快速上手 (Quick Start)

只需 3 步即可完成编译、环境自检与后台常驻守护安装：

```sh
# 1. 编译本地二进制（依赖 Go 1.26+，仅使用标准库，无运行时外部依赖）
go build -o aw-watcher-herdr .

# 2. 一键环境诊断（自动排查 Herdr CLI、Unix Socket、ActivityWatch API 及守护服务）
./aw-watcher-herdr doctor

# 3. 安装为开机自启的后台守护服务（macOS LaunchAgent / Linux systemd）
./aw-watcher-herdr service install
```

查看服务状态与日志路径：

```sh
./aw-watcher-herdr service status
```

---

## 核心特性 (Key Capabilities)

* **单 Bucket 统一建模（零双重计时）**：彻底告别多 Bucket 带来的统计虚胖与时间加和翻倍。每台主机严格维护单个 Bucket（`aw-watcher-herdr_<hostname>`），一条时间轴精准映射真实挂钟时间。
* **全景人机协同切片**：单条事件同时聚合“人类视觉焦点（`focused`）”与“集群并发状态（`agents`）”。
  * 自动标记 `interaction`（`supervised` 监督协作 / `autonomous` 离线自主 / `manual` 纯人工终端）。
  * 顶层提权 `dominant_state`（`working` / `blocked` / `idle`），无需遍历数组即可直观反映集群整体状态。
* **ActivityWatch 原生分类友好**：
  * 事件标题规范化为 `[<project>] <dominant_state> (<interaction>)`（如 `[modelcards] working (supervised)`）。
  * 完美适配 ActivityWatch `Settings -> Categorization` 正则提取规则，无需复杂脚本即可在 Sunburst 环形图与 Top Titles 排行中自动分组。
* **细粒度工作区与项目感知**：通过本地 Unix Socket 监听 Herdr 拓扑事件（`pane_focused`、`tab_focused`、`workspace_focused`），以秒级精度记录项目专注时长。工作区未打标签时，自动回退解析为当前文件目录名（`project` 智能 Fallback）。
* **系统级自愈与用户服务管理**：内置 LaunchAgent（macOS）与 systemd（Linux）管理能力，支持服务 `install`、`status`、`restart`、`stop`、`uninstall`。自动处理路径转义，并主动保护 Nix / Home Manager 等外部只读配置。
* **只读对接与本地隐私边界**：仅通过本地 Socket 读取状态，不向终端发送字符、不采集控制台正文与按键输入、不建立远程 SSH 网络连接。

---

## 数据结构与示例 (Data Schema)

数据自动上报至 ActivityWatch，每个主机仅维护单个核心 Bucket：

* **Bucket 命名**：`aw-watcher-herdr_<hostname>`（展示名：`Herdr (<host>)`）
* **事件类型**：`herdr.status`
* **事件载荷示例**：
  ```json
  {
    "app": "Herdr",
    "title": "[modelcards] working (supervised)",
    "project": "modelcards",
    "primary_project": "modelcards",
    "primary_agent": "codex",
    "dominant_state": "working",
    "interaction": "supervised",
    "machine": "Local",
    "machine_id": "local",
    "session": "/Users/example/.config/herdr/herdr.sock",
    "active_count": 2,
    "working_count": 1,
    "blocked_count": 0,
    "connection_epoch": "epoch_65c77b",
    "focused": {
      "workspace_id": "w1W",
      "tab_id": "w1W:t1",
      "pane_id": "w1W:p1",
      "terminal_id": "term_65c77b08d74c745",
      "project": "modelcards",
      "cwd": "/Users/example/workspaces/modelcards/src",
      "foreground_cwd": "/Users/example/workspaces/modelcards/src",
      "agent": "codex",
      "status": "working",
      "execution_mode": "supervised",
      "scope": "machine-selection-and-server-focus"
    },
    "agents": [
      {
        "agent": "codex",
        "status": "working",
        "project": "modelcards",
        "workspace_id": "w1W",
        "tab_id": "w1W:t1",
        "pane_id": "w1W:p1",
        "terminal_id": "term_65c77b08d74c745",
        "cwd": "/Users/example/workspaces/modelcards/src",
        "foreground_cwd": "/Users/example/workspaces/modelcards/src",
        "is_focused": true,
        "execution_mode": "supervised",
        "is_terminal": false
      },
      {
        "agent": "claude",
        "status": "idle",
        "project": "docs",
        "workspace_id": "w2D",
        "tab_id": "w2D:t1",
        "pane_id": "w2D:p1",
        "terminal_id": "term_92a34b11f01c823",
        "cwd": "/Users/example/workspaces/docs",
        "foreground_cwd": "/Users/example/workspaces/docs",
        "is_focused": false,
        "execution_mode": "autonomous",
        "is_terminal": false
      }
    ]
  }
  ```

---

## ActivityWatch 分析与查询 (Analytics & AQL)

### 1. 原生分类配置规则 (Category Rules)

在 ActivityWatch Web UI 的 `Settings` -> `Categorization` 中，可直接利用 `title` 格式配置规则：

* **按项目自动归类**：添加规则 `title` 正则匹配 `^\[(?P<project>[^\]]+)\]`。
* **人机协作模式识别**：
  * 规则包含 `supervised` -> 标记为 `AI Pair Programming`
  * 规则包含 `autonomous` -> 标记为 `AI Background Run`
  * 规则包含 `manual` -> 标记为 `Manual Terminal`

### 2. AQL 统计查询示例

在 ActivityWatch Web UI 的 `Query` 面板中，可直接执行以下 AQL 统计今天各项工作模式的耗时分布：

```python
events = query_bucket(find_bucket("aw-watcher-herdr_"));

# 按人机交互模式分段统计时长
supervised = filter_keyvals(events, "interaction", ["supervised"]);
autonomous = filter_keyvals(events, "interaction", ["autonomous"]);
manual = filter_keyvals(events, "interaction", ["manual"]);

# 按项目聚合
by_project = group_by_field(events, "primary_project");

RETURN = {
    "supervised_duration": sum_durations(supervised),
    "autonomous_duration": sum_durations(autonomous),
    "manual_duration": sum_durations(manual),
    "projects": sum_durations(by_project)
};
```

---

## 常用命令与配置参考 (Commands & Options)

### 交互运行与诊断

```sh
./aw-watcher-herdr doctor          # 环境诊断（CLI、Socket、AW、服务状态）
./aw-watcher-herdr --dry-run       # 仅在终端打印 JSON 事件，不写 AW
./aw-watcher-herdr --version       # 查看版本 (v0.2.0)
./aw-watcher-herdr --help          # 查看完整参数
```

### 服务管理命令

```sh
./aw-watcher-herdr service install [flags]   # 创建用户服务并启动开机自启
./aw-watcher-herdr service status            # 查看服务状态、配置路径与日志文件
./aw-watcher-herdr service restart           # 热重启服务 (macOS kickstart -k / Linux systemctl restart)
./aw-watcher-herdr service stop              # 暂停服务并临时禁用登录自启
./aw-watcher-herdr service start             # 恢复服务并启用登录自启
./aw-watcher-herdr service uninstall         # 停止服务并移除生成的配置文件
```

### 核心参数矩阵

所有参数均提供默认值：

| 参数 | 默认值 | 说明与规则 |
|---|---|---|
| `--socket` | 自动推导 | 本地 Herdr Unix Socket 路径。优先级：Flag > `HERDR_SOCKET_PATH` > `$XDG_CONFIG_HOME/herdr/herdr.sock` > `~/.config/herdr/herdr.sock`。 |
| `--aw-url` | `http://127.0.0.1:5600` | ActivityWatch API 地址。 |
| `--herdr` | `herdr` | 本地 Herdr 可执行文件路径（用于机器状态探测）。 |
| `--heartbeat-interval` | `1s` | 状态未变动时维持脉冲的基准间隔。 |
| `--pulsetime` | `2s` (自动计算) | ActivityWatch 合并相邻相同事件的时间窗口（缺省时为 `heartbeat*1.5`）。 |
| `--retry-interval` | `10s` | 断线重连等待间隔。 |
| `--timeout` | `5s` | 单次请求及系统调用超时。 |

---

## 工程边界与设计权衡 (Design Boundaries)

本项目的系统边界与设计考虑如下：

1. **远程机器隔离（Remote Isolation）**：
   * 本程序定位为单一本地节点的采集器，不通过 SSH 访问远程机器。
   * 当用户在 Herdr 中选中远程机器时，本地焦点会自动挂起（事件中 `focused` 节点置空，`interaction` 自动切换为 `autonomous`），防止将远程作业污染为本地活动；与此同时，本地后台正在运行的 Agent 依然持续采集。
2. **关于锁屏与离开（AFK）的解耦**：
   * 采集器记录的是 Herdr 服务端视角下的窗格聚焦。若用户最小化窗口或离开电脑，Herdr 内部仍保留该分屏的选中状态。
   * 如需计算排除离席后的净工时，可在 ActivityWatch 查询中将 `aw-watcher-herdr_<hostname>` 与 `aw-watcher-window`（窗口前台）或 `aw-watcher-afk`（离席监测）做时间交集求交（`filter_period_intersect`）。
3. **内存缓冲与去重防污染（Epoch Protection）**：
   * 进程不在本地磁盘持久化待发送队列。若 ActivityWatch 短暂下线，恢复后 Watcher 会分配全新的 `delivery_epoch`，防止将故障前后的时间拼接为虚假连续工作。

---

## 参考与协议 (References)

* [ActivityWatch Watcher 规范](https://docs.activitywatch.net/en/latest/watchers.html#custom-watchers)
* [Herdr Socket API 规范](https://herdr.dev/docs/socket-api/)
