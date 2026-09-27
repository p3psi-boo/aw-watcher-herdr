# aw-watcher-herdr 将本地 Herdr 活动写入 ActivityWatch。

`aw-watcher-herdr` 是用 Go 编写的后台采集程序，用于在 ActivityWatch 中记录本地 Herdr 的焦点和 agent 状态。焦点指 Herdr 服务器记录的当前工作区、标签页和终端区域；agent 指在 Herdr 终端中运行、由 Herdr 识别的 AI 编程程序。

程序只访问本地 Herdr，不建立 SSH 连接，也不采集远程机器的状态。选中远程机器时，程序暂停本地焦点记录，本地后台 agent 的状态记录继续。

从源码构建后，按下文设置时间参数即可运行。记录默认写入 ActivityWatch；加上 `--dry-run` 后，记录改为输出到终端。程序不发送终端输入，不修改 Herdr 配置，也不启动或停止 Herdr 服务。

## 构建和运行需要准备本地依赖。

当前实现面向 macOS 和 Linux，通过 Unix socket 连接 Herdr。Unix socket 是同一台机器上进程之间的通信接口。

构建需要 Go 1.26，版本声明见 [`go.mod`](go.mod)。代码仅使用 Go 标准库，构建后的程序运行时无需 Go、Python 或 SSH。

运行前需要准备以下服务：

- 本地 Herdr 服务器须提供 `session.snapshot` 和 `events.subscribe` 接口，分别用于读取当前状态和订阅变化。
- 本地 Herdr 命令行程序须提供 `herdr machine list --json`，用于查询当前选中的机器。
- 写入记录时需要运行 ActivityWatch；`--dry-run` 模式不访问 ActivityWatch。

本项目已在本机 Herdr 0.9.0 上验证接口。程序不依赖该版本缺少的全局 `--machine` 参数。

在项目目录执行以下命令，构建程序、运行检查并查看参数说明：

```sh
go build -o aw-watcher-herdr .
go test -race ./...
go vet ./...
./aw-watcher-herdr --help
```

## 启动前需要明确设置时间参数。

程序通过 heartbeat 向 ActivityWatch 报告持续状态。Heartbeat 是带时间的状态记录；ActivityWatch 可以将时间相邻且数据相同的记录合并为一个区间。

所有时间参数均为必填项，值须大于零，没有默认值。参数采用 Go duration 格式，即数字后接时间单位，例如 `ms`、`s` 或 `m`。运行者需要自行确定时间值，测试代码中的数值不作为运行默认值。

| 参数 | 参数的作用和约束 |
|---|---|
| `--selection-interval` | 程序完成一次机器列表查询后，等待这段时间再查询。两次查询之间的短暂切换可能未被记录。 |
| `--heartbeat-interval` | 程序按此间隔检查本地服务器并刷新状态。数据没有变化时，两次报告至少间隔这段时间。 |
| `--pulsetime` | ActivityWatch 使用此时间窗口合并相邻的相同记录。值须不小于 `--heartbeat-interval`，否则正常报告也可能被分成不同区间。 |
| `--retry-interval` | 本地 Herdr 连接失败后，程序等待这段时间再重连。 |
| `--timeout` | 单次 HTTP 请求、本地 Herdr 请求和机器列表查询使用此超时。 |

设置下列环境变量后，执行启动命令。命令中的变量只用于向程序传入参数；变量未设置或为空时，shell 会提示缺少的值。

```sh
./aw-watcher-herdr \
  --selection-interval "${SELECTION_INTERVAL:?请设置机器选择查询间隔}" \
  --heartbeat-interval "${HEARTBEAT_INTERVAL:?请设置状态检查与报告间隔}" \
  --pulsetime "${PULSETIME:?请设置ActivityWatch合并窗口}" \
  --retry-interval "${RETRY_INTERVAL:?请设置重连间隔}" \
  --timeout "${REQUEST_TIMEOUT:?请设置请求超时}"
```

首次运行时，在命令末尾加上 `--dry-run`。检查终端输出中的来源、目录和状态，确认后去掉该参数，开始写入 ActivityWatch。两种模式都只对本地 Herdr 执行查询和订阅。

其余参数均为可选项：

| 参数 | 默认值与行为 |
|---|---|
| `--aw-url` | 默认值为 `http://127.0.0.1:5600`。使用其他地址或测试服务器时，显式传入完整的 HTTP 或 HTTPS 地址。 |
| `--socket` | 指定本地 Herdr socket 路径。未指定时，程序优先读取 `HERDR_SOCKET_PATH`，否则使用 `$XDG_CONFIG_HOME/herdr/herdr.sock`；`XDG_CONFIG_HOME` 未设置时使用 `~/.config/herdr/herdr.sock`。命名会话应明确传入路径。 |
| `--herdr` | 指定本地 Herdr 可执行文件，默认从 `PATH` 查找 `herdr`。 |
| `--hostname` | 指定采集端身份，默认使用操作系统返回的主机名。该值参与 ActivityWatch 记录集合的命名。 |
| `--dry-run` | 默认关闭。启用后将记录写到标准输出，不访问 ActivityWatch。 |

## 程序结合事件订阅和定期查询采集状态。

Herdr 保存终端状态，程序通过本地连接读取状态并接收变化，再将记录发送给 ActivityWatch。代码中的主要对象如下。

| 对象 | 代码名称 | 对象的用途 |
|---|---|---|
| 机器配置 | `Machine` | 保存机器标识和选择状态。程序通过本地命令行查询机器列表。 |
| 终端区域 | `Pane` | 对应 Herdr 中的一个 pane，即分割后的终端区域。 |
| 状态快照 | `Snapshot` | 保存一次查询得到的焦点、工作区、终端区域和布局。 |

程序先读取本地 pane 列表，为各 pane 构造 agent 状态订阅；建立订阅后，再读取完整状态。订阅连接在完整状态查询期间已经开始接收事件，程序随后按接收顺序处理。普通请求使用独立连接，符合本机 Herdr 0.9.0 在单次响应后关闭连接的行为。

焦点和 agent 状态事件直接更新程序保存的状态。工作区信息等事件会触发状态查询。新增、关闭或移动 pane 后，程序重建订阅并重新划分记录区间，以覆盖变化后的 pane ID。程序还会定期查询状态，刷新目录并检查连接。

机器选择没有采用事件订阅。程序定期调用本地命令读取机器列表，不会因为远程配置已保存或已启用而创建远程连接。

运行时会增加采集进程、本地请求和 ActivityWatch 写入。Go 编译只发生在构建阶段。资源开销尚未做基准测量。

## 焦点记录和 agent 状态需要分别解读。

ActivityWatch 用 bucket 保存同类事件，每个 bucket 是一个记录集合。程序分别使用 `herdr.focus` 和 `herdr.agent.status` 两种类型。

### 焦点记录表示本地服务器的选择状态。

焦点记录的 `scope` 固定为 `machine-selection-and-server-focus`。程序按以下条件决定是否报告焦点：

| 机器列表的查询结果 | 焦点记录的行为 |
|---|---|
| 查询成功，所有远程配置均未选中。 | 程序按当前已验证的单客户端行为将选择解释为 Local，并记录本地焦点。 |
| 查询成功，某个远程配置被选中。 | 程序暂停焦点报告，跳过远程工作区和 pane 的状态。 |
| 查询失败。 | 程序暂停焦点报告，避免将未知选择归为本地活动。 |
| 选择从远程或未知状态返回 Local。 | 程序使用新的选择标识恢复报告，不合并暂停期间的时间。 |

程序尚未结合桌面前台窗口和 ActivityWatch 的离开状态。因此，关闭 Herdr 界面后，服务器保留的焦点仍可能持续记录。多个客户端同时连接时，现有记录也没有客户端身份来确定焦点属于哪个桌面窗口。

焦点记录不直接代表个人关注时长。计算个人关注时长还需要结合桌面窗口和离开状态。

### Agent 记录表示 Herdr 识别的运行状态。

本地 agent 的状态记录不受机器选择影响。选中远程机器或机器列表查询失败时，本地后台 agent 仍继续记录；远程 agent 始终不采集。

状态识别可能来自屏幕或集成报告。`working` 不等同于模型实际推理时间，`done` 不等同于任务验收通过。并行 agent 分别记录，各 agent 时长之和可能超过实际经过的时间。

## 记录按主机和终端身份保存在 ActivityWatch 中。

每个采集主机使用一个焦点 bucket。Agent bucket 按采集主机、机器配置 ID、会话和 terminal ID 分开保存，避免并行 agent 的报告相互打断；缺少 terminal ID 时使用 pane ID。

程序根据上述身份信息生成完整的 SHA-256 摘要，用于构成 bucket ID。机器、目录和项目等可读信息保存在事件的 `data` 中。

| 字段 | 字段的含义 |
|---|---|
| `machine_id`、`machine`、`session` | 标识数据来源和会话。当前仅记录本地来源，`session` 保存本地 socket 路径。 |
| `workspace_id`、`project`、`tab_id`、`pane_id`、`terminal_id` | 标识对应的 Herdr 对象；`project` 使用工作区标签。 |
| `cwd`、`foreground_cwd` | 保存 Herdr 返回的目录；前台进程目录可能为空。 |
| `agent`、`status`、`scope` | Agent 记录包含前两个字段；焦点记录包含 `scope`。 |
| `connection_epoch`、`selection_epoch`、`delivery_epoch` | 区分不同连接、选择和写入阶段的随机标识，用于避免跨已知中断合并记录。`selection_epoch` 仅出现在焦点记录中。 |

每个 heartbeat 使用采集端的观察时间，并转换为 UTC，即协调世界时。初始时长为零，程序不会预先计入未来时间。状态变化会产生新数据，相邻相同数据由 ActivityWatch 合并。短暂状态的结束时间受报告时机影响，记录不等同于按事件实际发生时刻生成的精确审计日志。

程序不读取终端正文、命令参数或对话内容；目录、机器标签和工作区标签会写入 ActivityWatch。项目尚未提供专用图表，也没有将自定义事件作为内置窗口事件写入。可先在 ActivityWatch 的数据页面查看对应 bucket，再编写所需查询。

## 连接或写入中断后，程序不补算缺失时间。

| 发生的情况 | 程序的处理方式 |
|---|---|
| 本地 Herdr 连接断开。 | 程序暂停报告并按 `--retry-interval` 重连。重连成功后读取当前状态，使用新的连接标识。历史事件不回放，断线期间保持未知。 |
| 机器列表查询失败。 | 程序暂停焦点报告，本地 agent 状态采集继续；焦点恢复条件见前文。 |
| ActivityWatch 写入失败。 | 程序记录错误，在后续报告时再次尝试写入。恢复后使用新的写入标识，不将缺失区间合并为连续活动。 |
| 程序收到 `SIGINT` 或 `SIGTERM`。 | 程序关闭自己的订阅连接并退出，不删除 Herdr 的 socket。 |

当前实现没有持久化待发送队列，ActivityWatch 写入失败期间的数据可能丢失。重连使用显式配置的固定间隔，没有重试次数上限。程序未另外设置事件数量或内容长度限制，操作系统的接口限制仍然适用。

## macOS 可通过 LaunchAgent 管理后台进程。

LaunchAgent 是登录用户的后台任务配置。采集程序本身保持前台运行，由 `launchd` 启动和管理。为避免重复采集，同一采集主机上每个采集端身份只运行一个实例。

配置模板位于 [`examples/org.activitywatch.herdr.plist`](examples/org.activitywatch.herdr.plist)。程序不会自动安装模板。手动配置时按以下步骤操作：

1. 将模板中的可执行文件、socket、日志路径和时间占位符替换为实际值。
2. 创建日志文件的父目录。
3. 将填写后的配置保存到 `~/Library/LaunchAgents/org.activitywatch.herdr.plist`。
4. 校验配置，通过后加载任务并查看状态。

模板没有时间默认值，填写完成后再加载。LaunchAgent 的环境与交互式 shell 不同，因此模板使用本地 Herdr 的绝对路径。

```sh
plutil -lint "$HOME/Library/LaunchAgents/org.activitywatch.herdr.plist"
launchctl bootstrap "gui/$(id -u)" "$HOME/Library/LaunchAgents/org.activitywatch.herdr.plist"
launchctl print "gui/$(id -u)/org.activitywatch.herdr"
```

停止后台任务时执行：

```sh
launchctl bootout "gui/$(id -u)/org.activitywatch.herdr"
```

## 测试覆盖协议、记录分离和中断处理。

| 测试文件 | 文件验证的行为 |
|---|---|
| [`herdr_test.go`](herdr_test.go) | 使用临时 Unix socket 模拟 Herdr，验证普通请求、事件推送、退出清理、断线重连和焦点更新。 |
| [`activitywatch_test.go`](activitywatch_test.go) | 使用临时 HTTP 服务验证 bucket 创建、heartbeat、写入失败后的区间分隔，以及并行 agent 和来源身份之间的 bucket 分离。 |
| [`watcher_test.go`](watcher_test.go) | 验证采集进程的写入和退出，以及远程选中时跳过焦点、查询失败时暂停焦点、返回 Local 后分开记录的行为。 |

执行以下命令运行测试和静态检查：

```sh
go test -race ./...
go vet ./...
```

测试中的时间数值仅用于构造测试输入，不是运行默认值。

## 官方文档说明了程序使用的协议。

- [ActivityWatch watcher 文档](https://docs.activitywatch.net/en/latest/watchers.html#custom-watchers)介绍自定义采集程序。
- [ActivityWatch REST API](https://docs.activitywatch.net/en/latest/api/rest.html)定义 bucket 和 heartbeat 接口。
- [ActivityWatch 数据模型](https://docs.activitywatch.net/en/latest/buckets-and-events.html)说明事件格式和合并行为。
- [Herdr Socket API](https://herdr.dev/docs/socket-api/)定义状态读取与事件订阅。具体可用方法以安装版本的 `herdr api schema --json` 为准。
- [Herdr 多机器说明](https://herdr.dev/docs/connecting-machines/)说明机器配置与服务器之间的关系。
