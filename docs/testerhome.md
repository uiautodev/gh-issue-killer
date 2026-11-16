# 把 GitHub issue 交给 AI：一条评论走完计划、实现、CI 和合并

过去半年我大部分代码是让编码 agent 写的，但真正花时间的不是写代码，而是写代码
前后的那些事：读 issue、拆任务、盯 PR 的检查、解冲突、等 CI 绿了再点合并。于是
我写了个小工具 [gh-issue-killer](https://github.com/uiautodev/gh-issue-killer)：
一个 Go 单文件程序，监听 GitHub issue，让 opencode / Claude Code / Codex 在独立
worktree 里把活干完，你用评论控制节奏。下面按"为什么"和"怎么用"讲一遍。

## 先说痛点

- **issue 堆着没人动。** 读需求、写计划、实现、验证占掉整块时间，PR 一直挂着。
- **PR 的检查和冲突要人盯。** CI 变红、job 卡在已下线的 runner、分支冲突，都要
  手动查、手动修。
- **让 agent 直接改工作区太危险。** 改到一半失败，或者跑到一半断开，工作区就脏了。
- **仓库一多就互相堵。** 一个慢 CI 占住所有任务，只能给每个仓库再开一个 daemon。

这些问题本质上是同一件事：**流程的状态没有地方存，所以每推进一步都要人记着。**
gh-issue-killer 的做法是把状态放进 `state.json`，把每一步变成可恢复的阶段。

## 一条 issue 的完整生命周期

不用背命令，正常流程只有两步需要你出手。

1. **计划。** 新建 issue 并 `@yourname-agent`（或把 issue assign 给它）。daemon 从
   base 分支拉一个 detached worktree，agent 看过需求和仓库后发一条计划评论，标上
   `agent:awaiting-approval`，批准之前不写代码。带 `bug` 标签的 issue 跳过计划，直接复现、修复
   并补回归测试。
2. **批准。** 回 `@agent lgtm`（`continue`、`继续` 也行），或者直接给计划评论点 👍。
   在同一个 worktree 里开始写代码，必要时带上你补充的要求。
3. **实现与本地检查。** agent 除了改代码，还要写两个文件：`result.json` 汇报改了
   什么、怎么测的；`checks.json` 说明要跑哪些检查命令和超时。检查由 daemon 执行，
   失败就把输出回给 agent 修，默认最多 3 轮（`maxFixRounds`）。
4. **开 PR。** 检查通过后提交、push、开 PR，body 里带 `Closes #n`，并贴一条"实现
   完成"评论。原来是 UI 改动的，PR 里会有改动区域的前后对比截图。
5. **CI 门禁。** daemon 自己盯 PR 上的 check runs：等新提交的检查出现，失败就按日
   志修（`ci_fix`），job 卡住、需要维护者批准、分支冲突都能识别并处理。
6. **合并。** 回 `@agent 合并`，或者给 PR 公告评论点 👍：再解一次冲突、跑本地检查、
   过 CI，然后 squash merge、关 issue、删分支和 worktree。

每个阶段都写在状态里，所以重启 daemon 会接着做；关掉 issue 会取消任务并关闭 PR，
重新打开会继续。任务只是在等你看一眼时才会停下（`blocked`），比如检查修了几轮仍
然失败。

## 全程都是评论

| 评论 | 效果 |
| --- | --- |
| `@agent lgtm`、`@agent continue`、`@agent 继续` | 批准计划（或 blocked 后重试） |
| `@agent 合并`、`@agent merge`、`@agent ship` | squash merge PR，关 issue + PR，清理 worktree |
| `@agent review`、`@agent cr`（在 PR 上） | 审查这个 PR 的 diff，发表总结 + 行内意见 |
| `@agent <指令>`（在 PR 上） | 直接改这个 PR：改代码、跑检查，把提交推到 PR head |
| `@agent apply`、`@agent 应用`（在 PR 上） | 直接按最近一次 review 的意见修改，不再重跑 review |
| `@agent 合并`、`@agent merge`（在 PR 上） | 解冲突、跑检查和 CI，然后 squash merge 这个 PR |
| `@agent cancel`、`@agent stop`、`@agent 停止` | 中止当前任务 |
| plan 评论上的 👍 | 等同 `@agent lgtm` |
| PR 公告评论上的 👍 | 等同 `@agent 合并` |
| bot 那条"Review 已提交"评论上的 👍 | 按这条 review 的意见直接改 PR 并推送 |

两条规则值得记一下：命令必须来自 `approvers` 里的人、必须 @ 了 agent、而且
**整条评论就是命令**；其它评论（比如在 PR 上提意见）会被当成修改要求，交给同一个
worktree 里的 agent 处理。

## 怎么装

它是单个静态二进制，运行时只需要 `git`、一个编码 agent CLI 和 `/bin/sh`，只发起
出站 HTTPS，不需要开任何入站端口。

最省事的办法是把仓库交给 Claude Code、Codex 或 opencode，让它按 `DEPLOY.md` 自己
部署。仓库里有一段现成的 prompt，大意是"按第 0–5 节装好、自己推断默认值、最多问
一个问题"，agent 会装二进制、写配置、跑冒烟测试、起 daemon，只在需要你用 GitHub 小号
登录（内置 Device Flow）时停下来——那一步只能你本人操作。

手动安装也就四行：

```bash
go build -o bin/gh-issue-killer ./cmd/gh-issue-killer
./bin/gh-issue-killer -init      # 向导：仓库、agent、approvers、代理
./bin/gh-issue-killer            # 后台 daemon
./bin/gh-issue-killer -status    # pid、配置、日志和状态路径
```

配置是 `repos` / `codingAgent` / `approvers` / `proxy` 几个字段，每个仓库可以单独
覆盖 agent、model、approvers 和语言。保存配置就会热重载，不用重启。

## 需要的时候有个看板

配置 `webPort`（比如 `8787`）就能打开一个只在 `127.0.0.1` 上监听的看板：每个仓库
自己的队列和并发表、阶段进度、PR、检查结果、截图，计划按 Markdown 渲染。顶部显示
"待我处理"的任务数（待批准、待合并、被阻塞），任务列表能按项目过滤、按关键字搜索，
也能一键隐藏已完成和已取消。界面可以中英文切换，主题跟随系统或固定日间/夜间。

![看板](https://testerhome.com/uploads/photo/2026/e756c508-9eba-43c4-af3d-4186698eb624.png!large)

*看板截图：左边是各仓库的队列余量，右边是每个 issue 的阶段、PR、检查进度。*

## 边界，先说清楚

- **它不是"全自动写代码然后自己合"。** 计划和合并这两步默认要人批准或点 👍，这是
  刻意留的刹车。
- **它需要一个独立的 bot 账号。** token 只给 bot，approver 是人；daemon 不会去用
  你本机的 `gh auth token`。
- **单用户单实例运行**，一个 state 目录对应一个 daemon。macOS、Linux、Windows 都能跑；
  Windows 上也可以直接用桌面版，不用开 daemon，关窗即停。
- **语言跟随 issue。** 计划、总结、PR body 用 issue 或 PR 的语言写；daemon 自己的
  标题、提示这类脚手架文案目前有中英文两套。

如果想看看它在真实仓库里跑一整条链路，可以先跑 `scripts/smoke-test.sh owner/repo`。

代码是 MIT 协议：[github.com/uiautodev/gh-issue-killer](https://github.com/uiautodev/gh-issue-killer)。
用着有问题、或者你的流程跟它不一样，欢迎开 issue 直接 `@` 它自己——它就是这么被
开发出来的。
