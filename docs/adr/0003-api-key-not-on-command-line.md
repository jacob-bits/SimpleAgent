# API Key 不进命令行

API Key 只从 `llm.api_key`（不推荐）或 `llm.api_key_env` 指向的环境变量读取；CLI 不提供 `-api-key`，也不打印 Key。Key 为空时允许启动并输出一次 warning，好让本地兼容服务能跑起来。

**为什么**：命令行参数会进 shell 历史、进程列表和 CI 日志，而这是公开仓库，泄露即视为已泄露。少一个便利入口，就少一条泄露路径。

## Consequences

`config.json` 与 `.env` 必须留在忽略列表里；CI 里不允许出现真实 Key。
