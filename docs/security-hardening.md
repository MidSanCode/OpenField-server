# 安全加固（2026-08）

本次安全审计后落地的修复清单。按严重程度排列。

## 严重

### 1. 删除遗留单体死代码
`cmd/` 与 `internal/` 是微服务化之前的旧单体实现，其中 `internal/handler/auth.go`
的刷新令牌由时间戳生成（可预测 → 可接管任意账号），且 OIDC 回调把 access_token
拼进重定向 URL。新服务已修复，但旧代码仍可被误构建运行，已整体删除。

### 2. 启动时拒绝弱 JWT 密钥
- `pkg/config.Load()` 新增 `validateSecrets()`：密钥为空、长度 < 32 或命中已知默认值
  （"test"、"changeme"、示例占位符等）时拒绝启动。
- 逃生口：仅限一次性本地调试，`OPENFIELD_ALLOW_WEAK_SECRETS=true`。
- `config/config.local.yaml` 的弱密钥 "test" 已轮换为随机强密钥；
  `config.example.yaml` 的占位符改为空并注明生成方式。

## 高

### 3. 登录 / 支付 PIN 暴力破解防护（新增 `pkg/ratelimit`）
进程内滑动窗口 + 锁定：
- 登录：每账号 15 分钟内最多 10 次失败、每 IP 最多 100 次，超限 429（带 Retry-After）。
  客户端真实 IP 取自网关追加的 X-Forwarded-For 最后一跳，伪造头无效。
- 支付 PIN：每用户 15 分钟内最多 5 次失败；`/users/me/pin/verify` 与转账共用同一
  失败预算，锁定同时覆盖两条路径。

### 4. admin 面板加固
- Flask SECRET_KEY 不再有 "test" 默认值：优先 `ADMIN_SECRET_KEY` 环境变量，
  否则首次运行生成随机密钥持久化到 `.secret_key`（已加入 .gitignore）。
- 全站 CSRF 防护：所有 POST 表单注入 `csrf_token` 隐藏域，`before_request`
  校验 token + Origin/Referer 同源双重检查，跨站请求直接 400。
- Session cookie 增加 SameSite=Lax，可选 `ADMIN_COOKIE_SECURE=true`。
- 登录限流：IP+用户名 15 分钟内 5 次失败即锁定；登录成功轮换 session id。

### 5. 客户端敏感数据迁移到加密存储（新增 `SecureKV`）
- access/refresh token、过期时间、按主机保存的多会话、E2EE 身份私钥与群组密钥
  全部从 SharedPreferences（明文）迁移到 flutter_secure_storage
  （Keystore/Keychain/Credential Manager/libsecret）。
- 启动时一次性迁移旧明文数据并从 SharedPreferences 删除。
- Android 关闭 allowBackup 并新增 data_extraction_rules，
  备份/设备迁移不再携带会话数据。

## 中

### 6. WebSocket 一次性票据（移除 ?token=）
JWT 出现在 URL 会泄入代理与访问日志。现在客户端先 `POST /api/v1/ws`（Bearer 认证）
换取 30 秒单次有效的随机 ticket，再以 `?ticket=` 连接；ticket 由 push 服务内存保管、
用过即焚。网关为 WS 升级引入 authTicket 级别：不带凭据放行至 push 校验，
并删除客户端伪造的 X-User-ID。

### 7. 分片上传会话归属与限额（schema v14：upload_sessions）
- ChunkInit 在数据库登记会话（归属用户、桶、分片数、大小）。
- 上传/查询/完成均校验会话存在且属于当前用户（他人会话返回 404）。
- index 超出 total_chunks、total_chunks > 10000 均拒绝；完成时分片数必须一致。
- storage 服务每小时清理超过 24h 未完成的会话。

### 8. 上传 MIME 白名单（新增 `pkg/storage/mime.go`）
直传与分片完成统一校验 Content-Type：HTML/SVG/XHTML/JS 等可执行文档类型被拒
（415），公共存储桶不再可能被当作脚本宿主。

### 9. OIDC 登录 state 单次化（防登录 CSRF，schema v14：oidc_states）
登录不再使用固定 state "openfield-state"：每次 `/auth/oidc/login` 生成 256-bit
随机 nonce 入库，回调时原子校验并消费（不可重放）。绑定流程原有的 purpose token
不变。账户服务每小时清理过期 state 与 refresh token。

### 10. 帖子实时推送按可见性收敛
PostCreated 事件此前广播给所有在线连接，私密/好友可见帖子的内容会实时泄露给全部
用户。现在：public/login → 全体广播；friends → 仅互关者+作者
（新增 `FollowRepository.MutualFollowerIDs`）；private → 仅作者；出错时收窄不放宽。

### 11. refresh token 哈希入库
refresh_tokens 表只存 SHA-256(token)，数据库泄露不再等于批量会话劫持。
校验兼容旧明文行（平滑过渡），轮换时以哈希写入。

### 12. 群密钥轮换必须覆盖全体持钥成员（E2EE）
`PUT /conversations/:id/e2ee-keys` 原先只校验"信封目标是本会话活跃成员"，
版本号由任何成员递增，因此任一成员都能发一次只覆盖部分成员的新版本，把其余人
锁在新群密钥之外——线上看与一次正常轮换没有区别。现在要求本次版本覆盖所有
"已发布公钥"的活跃成员；没有公钥的成员不参与该要求（尚无人能加密给他，否则
轮换永远无法完成）。未覆盖时返回 400 并要求客户端刷新成员列表后重做完整轮换。
**未认证公钥本身（主动作恶的服务端替换公钥）不在本次范围内**，见
`SECURITY-AUDIT-OpenField.md` M-5 的威胁模型分歧。

### 13. 红包只能经由承载它的对象访问
`GET /checks/:id` 与 `POST /checks/:id/claim` 原先只按 `checks.id` 取行，id 自增
可枚举：挂在 friends/private 帖子或私聊里的红包可被局外人领走（钱从作者托管余额
支出），领取名单也可被任意登录用户读取。现在先解析承载对象——帖子走帖子可见性、
消息走会话成员关系、未挂载的仍按 id（此时 id 是唯一句柄）——不满足一律 404，
且 404 与"不存在"不可区分，避免存在性预言机。产品语义见 `docs/checks.md`。

### 14. 登录锁定不再按不存在的用户名计数 + 限流表可回收
`pkg/ratelimit` 此前对任意提交的用户名 `Fail`，10 次错误口令即可定向锁死任意
真实账号（包括以他人名义构造的大量假名，同时把限流表撑大——未达阈值的条目永不
回收）。现在账户桶只在用户确实存在后计数，未知用户名只记源 IP 桶；`Limiter`
在条目数超过阈值时惰性清扫：已服完锁定期或无有效失败记录的键会被删除。

### 15. 网关限流改为拒绝而非排队
`services/gateway` 的每 IP 令牌桶原先用 `Wait()` 阻塞等待令牌，超额请求挂起
handler（以及连接与文件描述符）。现在改为 `Allow()`，超额直接 `429` +
`Retry-After`，与登录/PIN/红包等处的语义一致。

### 16. 群待办的删除授权
`DeleteTodo` 的注释写着"创建者或管理员"，实现却只要求普通成员并把调用者 id
丢弃（`_ = userID`），任一成员可删他人的待办。现在归属校验进入 `DELETE` 语句
（`AND creator_id = $3`），只有管理员/群主可绕过；删不到的行与不存在统一返回 404。

## 低
- 刷新令牌字符抽样改为拒绝采样，消除模偏差。
- 仓库根目录误入的 14MB `cmd.exe`（未经签名、SHA-256 与系统同名程序不同，
  虽被 `.gitignore` 排除、未入库）已在本轮删除；同名系统程序出现在工作目录是
  命令解析劫持的经典条件。
- 示例配置 CORS 注释改为推荐显式 origin 列表。

## 兼容性说明
- 升级需要跑一次迁移（v14，各服务启动自动执行）。
- 旧版客户端仍用 `?token=` 连接 WS 将收到 401，需同步更新客户端。
- 旧版明文 refresh token 在轮换前仍然有效；轮换后自动转为哈希形态。
