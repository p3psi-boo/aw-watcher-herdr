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

* **细粒度工作区与项目感知**：通过本地 Unix Socket 监听 Herdr 拓扑事件（`pane_focused`、`tab_focused`、`workspace_focused`），以秒级精度记录项目专注时长。工作区未打标签时，自动回退解析为当前文件目录名（`project` 智能 Fallback）。
* **并行 AI Agent 独立采集**：为每个终端会话（Terminal ID）维护独立事件流，并发运行的多个 Agent 互不截断、互不串扰，忠实还原模型工作时长。
* **人机协同双向标注**：
  * 在 Agent 事件中自动注入 `is_focused`（是否正处于用户视觉焦点）。
  * 自动标记 `execution_mode`（`supervised` 人工监督 vs `autonomous` 离线自主执行），下游无需复杂双流 JOIN 即可直接计算 AI 杠杆率。
* **生态兼容与友好展示**：
  * 自动注入 `app: "Herdr"` 与 `title: "[project] <summary>"`，ActivityWatch 原生分类引擎和时间线直接生效。
  * Bucket 创建时附带可读名称（如 `Herdr Agent: codex (modelcards)`），告别一长串不可读的哈希 ID。
* **系统级自愈与用户服务管理**：内置 LaunchAgent（macOS）与 systemd（Linux）管理能力，支持服务 `install`、`status`、`restart`、`stop`、`uninstall`。自动处理路径转义，并主动保护 Nix / Home Manager 等外部只读配置。
* **只读对接与本地隐私边界**：仅通过本地 Socket 读取状态，不向终端发送字符、不采集控制台正文与按键输入、不建立远程 SSH 网络连接。

---

## 数据结构与示例 (Data Schema)

数据自动上报至 ActivityWatch，划分为两个独立的 Bucket 域：

### 1. 人类焦点记录 (`herdr.focus`)
* **Bucket 命名**：`aw-watcher-herdr-focus_<hash>`（展示名：`Herdr Focus (<host>)`）
* **事件载荷示例**：
  ```json
  {
    "app": "Herdr",
    "title": "[modelcards] src",
    "project": "modelcards",
    "cwd": "/Users/example/workspaces/modelcards/src",
    "foreground_cwd": "/Users/example/workspaces/modelcards/src",
    "machine": "Local",
    "machine_id": "local",
    "session": "/Users/example/.config/herdr/herdr.sock",
    "workspace_id": "w1W",
    "tab_id": "w1W:t1",
    "pane_id": "w1W:p1",
    "terminal_id": "term_65c77b08d74c745",
    "scope": "machine-selection-and-server-focus"
  }
  ```

### 2. AI Agent 状态记录 (`herdr.agent.status`)
* **Bucket 命名**：`aw-watcher-herdr-agent_<hash>`（展示名：`Herdr Agent: <agent> (<project>)`）
* **事件载荷示例**：
  ```json
  {
    "app": "Herdr",
    "title": "codex: working [modelcards]",
    "agent": "codex",
    "status": "working",
    "is_focused": true,
    "execution_mode": "supervised",
    "is_terminal": false,
    "project": "modelcards",
    "cwd": "/Users/example/workspaces/modelcards",
    "machine": "Local",
    "machine_id": "local",
    "session": "/Users/example/.config/herdr/herdr.sock",
    "workspace_id": "w1W",
    "tab_id": "w1W:t1",
    "pane_id": "w1W:p1",
    "terminal_id": "term_65c77b08d74c745"
  }
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
   * 当用户在 Herdr 中选中远程机器时，本地焦点（`herdr.focus`）会自动挂起，防止将远程作业污染为本地活动；与此同时，本地后台正在运行的 Agent 依然持续采集。
2. **关于锁屏与离开（AFK）的解耦**：
   * 采集器记录的是 Herdr 服务端视角下的窗格聚焦。若用户最小化窗口或离开电脑，Herdr 内部仍保留该分屏的选中状态。
   * 如需计算排除离席后的净工时，可在 ActivityWatch 查询中将 `herdr.focus` 与 `aw-watcher-window`（窗口前台）或 `aw-watcher-afk`（离席监测）做时间交集求交（`filter_period_intersect`）。
3. **内存缓冲与去重防污染（Epoch Protection）**：
   * 进程不在本地磁盘持久化待发送队列。若 ActivityWatch 短暂下线，恢复后 Watcher 会分配全新的 `delivery_epoch`，防止将故障前后的时间拼接为虚假连续工作。

---

## 参考与协议 (References)

* [ActivityWatch Watcher 规范](https://docs.activitywatch.net/en/latest/watchers.html#custom-watchers)
* [Herdr Socket API 规范](https://herdr.dev/docs/socket-api/)
