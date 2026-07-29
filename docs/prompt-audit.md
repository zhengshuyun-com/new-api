# 提示词审核

提示词审核把中转请求通过 `Request.GetTokenCountMeta()` 提取出的 `CombineText` 发送到外部审核服务.

该功能支持关闭, 异步送审和同步拦截. 同步模式采用 fail-open 策略, 只有审核服务返回明确拒绝结果时才阻断主请求.

## 执行位置

通用中转请求的处理顺序如下:

1. 解析并校验请求, 生成 `RelayInfo`.
2. 提取 `TokenCountMeta`, 执行本地屏蔽词检查.
3. 估算 token, 计算价格并完成余额预扣. 免费模型跳过预扣.
4. 执行提示词审核.
5. 同步审核明确拒绝时返回 HTTP 403 和 `prompt_blocked`, 已预扣额度会退款.
6. 审核通过或降级放行后选择动态渠道并请求上游.

固定渠道可能已由中间件提前选定. 动态渠道在提示词审核之后选定, 因此审核事件不包含最终渠道信息. 异步审核事件入队后, 即使后续选渠或上游请求失败, 已入队事件仍会继续发送.

## 配置

```env
# < 0: 关闭审核.
# = 0: 异步送审.
# > 0: 同步送审, 数值为主请求等待审核结果的最大毫秒数.
PROMPT_AUDIT_WAIT_MS=0

# PROMPT_AUDIT_WAIT_MS >= 0 时必填.
PROMPT_AUDIT_ENDPOINT_URL=http://127.0.0.1:8080/test/prompt-audit

# 可选. 非空时对请求体生成 HMAC-SHA256 签名.
PROMPT_AUDIT_SECRET=test-secret

# 仅异步模式使用.
PROMPT_AUDIT_QUEUE_SIZE=128
PROMPT_AUDIT_WORKER_COUNT=8
```

| 配置项 | 默认值 | 说明 |
|---|---:|---|
| `PROMPT_AUDIT_WAIT_MS` | `-1` | 审核模式和等待时间, 单位毫秒. `< 0` 关闭, `0` 异步, `> 0` 同步 |
| `PROMPT_AUDIT_ENDPOINT_URL` | 空 | 审核服务地址. 启用时必填, 支持 `http` 和 `https` |
| `PROMPT_AUDIT_SECRET` | 空 | HMAC-SHA256 签名密钥. 为空时不签名 |
| `PROMPT_AUDIT_QUEUE_SIZE` | `128` | 异步队列容量. `<= 0` 时恢复默认值 |
| `PROMPT_AUDIT_WORKER_COUNT` | `8` | 异步发送 worker 数量. `<= 0` 时恢复默认值 |

整数配置无法解析时会记录错误日志并使用默认值. `PROMPT_AUDIT_WAIT_MS` 无法解析时回退为 `-1`, 即关闭审核.

异步 HTTP 请求超时固定为 `3000ms`.

审核文本不设置独立长度限制, `prompt.text` 完整发送. 来源请求仍受全局 `MAX_REQUEST_BODY_MB` 限制, 默认 `128MB`.

## 启动校验

`PROMPT_AUDIT_WAIT_MS < 0` 时不校验 endpoint.

`PROMPT_AUDIT_WAIT_MS >= 0` 时, `PROMPT_AUDIT_ENDPOINT_URL` 必须满足以下条件:

- 非空.
- URL 可解析.
- scheme 为 `http` 或 `https`.
- host 非空.

校验失败时服务启动失败. 启动过程不会探测审核服务是否在线.

## 运行模式

### 异步模式

`PROMPT_AUDIT_WAIT_MS = 0` 时:

- 主请求完成入队后立即继续, 不等待审核结果.
- 队列满时丢弃新事件并记录 warn 日志.
- 每次发送的 HTTP 超时固定为 `3000ms`.
- HTTP `2xx` 视为发送成功, 不解析业务响应.
- 非 `2xx`, 超时, 网络错误或读取响应失败时记录 warn 日志.
- 不重试, 不影响主请求.

### 同步模式

`PROMPT_AUDIT_WAIT_MS > 0` 时:

- 主请求最多等待配置的毫秒数.
- 有效审核响应中, 仅当 `data` 不为空且 `data.action=REJECT` 时拒绝主请求.
- `data` 为空或 `data.action` 不等于 `REJECT` 时直接放行.
- 非 `HTTP 200`, 响应读取失败, 非法 JSON, `code` 非 `SUCCESS`, 超时或网络错误时降级放行.
- 审核请求不重试.

审核响应体最多读取 `4096` 字节. 同步响应应控制在该限制内, 否则 JSON 可能因截断而被判定为无效并降级放行.

## HTTP 请求协议

```text
POST PROMPT_AUDIT_ENDPOINT_URL
Content-Type: application/json
```

固定请求头:

| 请求头 | 值 |
|---|---|
| `Content-Type` | `application/json` |
| `X-NewAPI-Audit-Version` | `v1` |
| `X-NewAPI-Request-ID` | New API 请求 ID |
| `X-NewAPI-Audit-Event-ID` | 当前与请求 ID 相同 |

配置 `PROMPT_AUDIT_SECRET` 后额外发送:

| 请求头 | 值 |
|---|---|
| `X-NewAPI-Audit-Timestamp` | Unix 秒级时间戳 |
| `X-NewAPI-Audit-Signature` | `sha256=<hex_hmac_sha256>` |

签名输入是原始 JSON 请求体, 不得在验签前重新序列化:

```text
signing_content = timestamp + "." + raw_json_body
signature = HMAC-SHA256(secret, signing_content)
```

## 请求体

```json
{
  "version": "v1",
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
    "text_bytes": 97
  }
}
```

字段定义:

| 字段 | 说明 |
|---|---|
| `version` | 固定为 `v1` |
| `event_id` | 当前使用 New API 请求 ID |
| `sent_at` | UTC 时间, RFC3339Nano 格式 |
| `source` | 固定为 `new-api` |
| `request.request_id` | New API 请求 ID |
| `request.path` | 原始请求路径, 不含 query |
| `request.relay_format` | 中转协议格式 |
| `request.relay_mode` | 内部中转模式整数值 |
| `request.model` | 客户端请求的原始模型名称 |
| `request.stream` | 是否为流式请求 |
| `user.id` | 用户 ID |
| `user.email` | 用户邮箱, 空值时省略 |
| `user.group` | 用户分组, 空值时省略 |
| `user.using_group` | 实际计费分组, 空值时省略 |
| `token.id` | Token ID |
| `token.group` | Token 分组, 空值时省略 |
| `prompt.text` | 从模型请求中提取并完整发送的审核文本 |
| `prompt.text_bytes` | `prompt.text` 的 UTF-8 字节数 |

`prompt.text` 为空或仅包含空白字符时不发送审核请求.

## 响应判定

异步和同步模式使用不同的响应规则:

| 模式 | HTTP 状态码 | 响应体 |
|---|---|---|
| 异步 | `2xx` 表示发送成功, 非 `2xx` 表示发送失败 | 不解析业务内容 |
| 同步 | 只有 `HTTP 200` 才继续判断业务结果, 非 `HTTP 200` 直接降级放行 | 有效响应中仅当 `data` 不为空且 `data.action=REJECT` 时拒绝 |

因此, "只判断 HTTP 状态码" 仅适用于异步模式. 同步模式会先判断 HTTP 状态码, 再判断响应体.

有效审核响应要求 HTTP 状态码为 `200`, JSON 可以解析且 `code=SUCCESS`. 同步拦截规则可以概括为: 仅当 `data` 不为空且 `data.action=REJECT` 时拒绝, 其他情况全部放行.

### 同步响应协议

允许请求:

```json
{
  "code": "SUCCESS",
  "data": {
    "action": "ALLOW"
  }
}
```

拒绝请求:

```json
{
  "code": "SUCCESS",
  "data": {
    "action": "REJECT"
  }
}
```

判定规则区分大小写:

| HTTP 和响应体 | 结果 |
|---|---|
| `HTTP 200`, `code=SUCCESS`, `data != null`, `data.action=REJECT` | 拒绝 |
| `HTTP 200`, `code=SUCCESS`, `data == null` 或 `data.action != REJECT` | 放行 |
| `HTTP 200`, code 非 `SUCCESS` | 降级放行 |
| 非 `HTTP 200` | 降级放行 |
| 空响应体或非法 JSON | 降级放行 |
| 超时, 网络错误或读取失败 | 降级放行 |

响应体可以包含 `msg`, `timestamp`, `reason` 等额外字段, New API 不读取这些字段.

## 文本提取范围

审核服务收到的是 `TokenCountMeta.CombineText`, 不是原始 HTTP body. 只有文本进入 `prompt.text`, `TokenCountMeta.Files` 中的文件元信息不会发送.

| 请求类型 | 进入 `prompt.text` 的内容 |
|---|---|
| OpenAI Chat 和 Completions | `prompt`, `input`, message role, message name, 文本 content, tool 名称, 描述和参数 |
| OpenAI Responses | 文本 input, instructions, metadata, text, tool choice, prompt 和 tools |
| OpenAI Responses Compaction | instructions 和 input |
| Claude Messages | system 文本, message role 和文本, tool use, tool result, tool 定义 |
| Gemini Chat | `contents[].parts[].text` |
| OpenAI 和 Gemini Embedding | embedding input 文本 |
| Rerank | documents 和 query |
| Image | prompt |
| Audio | input |
| Alpha Search | 原始 JSON 请求体 |

OpenAI, Claude 和 Gemini 多模态请求中的图片, 音频, 文件和视频数据不会进入 `prompt.text`. 如果 URL 或 base64 作为普通文本字段传入, 它仍会进入审核文本.

## 日志

- 启动成功时记录模式和脱敏后的 endpoint.
- 队列满, 发送失败, 同步响应无效和明确拒绝时记录 warn.
- 成功发送不记录逐条日志.

## 联调

使用普通 Chat Completions 请求触发审核:

```bash
curl -i 'http://127.0.0.1:3000/v1/chat/completions' \
  -H 'Authorization: Bearer sk-your-token' \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "gpt-5.4-mini",
    "messages": [
      {"role": "user", "content": "prompt audit integration test"}
    ],
    "stream": false
  }'
```

同步拒绝场景应返回 HTTP 403, 错误码为 `prompt_blocked`. 异步模式下审核服务的响应内容不会改变主请求结果.
