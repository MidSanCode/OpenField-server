# P2P 语音 / 视频通话

OpenField 的通话是**点对点（P2P）**的：媒体流在两端之间直接传输，服务端只负责两件事——

1. **状态机**：谁在打给谁、现在响铃还是已接通、为什么结束；
2. **信令中继**：把 SDP / ICE 这些建连协商数据从一端原样搬给另一端。

服务端不解码、不转码、不转发媒体，因此**没有媒体带宽成本**，也不需要部署 SFU/MCU。代价是：没有 TURN 服务器时，部分网络环境（对称 NAT、企业防火墙）无法建连——TURN 的部署见第 9 节。

> 当前范围：**一对一（`type = private`）会话内的通话**。群组通话需要 mesh 或 SFU，尚未实现，接口会明确拒绝（见 5.1）。

---

## 1. 为什么信令走 REST 而不是 WebSocket

推送通道（`GET /api/v1/ws`）是**纯下行**的：`services/push/internal/handler/ws.go` 的 `readPump` 只做 `conn.ReadMessage()` 用来探测断连，**读到的帧被直接丢弃**，没有上行分发逻辑。客户端无法把 ICE candidate 写上去。

所以信令的形态是：

```
客户端 A ──POST /api/v1/calls/:id/signal──► chat 服务 ──NOTIFY openfield_events──► push 服务
                                                                                    │
客户端 B ◄──────────────────── WebSocket: call.signal ◄─────────────────────────────┘
```

即**上行走 REST，下行复用现有推送**。这与仓库里 `chat.typing`、`chat.consent.requested` 的模式完全一致，没有引入新的传输层。

---

## 2. 数据模型

迁移版本 **v33**（`pkg/database/migration.go`，`name: "call-sessions"`）。

### call_sessions

| 列 | 说明 |
|---|---|
| `id` | 通话 ID |
| `conversation_id` | 所属会话，`ON DELETE CASCADE` |
| `initiator_id` | 发起人，`ON DELETE CASCADE` |
| `mode` | `audio` \| `video` |
| `state` | `ringing` \| `active` \| `ended` |
| `end_reason` | `hung_up` \| `cancelled` \| `declined` \| `timeout` |
| `created_at` / `answered_at` / `ended_at` | 时间戳 |
| `duration_seconds` | **不落库**，查询时由 `answered_at`/`ended_at` 推导 |

索引 `idx_calls_live`：

```sql
CREATE UNIQUE INDEX idx_calls_live
    ON call_sessions(conversation_id) WHERE state <> 'ended';
```

这是**并发保证**：同一会话同时只允许一个未结束的通话。第二次发起会命中唯一冲突 → 返回 `409`，而不是让对端同时响两个铃。已结束的通话不参与该索引，所以历史记录可以无限累积。

索引 `idx_calls_conversation` 服务"这个会话的通话历史"查询。

### call_participants

| 列 | 说明 |
|---|---|
| `call_id` | 外键 `call_sessions`，`ON DELETE CASCADE` |
| `user_id` | 外键 `users`，`ON DELETE CASCADE` |
| `state` | `invited` \| `accepted` \| `declined` \| `left` |
| `joined_at` / `left_at` | 时间戳 |

主键 `(call_id, user_id)`。发起人在创建时即写为 `accepted`（他本来就在通话里），被叫方初始为 `invited`。

会话被删除或用户被删除时，通话与其成员一并级联删除——与"主群删了子群也没了"的既有约定一致，通话不留下悬挂记录。

---

## 3. 状态机

```
                 answer
   ringing ──────────────────► active ──── hangup ────► ended
      │                          │
      ├── decline ───────────────┤
      ├── cancel（振铃时挂断）────┤
      └── timeout（45s 未接）─────┘
```

- `ended` 是终态，**不会回到 ringing**；再打是新的 `id`。
- `answer` 用条件更新 `WHERE state = 'ringing'` 实现，天然解决**多端同时接听**的竞态：抢输的那一端拿到 `409` 和当前通话状态，据此停止响铃。

---

## 4. 权限

新增权限键 **`chat.call`**（`pkg/permission/permission.go`）。

- 属于普通功能键，**自动下发给默认组**（`DefaultGroupPermissions()` = 全部键 − 管理键），因此注册用户默认可通话。
- 运营方可以按组撤权来整体关停通话，而不影响收发消息。
- 网关层统一校验：`authPermission` + `chat.call`，chat 服务内部不再重复判断权限（与其他 handler 一致），但仍校验会话成员身份。

---

## 5. API

全部挂在 chat 服务下，经网关 `/api/v1` 暴露，鉴权级别 `authPermission` + `chat.call`（即必须携带有效 access token 或 bot token，且所属组拥有该权限）。

### 5.1 发起通话

```
POST /api/v1/calls
```

```json
{ "conversation_id": 123, "mode": "audio" }
```

`mode` 可省略，默认 `audio`。

| 状态码 | 含义 |
|---|---|
| `201` | 已发起，返回 `{"call": {...}}`，同时向对端推送 `call.invited` |
| `400` | 参数错误；或会话不是 `private`（群组通话未实现）；或解析不出对端 |
| `403` | 不是该会话成员；或发起方是 bot 账号（bot 没有媒体端点，见 10） |
| `404` | 会话不存在 |
| `409` | 该会话已有进行中的通话 |

### 5.2 查询通话

```
GET /api/v1/calls/:id
```

返回 `{"call": {...}}`，含 `participants` 名单与实时推导的 `duration_seconds`。

**没有事件重放**：客户端连上会话、重连、或怀疑漏收事件时，都应主动拉一次这个接口对齐状态。

非参与者返回 `403`。

### 5.3 接听

```
POST /api/v1/calls/:id/answer
```

成功返回 `200` 与最新通话，并向**全部参与者**推送 `call.accepted`（让被叫的其他设备停止响铃）。

| 状态码 | 含义 |
|---|---|
| `400` | 发起人试图接听自己的去电 |
| `403` | 不是参与者 |
| `409` | 已不再响铃（已被其它端接听 / 已拒接 / 已超时），响应体带当前 `call` |

### 5.4 拒接

```
POST /api/v1/calls/:id/decline
```

一对一场景下拒接即**结束整通电话**（`end_reason = declined`），并推送 `call.declined` 给发起人、`call.ended` 给全体。

### 5.5 挂断

```
POST /api/v1/calls/:id/hangup
```

- 振铃阶段挂断 → `end_reason = cancelled`（主叫放弃）；
- 已接通后挂断 → `end_reason = hung_up`。

`End()` 是条件更新（`WHERE state <> 'ended'`），所以 `call.ended` 事件**只会推送一次**。

### 5.6 信令中继

```
POST /api/v1/calls/:id/signal
```

```json
{ "type": "offer", "payload": { "...": "SDP 或 candidate，原样透传" } }
```

- `type` 只能是 `offer` / `answer` / `candidate`；`payload` 是**不透明**数据，服务端不解析、不校验结构。
- 上限 **6000 字节**（`repository.MaxSignalBytes`），超出返回 `413`。原因是 `pg_notify` 单条上限 8000 字节，事件信封还要占约 200 字节；与其让超长载荷在 `NOTIFY` 里静默丢失，不如在 API 层明确拒绝。音频 SDP 约 1 KB、单个 candidate 几百字节，正常流量远低于该上限。
- **不落库**：candidate 交换频率高、通话结束后即无价值。
- 转发给**其他参与者**，事件为 `call.signal`。无对端可投递时返回 `409`；通话已结束返回 `409`。

---

## 6. 推送事件

事件常量在 `pkg/events/events.go`，经 `NOTIFY openfield_events` → push 服务下发到该用户的**所有** WebSocket 连接。

| 事件 | 接收者 | 载荷 |
|---|---|---|
| `call.invited` | 被叫方 | `call_id`, `conversation_id`, `initiator_id`, `mode`, `created_at` |
| `call.accepted` | 全体参与者 | `call_id`, `user_id` |
| `call.declined` | 发起人 | `call_id`, `user_id` |
| `call.ended` | 全体参与者 | `call_id`, `conversation_id`, `end_reason`, `duration_seconds` |
| `call.signal` | 其他参与者 | `call_id`, `from_user_id`, `type`, `payload` |

**已知限制（多端）**：`Hub` 按 `user_id` 索引连接（`hub.go` 的 `byUser`），`Client` 不带设备标识，所以 `call.signal` 会投递到被叫**所有**设备。客户端必须按 `call_id` 过滤，并且只在真正接听的那台设备上处理协商数据。若要精确投递，需要给 WS 票据增加 `session_id` 并在 `Hub` 中增加按会话投递的能力——属于后续优化，不在本阶段。

---

## 7. 超时与清理

- 振铃超时 **45 秒**（`repository.RingTimeout`），与客户端保持一致。
- chat 服务启动一个 **2 秒** 周期的 sweeper（`CallHandler.StartSweeper`），把超时的 `ringing` 通话置为 `ended`（`end_reason = timeout`）并推送 `call.ended`。
- 没有这个 sweeper，主叫杀进程后对端会**一直响铃**，同时 `idx_calls_live` 会让该会话**再也打不通电话**。

---

## 8. 客户端接入要点（尚未实现）

服务端已就绪，客户端需要：

1. **依赖** `flutter_webrtc`（当前 `pubspec.yaml` 没有 WebRTC 相关依赖）；
2. 收到 `call.invited` → 展示来电界面 → `POST /calls/:id/answer`；
3. 主叫在 `POST /calls` 成功后创建 `RTCPeerConnection`，生成 offer → `POST /calls/:id/signal`（`type=offer`）；
4. 被叫收到 `call.signal`（`type=offer`）→ `setRemoteDescription` → 生成 answer → 回传 `type=answer`；
5. 双方持续把 `onIceCandidate` 收集到的 candidate 用 `type=candidate` 发出去；
6. 需要 **STUN**（`stun:stun.l.google.com:19302` 之类）以及部署 **TURN**；
7. 通话结束/失败 → `POST /calls/:id/hangup`；
8. 进入会话时 `GET` 一次活跃通话，避免漏事件导致状态错位。

平台注意事项（6 个构建目标）：

| 平台 | 注意点 |
|---|---|
| Android | 相机/麦克风运行时权限；后台通话需前台服务与通知渠道 |
| iOS | `Info.plist` 用途描述 + 后台 `audio` 模式；锁屏来电需要 CallKit/PushKit（工作量大，可后置） |
| Web | 必须 HTTPS（secure context），麦克风需用户手势触发；依赖网关 CORS 配置 |
| Windows / macOS | 打包 WebRTC 运行库，安装包体积增加；macOS 需 entitlements |
| Linux | `flutter_webrtc` 的平台支持最弱，建议先做一次构建验证，必要时首版标记为实验性 |

---

## 9. TURN（生产环境必需）

只在同一局域网或宽松 NAT 下，STUN 才够用。对称 NAT、企业防火墙、部分移动网络下必须靠 TURN 中继，否则表现为**一直"连接中"最后失败**。

- 组件：coturn，开放 `3478/udp`、`3478/tcp`、`5349/tls` 与中继端口段；
- 鉴权：**不要**使用静态用户名密码，用 `use-auth-secret` + 服务端按 `user_id` 和过期时间签发短期凭证；
- 容量：只有中继的流量计费，经验比例 10%～20%；
- 媒体加密：P2P 下由 **DTLS-SRTP** 保证端到端加密，媒体不经过服务器。但**信令与元数据**（谁打给谁、时间、时长）对服务端可见，文档与产品文案不应声称"完全不可见"。

---

## 10. 已知限制

- **群组通话未实现**：`type = group` 的会话直接返回 `400`。要支持需要 mesh（两两建连，上行带宽随人数线性增长）或 SFU（新增有状态媒体服务），是另一个量级的工作。
- **bot 不能发起通话**：bot 没有媒体端点，发起的通话只会让对方无意义响铃，因此 `POST /calls` 对 bot 返回 `403`。反之，用户给 **bot** 打电话会正常响铃然后 45 秒后超时——目前不额外拦截，如需更友好的提示可加一条"对端是 bot"的预检。
- **多端精确投递**：见第 6 节，`call.signal` 会投递到被叫所有设备，需要客户端按 `call_id` 过滤。
- **无事件重放**：推送是尽力而为，客户端必须靠 `GET /calls/:id` 兜底对齐状态。
- **无通话记录消息**：目前通话结束后不会在会话里插入"通话 2 分 13 秒"的横幅。若要，可复用 `messages` 的系统消息（参考 `system.join`），需要客户端支持新的 `kind`。

## 11. 排障

| 现象 | 排查方向 |
|---|---|
| 发起返回 `409` | 该会话已有未结束通话；查 `call_sessions WHERE conversation_id=$1 AND state<>'ended'`，必要时手动 `End` 或等 sweeper |
| 对端收不到 `call.invited` | 对端是否在线（WS 已连接）；`events` 表/NOTIFY 通道是否正常；共享同一 PostgreSQL |
| `call.signal` 返回 `413` | 载荷超过 6000 字节，检查是否把整段带 simulcast 的 SDP 塞进去 |
| 一直"连接中" | 通常是 NAT 穿透失败：确认 STUN/TURN 可达，检查 candidate 是否成对 |
| 通话结束后会话仍打不通 | 确认 `state` 已是 `ended`；`idx_calls_live` 只排除已结束行 |
