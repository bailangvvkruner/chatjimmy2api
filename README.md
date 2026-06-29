# chatjimmy2api

OpenAI 兼容 API 代理，将 [chatjimmy.ai](https://chatjimmy.ai) 的 API 转换为标准 OpenAI API 格式。

## 特性

- ✅ OpenAI 兼容的 `/v1/chat/completions` 接口
- ✅ 流式（Streaming）与非流式输出
- ✅ 工具调用（Tool Calling / Function Calling）
- ✅ 多模型选择
- ✅ 静态二进制，零依赖（scratch 镜像）
- ✅ 健康检查 `/health`
- ✅ 模型列表 `/v1/models`

## 快速开始

```bash
docker run -d \
  --name chatjimmy2api \
  -p 28094:28094 \
  -e API_KEY=your-api-key \
  bailangvvking/chatjimmy2api
```

## 环境变量

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `PORT` | `28094` | 监听端口 |
| `API_KEY` | `sk-1234567890abc` | Bearer Token 认证 |
| `UPSTREAM_URL` | `https://chatjimmy.ai/api/chat` | 上游 API 地址 |
| `LISTEN_ADDR` | (空) | 完整监听地址，设置后覆盖 `PORT` |

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
docker build -t bailangvvking/chatjimmy2api .
```

## 构建状态

GitHub Actions 自动构建并推送到 Docker Hub。

[![Docker Image CI/CD](https://github.com/bailangvvkruner/chatjimmy2api/actions/workflows/build.yml/badge.svg)](https://github.com/bailangvvkruner/chatjimmy2api/actions/workflows/build.yml)

## License

MIT
