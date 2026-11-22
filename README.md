# DeepSeek Tool MCP

DeepSeek 官方 **Search / Fetch** 能力的 MCP 服务，为 ZCode（以及任意 MCP 客户端）提供**兜底检索**：

- 本地 Playwright / 浏览器自动化 / 爬虫能拿到的内容，不用它；
- 只有遇到反爬、地区/IP 限制、必须走代理、或者需要全球网页索引时，才调用它。

服务名：`deepseek-search`，可执行文件：`deepseek-search-mcp`。

## 为什么是「兜底」

服务在 MCP `initialize` 的 `instructions` 和每个工具的描述里都明确了调用优先级：

| 优先级 | 工具 | 使用场景 |
| --- | --- | --- |
| **T0** | 本地 Playwright、浏览器自动化、交互式浏览、Python 爬虫 | 本机可达的页面一律优先使用 |
| **T1** | `deepseek_search` / `deepseek_fetch` | 反爬/Cloudflare、登录墙、geo/IP 限制、代理场景，或需要全球索引 |

- 本地工具已成功返回的内容，不要用本服务重复抓取。
- 先 `deepseek_search`，再对最有价值的 URL 调用 `deepseek_fetch`。
- 独立 Search API 不支持图片搜索；图片搜索只能通过 DeepSeek 会话内的 `deepseek_web_search` 工具使用，本 MCP 未启用该形态。

## 工具

### `deepseek_search`

调用 `POST https://api.deepseek.com/v1/tools/search`，返回排序后的结构化结果。

| 参数 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `query` | string | 是 | 1–128 字符；支持 `site:`、`-site:`、`"精确短语"`、`-词`、`lang:zh,en`、`after:2025-01-01`、`before:2026-12-31` |
| `count` | integer | 否 | 1–50，默认 10 |
| `ori_question` | string | 否 | 用户原始问题（≤128 字符），帮助搜索引擎理解意图 |
| `localization` | string | 否 | 本地化搜索用的 IP，通常省略 |
| `need_content` | boolean | 否 | 是否返回网页正文，默认 false；正文很占 token，建议先搜索再按 URL 抓取 |

每条结果包含：标题、URL、短摘要、长摘要、站点名、发布时间、权威度（-1 未知，≥3 高权威）、排序分。

示例：

```json
{
  "name": "deepseek_search",
  "arguments": {
    "query": "DeepSeek 官方 联网搜索 API site:api-docs.deepseek.com",
    "count": 5
  }
}
```

搜索语法：

```text
DeepSeek 模型 site:github.com OR site:huggingface.co lang:zh after:2025-01-01
"exact phrase" -excluded-term -site:spam.example.com
```

### `deepseek_fetch`

调用 `POST https://api.deepseek.com/v1/tools/fetch`，把单个网页抓成 Markdown。

| 参数 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `url` | string | 是 | http(s) 绝对 URL，≤8192 字符，不允许携带账号密码 |
| `max_chars` | integer | 否 | 返回 Markdown 的最大字符数，1000–200000，默认 60000 |

```json
{
  "name": "deepseek_fetch",
  "arguments": {
    "url": "https://api-docs.deepseek.com/zh-cn/guides/search",
    "max_chars": 20000
  }
}
```

## 快速开始

### 1. 克隆与构建

需要 Go 1.27+。

```bash
git clone git@github.com:cutejerryc/deepseek-tool-mcp.git
cd deepseek-tool-mcp
```

Windows：

```powershell
.\build.ps1
```

脚本会依次执行 `go test ./...`、构建 `bin\deepseek-search-mcp.exe`，并复制到 `%USERPROFILE%\.local\bin`。如果 `PATH` 中没有 `go`，脚本会尝试 `%USERPROFILE%\.local\go-toolchain\go\bin\go.exe`。

macOS / Linux：

```bash
go test ./...
go build -trimpath -ldflags "-s -w -X main.version=0.1.0" -o bin/deepseek-search-mcp .
install -m 0755 bin/deepseek-search-mcp ~/.local/bin/
```

### 2. 配置 API Key

Windows（用户级，新进程生效）：

```powershell
setx DEEPSEEK_API_KEY "sk-..."
```

macOS / Linux：

```bash
export DEEPSEEK_API_KEY="sk-..."
# 持久化：写入 ~/.bashrc / ~/.zshrc
```

也可以通过 `DEEPSEEK_BASE_URL` 指向代理网关。

### 3. 注册到 ZCode

编辑 `~/.zcode/cli/config.json`：

```json
{
  "mcp": {
    "servers": {
      "deepseek-search": {
        "type": "stdio",
        "command": "C:/Users/<you>/.local/bin/deepseek-search-mcp.exe",
        "args": [],
        "env": {},
        "timeoutMs": 180000,
        "enabled": true
      }
    }
  }
}
```

注意事项：

- `command` 必须是**绝对路径**。ZCode 配置文件不会展开 `${...}` 模板。
- `timeoutMs` 从默认 30000 提高到 180000，避免 `need_content=true` 或大页面抓取超时。
- `env` 留空是刻意的：密钥从进程环境继承，不写入配置文件。
- Windows 路径建议使用正斜杠；Linux/macOS 把 `command` 换成 `/home/<you>/.local/bin/deepseek-search-mcp`。

### 4. 重启客户端

环境变量或配置改动后，**完全退出并重启 ZCode**（包括托盘进程）。随后在 Settings → MCP 中确认 `deepseek-search` 已连接，工具列表中应出现：

- `mcp__deepseek-search__deepseek_search`
- `mcp__deepseek-search__deepseek_fetch`

## 环境变量

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `DEEPSEEK_API_KEY` | 无（必填） | DeepSeek API Key |
| `DEEPSEEK_BASE_URL` | `https://api.deepseek.com` | API 源站，可指向代理网关 |
| `DEEPSEEK_TIMEOUT_SECONDS` | `120` | 单次 HTTP 尝试超时 |
| `DEEPSEEK_MAX_ATTEMPTS` | `3` | 总尝试次数（429/5xx/网络错误重试，遵守 `Retry-After`） |

## 内容预算与限制

- 搜索：每条结果正文最多 20000 字符，所有结果正文合计最多 120000 字符；超出会截断并标注。
- 抓取：默认 60000 字符，超出会标注 `truncated from N`。
- `query` 1–128 Unicode 字符，不能全为空白。
- `count` 1–50。
- `url` ≤8192 字符，仅 http/https，不能带用户名密码。
- 响应体上限 32 MiB；单次 HTTP 尝试超时默认 120s。
- 401/400 等客户端错误不重试；429/500/502/503/504 与网络错误最多重试 3 次。

## 计费

DeepSeek 官方工具价格（2026-11-13 公布）：

| 能力 | 价格 |
| --- | --- |
| Search | **$0.75 / 1K 次请求** |
| Fetch | **$0.75 / 1K 次请求** |

本 MCP 只调用独立 Search / Fetch 接口，不产生模型 token 费用；如果直接使用 `deepseek_web_search` 会话工具，则还会产生 token 费用。

## 开发

```text
.
├─ main.go                     # 启动、环境变量、stdio transport
├─ internal/deepseek/          # HTTP 客户端、重试、错误映射
├─ internal/mcpserver/         # MCP 工具、参数校验、Markdown 格式化
├─ build.ps1
└─ README.md
```

```bash
go test ./...    # 17 项测试（含 in-memory MCP 集成测试）
go vet ./...
```

测试覆盖：鉴权头、请求体、可空字段、401 不重试、429 按 `Retry-After` 重试、5xx 重试上限、Fetch、URL 校验、内容预算截断、MCP 工具列表与调用、API 错误透传。

## 故障排查

| 现象 | 处理 |
| --- | --- |
| `DEEPSEEK_API_KEY is not set` | 环境变量是在客户端启动后才写入的；完全退出并重启客户端 |
| `HTTP 401` / `invalid_api_key` | 检查 key 是否有效、是否被空格污染；必要时重新生成 |
| `HTTP 429` | 已自动按 `Retry-After` 重试；仍失败则降低调用频率 |
| 请求超时 | 调大 `DEEPSEEK_TIMEOUT_SECONDS`，并确认客户端 `timeoutMs` 足够大 |
| 搜索结果为空 | 去掉 `site:` / `after:` 等过滤条件，或换更宽泛的查询 |
| Settings → MCP 显示失败 | 查看错误详情；直接在终端运行可执行文件复现 stderr |
| 修改配置不生效 | 同名用户级 server 会覆盖工作区级；确认编辑的是 `~/.zcode/cli/config.json` |

## 安全

- API Key 只应存在于环境变量或本地密钥管理中，不要提交到仓库。
- 本服务只读外部网页，不修改本地文件；工具已标注 `ReadOnlyHint: true`。
- 抓取结果属于不可信外部内容，使用方应自行做提示注入与内容安全防护。

## 参考

- DeepSeek API 文档：<https://api-docs.deepseek.com/zh-cn/>
- 网页搜索与抓取指南：<https://api-docs.deepseek.com/zh-cn/guides/search>
- Search API Reference：<https://api-docs.deepseek.com/api/search>
- Fetch API Reference：<https://api-docs.deepseek.com/api/fetch>
- MCP 规范：<https://modelcontextprotocol.io/>
- ZCode MCP 配置：`~/.zcode/cli/config.json` → `mcp.servers`

## License

[MIT](LICENSE) © 2026 cutejerryc
