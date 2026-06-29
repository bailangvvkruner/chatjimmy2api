# chatjimmy2api

OpenAI 兼容 API 代理，将 [chatjimmy.ai](https://chatjimmy.ai) 的 API 转换为标准 OpenAI API 格式。

## 特性

- ✅ OpenAI 兼容的 `/v1/chat/completions` 接口（流式 + 非流式）
- ✅ 工具调用（Tool Calling / Function Calling）
- ✅ 多模型选择（12 个模型，可自定义）
- ✅ 多模态 content 数组 → 纯文本自动转换
- ✅ 超长上下文自动截断（适配上游 48KB 请求限制）
- ✅ 流式输出附带 Token 统计（`Generated in Xms • Y tok/s`）
- ✅ 内置调试日志端点 `/v1/admin/logs`
- ✅ 健康检查 `/health`、模型列表 `/v1/models`
- ✅ 静态二进制，零依赖（scratch 镜像）

## 快速开始

```bash
docker run -d \
  --name chatjimmy2api \
  -p 28094:28094 \
  -e PORT=28094 \
  -e API_KEY=your-api-key \
  bailangvvking/chatjimmy2api
```

## 环境变量

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `PORT` | `28094` | 监听端口 |
| `LISTEN_ADDR` | (空) | 完整监听地址，设置后覆盖 `PORT` |
| `API_KEY` | `sk-1234567890+abc` | Bearer Token 认证 |
| `UPSTREAM_URL` | `https://chatjimmy.ai/api/chat` | 上游 API 地址 |
| `UPSTREAM_MODELS` | (内置 12 个模型) | 自定义模型列表，逗号分隔 |
| `LOG_LEVEL` | `info` | 日志级别：`error` / `warn` / `info` / `debug` |

### LOG_LEVEL

| 级别 | 输出内容 |
|------|----------|
| `error` | 仅错误 |
| `warn` | 错误 + 警告 |
| `info` | 错误 + 警告 + 信息（默认） |
| `debug` | 全部，含详细请求/响应日志 |

生产环境建议 `-e LOG_LEVEL=error`，排查问题时设为 `debug`。

### 自定义模型列表

```bash
docker run -d \
  --name chatjimmy2api \
  -p 28094:28094 \
  -e API_KEY=your-api-key \
  -e UPSTREAM_MODELS="llama3.1-8B,llama3.1-70B,gpt-4o" \
  bailangvvking/chatjimmy2api
```

默认模型（12 个）：

`llama3.1-8B`, `llama3.1-70B`, `gpt-4o`, `gpt-4o-mini`, `claude-3.5-haiku`, `qwen2.5-7B`, `qwen2.5-72B`, `deepseek-v3`, `deepseek-r1`, `gemini-2.0-flash`, `gemini-2.5-pro`, `mistral-large`

## API 端点

| 路径 | 方法 | 说明 |
|------|------|------|
| `/health` | GET | 健康检查 |
| `/v1/models` | GET | 模型列表 |
| `/v1/chat/completions` | POST | 聊天补全（流式/非流式） |
| `/v1/admin/logs` | GET | 最近 500 条日志（需 API Key 认证）|

## 使用示例

### cURL

```bash
# 聊天
curl -X POST http://localhost:28094/v1/chat/completions \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"llama3.1-8B","messages":[{"role":"user","content":"Hello"}],"stream":false}'

# 流式
curl -N -X POST http://localhost:28094/v1/chat/completions \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"llama3.1-8B","messages":[{"role":"user","content":"Hello"}],"stream":true}'

# 查看运行时日志
curl http://localhost:28094/v1/admin/logs \
  -H "Authorization: Bearer $API_KEY"
```

### OpenAI Python SDK

```python
from openai import OpenAI

client = OpenAI(
    base_url="http://localhost:28094/v1",
    api_key="your-api-key",
)

response = client.chat.completions.create(
    model="llama3.1-8B",
    messages=[{"role": "user", "content": "Hello"}],
    stream=True,
)
for chunk in response:
    print(chunk.choices[0].delta.content or "", end="")
```

### 工具调用

```python
response = client.chat.completions.create(
    model="llama3.1-8B",
    messages=[{"role": "user", "content": "Whats the weather in Beijing?"}],
    tools=[{
        "type": "function",
        "function": {
            "name": "get_weather",
            "description": "Get weather for a city",
            "parameters": {
                "type": "object",
                "properties": {"location": {"type": "string"}},
                "required": ["location"],
            },
        },
    }],
)
print(response.choices[0].message.tool_calls)
```

## 本地编译

```bash
CGO_ENABLED=0 go build -ldflags="-s -w" -trimpath -o chatjimmy2api .
upx --best --lzma chatjimmy2api   # 可选：压缩到 ~3MB
```

## 构建状态

GitHub Actions 自动构建并推送到 Docker Hub。

[![Docker Image CI/CD](https://github.com/bailangvvkruner/chatjimmy2api/actions/workflows/build.yml/badge.svg)](https://github.com/bailangvvkruner/chatjimmy2api/actions/workflows/build.yml)

## 技术说明

### 请求大小限制

上游 `chatjimmy.ai` 对 JSON 请求体有约 48KB 的限制，超限返回 HTTP 200 空 body。代理会自动截断中间的历史消息，确保请求体控制在 35KB 以内。

### Content 数组兼容

AstrBot 等客户端发送的多模态 content 数组格式 `[{"type":"text","text":"..."}]` 会自动转换为纯文本字符串后转发。

### 流式输出统计

流式结束时自动追加 Token 统计：
```
data: [DONE]

data: Generated in 1234ms • 567 tok/s
```

## License

MIT
