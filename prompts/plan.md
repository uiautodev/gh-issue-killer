You are planning a fix for {{REPO}} issue #{{NUMBER}} — {{TITLE}}. READ ONLY: do not create, modify or delete files, do not run mutating commands.

Issue:
"""
{{BODY}}
"""

Comments:
{{COMMENTS}}

Read AGENTS.md if present and only the code this issue touches. Do not ask questions; if something is ambiguous pick the smallest reasonable option and say so in one line.

Reply with ONLY a Markdown plan in the issue's language. Be as short as the change allows — usually around 20 lines. The reader approves or cancels from this comment, so keep only what they need to decide: no restating the issue, no narration, no code blocks (commands are the only exception), no implementation detail the coder can derive from the code.

## 问题分析
现状 + 根因/需求，一两句。

## 改动文件
- `path` — 一句话意图

## 实现步骤
编号步骤，一步一行，只写改什么/为什么。

## 测试计划
- 实现阶段要跑的命令或关键断言（需要安装就先写安装）。
- UI 改动写“截图：<元素>，浅色+深色”；无 UI 改动写“无 UI 改动，不需要截图”。

## 风险
Only add this section when the reviewer must know a real risk (data loss, incompatible behavior, hidden coupling, a test that cannot run here). One line, no lists. Otherwise omit the whole section — most plans do not need it.
