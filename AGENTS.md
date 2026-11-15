## Agent skills

### Issue tracker

Issues live in GitHub Issues on `jacob-bits/SimpleAgent` (origin), via the `gh` CLI. See `docs/agents/issue-tracker.md`.

### Triage labels

Default five-role vocabulary (`needs-triage`, `needs-info`, `ready-for-agent`, `ready-for-human`, `wontfix`). See `docs/agents/triage-labels.md`.

### Domain docs

Single-context: root `GLOSSARY.md` + `docs/adr/`. See `docs/agents/domain.md`.

## 文档治理

**单一事实源**：每个语义只有一处正文，其余地方只放指针。复述即漂移——两处都在描述同一件事的那天，就是它开始分叉的那天。

- **代码行为**只写在模块文档（`agent.md`、`cli.md`、`config.md`、`llm.md`、`tool.md`）；`architecture.md` 只写跨模块关系，不重述模块内部。
- **契约**（字段、flag、退出码、JSON 形状）不抄进文档，指向代码；文档只写代码不会自己说的：边界与不变量、为什么、坑、验证命令。
- **词义**以 `GLOSSARY.md` 为准，**被否决的方案**以 `docs/adr/` 为准。发现文档与代码不一致时，改文档或开 issue 记录设计意图，不让两种说法并存。
