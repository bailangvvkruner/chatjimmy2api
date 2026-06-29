# chatjimmy2api CI/CD 部署指南

## 概述

本项目使用 **GitHub Actions** 自动构建 Docker 镜像并推送到 **Docker Hub**。

| 项目 | 地址 |
|------|------|
| 源代码 | `https://github.com/bailangvvkruner/chatjimmy2api` |
| Docker 镜像 | `bailangvvking/chatjimmy2api` |

---

## 1. 准备工作

### 1.1 创建 GitHub 仓库

在 GitHub 上创建仓库 `bailangvvkruner/chatjimmy2api`（如果尚未创建）。

### 1.2 推送代码

```bash
# 初始化 git（如果尚未初始化）
cd /tmp/opencode/chatjimmy2api
git init
git add .
git commit -m "initial: chatjimmy2api Go API proxy for chatjimmy.ai"

# 添加远程仓库并推送
git remote add origin https://github.com/bailangvvkruner/chatjimmy2api.git
git branch -M main
git push -u origin main
```

### 1.3 配置 GitHub Secrets

在 GitHub 仓库页面进入 **Settings → Secrets and variables → Actions**，添加以下 secrets：

| Secret | 值 |
|--------|----|
| `DOCKERHUB_USERNAME` | 你的 Docker Hub 用户名（`bailangvvking`） |
| `DOCKERHUB_TOKEN` | Docker Hub **Access Token**（不是登录密码） |

**如何获取 Docker Hub Token：**

1. 登录 [hub.docker.com](https://hub.docker.com)
2. 进入 **Account Settings → Security → New Access Token**
3. 创建 Token，权限选 **Read & Write**
4. 复制 Token 粘贴到 GitHub 的 `DOCKERHUB_TOKEN` secret 中

---

## 2. GitHub Actions 工作流

工作流文件：`.github/workflows/build.yml`

### 触发条件

- 推送到 `main` 分支时自动触发
- 也可以在 GitHub Actions 页面手动触发（`workflow_dispatch`）

### 构建流程

```yaml
# 简化的流程说明：
1. Checkout 代码
2. 设置 Docker Buildx
3. 用 DOCKERHUB_USERNAME + DOCKERHUB_TOKEN 登录 Docker Hub
4. 多平台构建镜像并推送（9 个平台架构）
   - bailangvvking/chatjimmy2api:latest
5. Trivy 扫描镜像漏洞，上传 SARIF 报告
```

### Dockerfile 说明

```dockerfile
FROM golang:alpine AS builder    # 编译阶段：安装 upx，CGO_ENABLED=0 编译
RUN upx --best --lzma chatjimmy2api  # UPX 压缩，9MB → ~3MB
FROM scratch                      # 运行阶段：纯静态二进制，零依赖
```

- **基础镜像**: `scratch` — 空镜像，只有编译好的 Go 二进制
- **端口**: `28094`（通过 `PORT` 环境变量可改）
- **认证**: 通过 `API_KEY` 环境变量设置 Bearer token
- **上游**: 默认 `https://chatjimmy.ai/api/chat`，可通过 `UPSTREAM_URL` 修改

---

## 3. 运行

### 3.1 Docker 运行

```bash
# 默认配置
docker run -d \
  --name chatjimmy2api \
  -p 28094:28094 \
  bailangvvking/chatjimmy2api

# 自定义端口 + API Key
docker run -d \
  --name chatjimmy2api \
  -p 8080:8080 \
  -e PORT=8080 \
  -e API_KEY=my-secret-key \
  bailangvvking/chatjimmy2api
```

### 3.2 验证

```bash
# 健康检查
curl http://localhost:28094/health

# 模型列表
curl -H "Authorization: Bearer ${API_KEY}" http://localhost:28094/v1/models

# 聊天（非流式）
curl -X POST http://localhost:28094/v1/chat/completions \
  -H "Authorization: Bearer ${API_KEY}" \
  -H "Content-Type: application/json" \
  -d '{"model":"llama3.1-8B","messages":[{"role":"user","content":"Hello"}],"stream":false}'

# 聊天（流式）
curl -N -X POST http://localhost:28094/v1/chat/completions \
  -H "Authorization: Bearer ${API_KEY}" \
  -H "Content-Type: application/json" \
  -d '{"model":"llama3.1-8B","messages":[{"role":"user","content":"Hello"}],"stream":true}'
```

### 3.3 环境变量

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `PORT` | `28094` | 监听端口 |
| `API_KEY` | `sk-1234567890abc` | Bearer Token 认证 |
| `UPSTREAM_URL` | `https://chatjimmy.ai/api/chat` | 上游 API 地址 |
| `LISTEN_ADDR` | (空) | 完整监听地址，设置后覆盖 `PORT` |

---

## 4. 用 curl 模拟工具调用

```bash
# Step 1: 请求工具调用
curl -s -X POST http://localhost:28094/v1/chat/completions \
  -H "Authorization: Bearer ${API_KEY}" \
  -H "Content-Type: application/json" \
  -d '{
    "model":"llama3.1-8B",
    "messages":[{"role":"user","content":"Whats the weather in Beijing?"}],
    "tools":[{
      "type":"function",
      "function":{
        "name":"get_weather",
        "description":"Get weather for a city",
        "parameters":{
          "type":"object",
          "properties":{"location":{"type":"string"}},
          "required":["location"]
        }
      }
    }]
  }'

# Step 2: 发送工具结果回模型
curl -s -X POST http://localhost:28094/v1/chat/completions \
  -H "Authorization: Bearer ${API_KEY}" \
  -H "Content-Type: application/json" \
  -d '{
    "model":"llama3.1-8B",
    "messages":[
      {"role":"user","content":"Whats the weather in Beijing?"},
      {"role":"assistant","tool_calls":[{"id":"call_xxx","type":"function","function":{"name":"get_weather","arguments":"{\"location\":\"Beijing\"}"}}]},
      {"role":"tool","tool_call_id":"call_xxx","content":"25°C, sunny"}
    ],
    "tools":[{"type":"function","function":{"name":"get_weather","description":"Get weather","parameters":{"type":"object","properties":{"location":{"type":"string"}},"required":["location"]}}}]
  }'
```

---

## 5. OpenAI SDK 兼容使用

```python
from openai import OpenAI

client = OpenAI(
    base_url="http://localhost:28094/v1",
    api_key="sk-1234567890abc",  # 换成你的 API_KEY
)

# 普通聊天
response = client.chat.completions.create(
    model="llama3.1-8B",
    messages=[{"role": "user", "content": "Hello"}],
    stream=True,
)
for chunk in response:
    print(chunk.choices[0].delta.content or "", end="")

# 工具调用
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

---

## 6. 本地构建（不依赖 GitHub Actions）

```bash
# 编译
CGO_ENABLED=0 go build -ldflags="-s -w" -trimpath -o chatjimmy2api .

# 可选：UPX 压缩
upx --best --lzma chatjimmy2api

# Docker 构建
docker build -t bailangvvking/chatjimmy2api .

# 推送
docker push bailangvvking/chatjimmy2api:latest
```
