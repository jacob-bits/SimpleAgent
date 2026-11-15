# 文档边界跟包边界，根目录不再有 DESIGN.md

每篇模块文档对应一个 `internal/` 包：`agent`（含 parser）、`cli`、`config`、`llm`、`tool`（含 readfile）；总体架构与协议进 `docs/architecture.md`，入口是 `docs/README.md`。`DESIGN.md` 删除，内容并入上述文件。

**为什么**：文档边界比代码边界细时，改一处要读两篇——agent 在 `ls internal/` 里看到什么，就该在 `docs/` 里读到什么。而 `DESIGN.md` 的 §4–§11 几乎每一段都有更权威的模块详设在别处。

## Considered options

- **保留 DESIGN.md 当总纲**：曾考虑让它只留架构、把模块细节用指针下沉。否决原因是总纲会持续吸附细节，而复述一处细节就是两处维护。
