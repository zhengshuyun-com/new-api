# 提示词审核

## 功能定位

提示词审核是一个异步旁路上报功能, 用于把中转请求中提取出的提示词文本发送到外部审核服务.

它不等待审核服务返回审核结论, 也不会根据外部审核结果拦截当前模型请求. New API 的职责只是提取文本并异步上报, 后续邮件, 封禁或风控动作由外部审核服务完成.

## 执行顺序

一次中转请求进入后, 顺序如下:

1. 解析模型请求.
2. 提取本地 token 统计需要的文本元信息.
3. 执行 New API 本地屏蔽词检查.
4. 如果本地屏蔽词检查命中, 直接拦截, 不发送提示词审核.
5. 如果本地屏蔽词检查通过, 尝试把提示词审核事件放入内存队列.
6. 后台 worker 从队列取事件并 POST 到外部审核服务.
7. 主模型请求继续正常中转, 不等待外部审核服务.

## 配置项

配置通过 `.env` 注入.

```env
# 是否启用异步提示词审核发送. 可选, 默认 false.
PROMPT_AUDIT_ENABLED=true

# 异步提示词审核接收地址. PROMPT_AUDIT_ENABLED=true 时必填, 为空或格式非法会启动失败, 服务不可用时运行中降级丢弃.
PROMPT_AUDIT_ENDPOINT_URL=http://127.0.0.1:8080/test/prompt-audit

# 异步提示词审核签名密钥. 可选, 默认空, 为空则不发送签名请求头.
PROMPT_AUDIT_SECRET=test-secret

# 异步提示词审核请求超时时间, 单位毫秒. 可选, 默认 3000.
PROMPT_AUDIT_TIMEOUT_MS=3000

# 异步提示词审核队列容量. 可选, 默认 3000.
PROMPT_AUDIT_QUEUE_SIZE=3000

# 异步提示词审核发送 worker 数量. 可选, 默认 8.
PROMPT_AUDIT_WORKER_COUNT=8

# 单次发送的提示词文本最大字节数. 可选, 默认 1048576, 约等于 1MB.
PROMPT_AUDIT_MAX_TEXT_BYTES=1048576
```

## 启动校验

`PROMPT_AUDIT_ENABLED=false` 时, 功能关闭, 不校验 endpoint.

`PROMPT_AUDIT_ENABLED=true` 时, 会校验 `PROMPT_AUDIT_ENDPOINT_URL`:

- 必须非空.
- URL 格式必须合法.
- scheme 必须是 `http` 或 `https`.
- host 必须非空.

如果校验失败, New API 启动失败.

启动时不会探测审核服务是否在线. 如果审核服务暂时不可用, New API 仍可正常启动.

## 队列和降级

提示词审核使用有界内存队列和固定数量 worker:

- 队列没满时, 新审核事件正常入队.
- 队列满时, 新审核事件直接丢弃, 主请求继续中转.
- 审核服务慢或不可用时, worker 会等待直到请求成功, 失败或超时.
- 审核服务慢本身不会直接丢弃事件, 但可能导致队列积压.
- 只有队列满了, 才会丢弃新的审核事件.
- 发送失败或非 2xx 响应只记录 warn 日志, 不重试, 不影响主请求.

当前默认值:

```text
worker: 8
queue: 3000
timeout: 3000ms
max text: 1MB
```

## 发送请求

发送方法:

```text
POST PROMPT_AUDIT_ENDPOINT_URL
Content-Type: application/json
```

固定请求头:

| 请求头 | 是否必传 | 说明 |
|---|---|---|
| `Content-Type` | 是 | 固定为 `application/json` |
| `X-NewAPI-Audit-Version` | 是 | 审核协议版本, 当前为 `prompt_audit.v1` |
| `X-NewAPI-Request-ID` | 是 | New API 请求 ID |
| `X-NewAPI-Audit-Event-ID` | 是 | 审核事件 ID, 当前使用 request id |

如果配置了 `PROMPT_AUDIT_SECRET`, 会额外发送:

| 请求头 | 是否必传 | 说明 |
|---|---|---|
| `X-NewAPI-Audit-Timestamp` | 配置签名密钥时必传 | Unix 秒级时间戳, 用于签名 |
| `X-NewAPI-Audit-Signature` | 配置签名密钥时必传 | HMAC-SHA256 签名, 格式为 `sha256=<hex_hmac_sha256>` |

签名内容:

```text
timestamp + "." + raw_json_body
```

签名算法:

```text
HMAC-SHA256(secret, signing_content)
```

## 响应要求

审核服务只需要返回 2xx HTTP 状态码即可, 不要求返回特定 JSON 格式.

New API 会读取并丢弃最多 1024 字节响应体, 不解析响应内容. 只要状态码在 `200 <= status < 300` 范围内, 就认为发送成功.

常用返回方式:

```text
HTTP 200 OK
```

或:

```text
HTTP 204 No Content
```

如果审核服务返回非 2xx 状态码, 或请求超时, 或网络错误, New API 只记录 warn 日志, 不重试, 不影响当前模型请求.

## 请求体协议

请求体示例:

```json
{
  "version": "prompt_audit.v1",
  "event_id": "20260622210132727140000OW90u6llkfXOscAX",
  "sent_at": "2026-06-22T21:01:32.72714Z",
  "source": "new-api",
  "request": {
    "request_id": "20260622210132727140000OW90u6llkfXOscAX",
    "path": "/v1/chat/completions",
    "relay_format": "openai",
    "relay_mode": 1,
    "model": "gpt-5.4-mini",
    "stream": false
  },
  "user": {
    "id": 1,
    "email": "user@example.com",
    "group": "default",
    "using_group": "default"
  },
  "token": {
    "id": 1,
    "group": "default"
  },
  "prompt": {
    "text": "system\n你是一个严谨的中文技术助手\nuser\n这是一次提示词审核异步推送测试",
    "text_bytes": 105,
    "truncated": false
  }
}
```

字段说明:

| 字段 | 示例 | 说明 |
|---|---|---|
| `version` | `prompt_audit.v1` | 协议版本, 当前为 `prompt_audit.v1` |
| `event_id` | `20260622210132727140000OW90u6llkfXOscAX` | 审核事件 ID, 当前使用 request id |
| `sent_at` | `2026-06-22T21:01:32.72714Z` | New API 生成事件的 UTC 时间 |
| `source` | `new-api` | 来源服务, 固定为 `new-api` |
| `request.request_id` | `20260622210132727140000OW90u6llkfXOscAX` | New API 请求 ID |
| `request.path` | `/v1/chat/completions` | 原始请求路径 |
| `request.relay_format` | `openai` | 中转协议格式 |
| `request.relay_mode` | `1` | 中转模式 |
| `request.model` | `gpt-5.4-mini` | 原始模型名称 |
| `request.stream` | `false` | 是否流式请求 |
| `user.id` | `1` | 用户 ID |
| `user.email` | `user@example.com` | 用户邮箱 |
| `user.group` | `default` | 用户分组 |
| `user.using_group` | `default` | 实际使用分组 |
| `token.id` | `1` | token ID |
| `token.group` | `default` | token 分组 |
| `prompt.text` | `system\n你是一个严谨的中文技术助手\nuser\n这是一次提示词审核异步推送测试` | 提取出的文本内容 |
| `prompt.text_bytes` | `105` | `prompt.text` 的 UTF-8 字节数 |
| `prompt.truncated` | `false` | 是否因超过 `PROMPT_AUDIT_MAX_TEXT_BYTES` 被截断 |

## 提示词提取范围

提示词审核发送的是 New API 已提取的文本, 不是原始 HTTP body.

OpenAI chat 类请求中, 通常会进入 `prompt.text` 的内容包括:

- message role.
- string content.
- 多模态 content 数组中的 `type=text` 文本.
- 部分 tools 的名称, 描述和参数.

不会进入 `prompt.text` 的内容包括:

- `image_url` 图片 URL 或 base64 图片内容.
- `input_audio` 音频内容.
- `file` 文件内容.
- `video_url` 视频内容.

这些多模态内容可能会用于 token 计费的文件元信息, 但不会发送到提示词审核的请求体中.

如果用户把 URL 或 base64 当普通文本写在 `content` 里, 它仍会作为文本进入 `prompt.text`.

## 日志

New API 侧日志行为:

- 启动成功时记录功能启用日志, endpoint 会脱敏.
- 队列满时记录 warn.
- 发送失败时记录 warn, 错误文本会脱敏.
- 成功发送不记录逐条日志, 避免高并发下日志量过大.

## 测试请求

可以用普通 chat completions 请求触发提示词审核:

```bash
curl -i 'http://127.0.0.1:3000/v1/chat/completions' \
  -H 'Authorization: Bearer sk-你的-new-api-token' \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "gpt-5.4-mini",
    "messages": [
      {
        "role": "system",
        "content": "你是一个严谨的中文技术助手, 回答要直接."
      },
      {
        "role": "user",
        "content": "我想测试多轮对话下提示词审核收到的文本."
      },
      {
        "role": "assistant",
        "content": "可以发送包含 system, user, assistant 历史消息的请求来观察."
      },
      {
        "role": "user",
        "content": "这是第二轮用户消息, 请总结前面对话."
      }
    ],
    "stream": false
  }'
```
