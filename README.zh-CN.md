<p align="center"><img src="frontend/static/logo.svg" width="88" height="88" alt="issuekiller"></p>

# issuekiller

[English](README.md)

监听 GitHub issue，让编码 agent（opencode、Claude Code、Codex、Pi 或 Oh My Pi）自己完成
计划 → 实现 → 测试 → 截图 → 开 PR → squash merge，全程由 issue 评论驱动。

## 工作流程

```mermaid
%%{init: {"themeVariables": {"fontSize": "15px"}, "flowchart": {"nodeSpacing": 64, "rankSpacing": 56}}}%%
flowchart LR
    A(["Issue @agent"]) --> B("计划评论")
    B -- "lgtm / 👍" --> C("实现 + 检查")
    C --> D("开 PR")
    D -. "反馈" .-> C
    D -- "合并 / 👍" --> E{"CI"}
    E -- "通过" --> F("Squash merge")

    classDef trigger fill:#1d4ed8,stroke:#1e40af,color:#ffffff
    classDef plan fill:#6d28d9,stroke:#5b21b6,color:#ffffff
    classDef work fill:#0e7490,stroke:#155e75,color:#ffffff
    classDef ci fill:#b45309,stroke:#92400e,color:#ffffff
    classDef done fill:#15803d,stroke:#166534,color:#ffffff

    class A trigger
    class B plan
    class C,D work
    class E ci
    class F done

    %% the dashed back edge (4th declared link) is the feedback loop
    linkStyle 3 stroke:#e11d48,stroke-width:1.5px
```

本地检查和 PR 上的 CI 检查失败会自动修，轮次用完才停在 `blocked`；关闭 issue 会
取消任务并关掉 PR，重新打开会接着做。

默认的 `mode: auto` 会让 agent 先判断需求：写得很清楚就直接实现、跳过计划评论；
`mode: plan` 总是先发计划，`mode: build` 总是不发计划。详见[完整配置项](DEPLOY.md#41-configuration-reference)。

## 解决什么问题

- **issue 堆着没人动。** 读需求、写计划、实现、验证占掉整块时间，PR 一直挂着。
- **PR 的检查和冲突要人盯。** CI 变红、job 卡在已下线的 runner、分支冲突，都要你手动
  查、手动修。
- **agent 直接改工作区太危险。** 改到一半失败或跑到一半断开，工作区就脏了。
- **仓库一多就互相堵。** 一个慢 CI 占住所有任务，只能给每个仓库再开一个 daemon。

## 它能做什么

- **该计划时才计划。** 默认 `mode: auto` 由 agent 判断每个 issue：需求写得很详细、
  改动简单就直接编码；描述含糊、有好几种差不多的合理实现，或提问人明确要求先 plan，
  就先发计划评论等你批准。`mode: plan` 总是先计划，`mode: build` 总是直接编码。
  带 `bug` 标签的 issue 仍然直接复现、修复并补回归测试。
- **每个 issue 一个 worktree。** 独立分支、bot 提交作者，永远不碰你的工作区，
  重启后自动续跑。
- **自己验证、自己修 CI。** 编码 agent 写下 `checks.json`，daemon 负责跑；失败、
  CI 变红或卡住都回到 agent 手里，轮次用完才停在 `blocked`。因 CI 失败停下的
  任务，等 PR 检查转绿后会自动重试合并。
- **一条评论驱动全程。** 批准、改 PR、解冲突、合并、取消都是评论或 👍；
  任何被监听仓库的 PR 都能交给它。
- **多仓库、互不阻塞。** 每个仓库有自己的 agent、model、approvers 和队列，
  一个慢仓库堵不住别的。
- **可选定时发版。** 仓库可以开启自动发版：默认分支有新提交时，`feat:` 提交升
  第二位版本号、其余提交升第三位，按每天或每隔几天、在配置的本地时间（默认
  09:00）发版。重大版本（major）由人工打 tag。默认关闭，按项目单独配置。
- **内置 GitHub 登录。** 不用装 `gh` 或 wrapper：桌面版/看板里点一下就用
  GitHub Device Flow 登录小号，token 存在 `~/.issuekiller/token`（0600），
  快过期时自动续期；也支持粘贴 token。
- **单个静态二进制。** 只发起出站 HTTPS，运行时只需要 `git`、一个编码 agent
  CLI 和一个 shell（`/bin/sh`，Windows 上是 `cmd`）。发布产物覆盖 macOS、
  Linux 和 Windows。

配置 `webPort` 后还能打开实时看板：顶部是吸顶双行头部（品牌与操作、实时
计数），下面有每个仓库的队列与并发表（PLAN/WORK 上限按项目各自计算）、阶段
进度、PR、检查和截图，计划按 markdown 渲染。任务列表在宽屏保留表格列，窄屏
自动切成带字段标签的卡片；仓库概览和控制台是可折叠卡片，默认都收起；任务详情和设置面板
使用原生 `dialog`，键盘焦点与 `Esc` 关闭交给浏览器处理；抽屉里的“批准并开始”按钮
点一下就执行，取消任务则会先确认。主题可跟随系统或固定
日间/夜间，界面可中英文切换、右上角图标栏可跳转本项目并显示仓库星数，还能在宽屏/窄屏布局间切换，任务列表可按项目过滤（下拉可选、支持键盘）或搜索、
也可隐藏已完成和已取消，控制台按级别给日志配色，顶部显示“待我处理”的任务数
（待批准、待合并、被阻塞），只有大于 0 时才放大高亮。设置面板分为登录、仓库、配置、
更新四个标签页，一次只显示一张卡片；仓库页用下拉选择项目，选中后只改这个项目的编码代理、
模型、语言和定时发版，上方的默认设置条显示全局的代理、模型与代理地址；配置页改这些全局默认值
（编码代理、模型、代理、更新镜像、看板端口），更新页显示守护进程的版本、pid、身份、事件来源和
配置文件路径。任何一项留在“跟随全局”时，都不会把当前的全局值写进该项目的配置。看板本身是
`frontend/` 里的 SvelteKit 应用（Tailwind + shadcn-svelte
主题）；发布产物已内嵌构建结果，从源码构建前需要先跑
`npm --prefix frontend run build`。

### 桌面版

不用开终端：从 [Releases](https://github.com/uiautodev/issuekiller/releases)
下载对应平台的桌面包（macOS 用 `.dmg` 拖进「应用程序」或解压 `.app`、
Linux 用 tar 包、Windows 用 zip）双击运行即可。窗口里就是同一套看板，带
**设置** 面板（仓库、编码代理、模型、代理、各项目的定时发版、更新镜像），
以及显示当前版本、可检查并一键升级的更新卡片。首次运行没有配置时会直接停在
设置页，顶部就是 GitHub 登录（请用 GitHub 小号，不要用主账号）；关窗口即停
引擎，不会在后台留进程。看板里的链接会在系统默认浏览器中
打开，桌面窗口内也一样。细节见
[DEPLOY.md](DEPLOY.md#31-desktop-app--optional)。

## 用编码 agent 安装

把仓库交给 Claude Code、Codex、opencode、Pi 或 Oh My Pi，然后把下面这段贴进去：

```text
按照 DEPLOY.md 第 0–5 节在这台机器上部署 issuekiller（除非我要求开机自启，
否则跳过第 6 节）。自主完成，最多问我一个问题，且只在这个值确实无法推断时才问。

默认值，不用确认：
- 监控仓库：当前 checkout 的 origin remote；如果没有就问我一次
- approver：`gh api user --jq .login` 得到的我的 GitHub 登录名
- codingAgent：PATH 里第一个 opencode、claude、codex、pi、omp
- model：留空（CLI 默认）
- proxy：设置了 $https_proxy 或 $ALL_PROXY 就用它，否则直连
- 二进制：~/.local/bin/issuekiller
- 配置：~/.config/issuekiller/config.json

不要创建 GitHub 账号或 token。允许用我的 gh 把上面这些仓库的权限授给这个 bot
（DEPLOY.md 第 2 节）。如果还没有 bot 凭据，就用内置的 GitHub 登录（桌面版或
看板，~/.issuekiller/token），或者提示我用小号登录；不要让我去装 gh-agent。

最后执行第 10 节的检查，把状态行贴出来。不要让我确认你能自己验证的步骤。
```

agent 会按 [DEPLOY.md](DEPLOY.md) 安装二进制、写配置、跑冒烟测试、启动 daemon，
并回报状态。它只会在需要你用 GitHub 小号登录（内置 Device Flow）时停下来。

**手动安装。** 依赖：Go 1.27+、`git`、一个编码 agent CLI，以及一个 GitHub bot
账号（在桌面版/看板里用内置登录，或设置 `GH_AGENT_TOKEN`）。

```bash
go build -o bin/issuekiller ./cmd/issuekiller
./bin/issuekiller -init      # 向导：GitHub 登录、仓库、agent、approvers、代理
./bin/issuekiller            # 后台 daemon
./bin/issuekiller -status    # pid、配置、日志和状态路径
```

从 `gh-issue-killer` 升级？首次启动会把 `~/.gh-issue-killer` 改名为
`~/.issuekiller`（桌面版还会把 `~/.config/gh-issue-killer` 改名为
`~/.config/issuekiller`），token、`state.json` 和 worktree 都会保留。旧的
`GHIK_*` 变量（`GHIK_OAUTH_CLIENT_ID`、`GHIK_CONFIG`、`GHIK_ENGINE`）仍会被读取，
但改名前的版本无法跨过这次改名自动更新：先手动装一次这个版本。

完整的部署、运维、升级和排障见 [DEPLOY.md](DEPLOY.md)；内部实现、状态机、队列
和 CI 门禁见 [DEVELOP.md](DEVELOP.md)，贡献/agent 规则见 [AGENTS.md](AGENTS.md)。

## 日常使用

新建一个 @ 了 bot（或 assign 给 bot）的 issue，然后：

| 评论 | 效果 |
| --- | --- |
| `@agent lgtm`、`@agent continue`、`@agent 继续` | 批准计划（或 blocked 后重试） |
| `@agent 合并`、`@agent merge`、`@agent ship` | squash merge PR，关 issue + PR，清理 worktree |
| `@agent review`、`@agent cr`（在 PR 上） | 审查这个 PR 的 diff，发表总结 + 行内意见 |
| `@agent <指令>`（在 PR 上） | 直接改这个 PR：agent 改代码、跑检查，把提交推到 PR head |
| `@agent apply`、`@agent 应用`（在 PR 上） | 直接按最近一次 review 的意见修改，不再重跑 review |
| `@agent 合并`、`@agent merge`（在 PR 上） | 自动解冲突、跑检查和 CI，然后 squash merge 这个 PR |
| `@agent cancel`、`@agent stop`、`@agent 停止` | 中止当前任务 |
| plan 评论上的 👍 | 等同 `@agent lgtm` |
| PR 公告评论上的 👍 | 等同 `@agent 合并` |
| bot 那条「Review 已提交」评论上的 👍 | 按这条 review 的意见直接改 PR 并推送 |
| bot 那条「出错了 / 被阻塞」评论上的 👍 | 重试被阻塞的任务（等同 `@agent continue`） |

每个被接受的 👍 都会立刻收到一个 👀 回应，任务还在排队或执行时也能看到已经收到。

命令必须来自 approver、@ 了 agent。解析时会忽略引用行，所以引用回复末尾
跟一条命令也能生效。bot 也会回复那些不是命令的 approver 评论，而不是沉默：
blocked 任务上的问题会用失败原因作答，其他补充会记下来留给重试。其他人
@ bot 只会得到一个 👀 确认，不会触发任何动作。批准词、状态标签和 review
的完整规则见 [DEPLOY.md](DEPLOY.md) 和 [DEVELOP.md](DEVELOP.md)。

## 配置

```bash
./bin/issuekiller -init      # 向导：GitHub 登录、仓库、agent、approvers、代理
```

`-init` 会写好配置；手写就是同样的 `repos` / `codingAgent` / `approvers` /
`proxy` 字段。完整配置项（`language`、`mode`、`webPort`、`updateProxy`、每仓库覆盖
（含可选的定时发版）、保存即重载）见
[DEPLOY.md](DEPLOY.md#41-configuration-reference)。打开
`selfUpdate` 后 daemon 会自己安装新版本（校验 checksums，直连失败时走
`updateProxy` 镜像）；也可以在看板里手动检查并升级。

## 开发

```bash
go test ./...
go build -o bin/issuekiller ./cmd/issuekiller
```

内部实现和发版（打 `v*` tag）见 [DEVELOP.md](DEVELOP.md)，贡献规则见
[AGENTS.md](AGENTS.md)。仓库内嵌了 GoogleChrome 的 `modern-web-guidance`
skill（`.agents/skills/`）；改看板的 CSS、布局、表格或弹窗前先检索它。

## 打赏

如果这个项目帮你省下了时间，欢迎扫码请作者喝杯咖啡：

<img src="assets/sponsor.png" alt="打赏二维码" width="240">

## 贡献者

发现 bug 或者有好点子？欢迎[提 issue](https://github.com/uiautodev/issuekiller/issues/new)
或直接提 PR，贡献规则见 [AGENTS.md](AGENTS.md)。感谢每一位贡献者！

<a href="https://github.com/uiautodev/issuekiller/graphs/contributors">
  <img src="https://contrib.rocks/image?repo=uiautodev/issuekiller" alt="issuekiller 的贡献者" />
</a>

## License
[MIT](LICENSE)
