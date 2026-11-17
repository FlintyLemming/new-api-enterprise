# 新用户默认订阅设计

日期：2026-11-17
状态：已确认（用户在对话中确认整体设计；下文标注「实现者自定」的部分由实现者决定）

## 1. 背景与目标

8850 实例只开放 SSO（OIDC）登录，新账号在首次登录时自动创建，但不会获得任何订阅。此前靠一个外部脚本在账号创建后几秒内调用管理接口发放「5小时150M套餐」（plan 2），用户已停用该脚本，不想再单独维护脚本。

目标：管理员在「订阅管理」页设置一个"新用户默认订阅"套餐，此后**所有新创建的账号**（OIDC/OAuth 自动建号、密码注册、微信登录建号、管理员后台新建）都自动获得该套餐。

## 2. 已确认的需求决策

| 决策点 | 结论 |
|---|---|
| 入口 | 订阅管理页「新建套餐」左侧加「设置」按钮，弹窗中选择套餐 |
| 生效范围 | 所有建号路径；已有用户不补发 |
| 关闭方式 | 选"不发放"，存为 0 |
| 套餐启用状态 | 不检查。目标 plan 2 当前为停用（只是不在商店展示），管理员手动发订阅同样不检查启用状态；下拉框列出停用套餐并标注"已停用" |
| 权限 | 管理员（与订阅管理其他接口一致），不走仅超级管理员可写的 `/api/option` |
| 套餐不存在时 | 跳过发放、记服务端日志，不阻断注册 |

## 3. 方案比较

| 方案 | 说明 | 取舍 |
|---|---|---|
| A. 建号事务内发放（采用） | 在 `User.Insert` / `User.InsertWithTx` 的 `tx.Create(user)` 之后，同一事务内调用 `CreateUserSubscriptionFromPlanTx` | 用户与订阅原子：要么都建成要么都不建；所有建号路径只改一处 |
| B. 建号提交后发放 | 在 `finishInsert` / `FinalizeOAuthUserCreation` 里另起事务发放 | 失败不影响注册，但进程在两步之间退出会留下无订阅账号，且每条路径的收尾函数不同，需改多处 |
| C. 恢复外部脚本 / 定时补发 | 定时扫描无订阅的新用户并发放 | 用户明确不想维护独立脚本 |

## 4. 设计

### 4.1 设置项

- 新增 `setting/operation_setting/subscription_setting.go`，注册到 `config.GlobalConfig`，前缀 `subscription_setting`，字段 `new_user_plan_id`（int，0 = 关闭）。option 键即 `subscription_setting.new_user_plan_id`，随现有 option 同步机制在多节点间生效。
- 新接口（`/api/subscription/admin` 组，`AdminAuth`）：
  - `GET /settings` → `{ new_user_plan_id }`，访问令牌规则 `billing:read`。
  - `PUT /settings`，body `{ new_user_plan_id }`，访问令牌规则 `billing:write`。要求已确认合规条款（`requirePaymentCompliance`，与新建套餐一致）；`new_user_plan_id < 0` 拒绝；`> 0` 时校验套餐存在；通过 `model.UpdateOption` 写入；记管理审计（新旧套餐 ID）。

### 4.2 发放

- 在 `User.Insert` 与 `User.InsertWithTx` 中，`tx.Create(user)` 成功后调用同一个发放方法（领域概念：新用户默认订阅）：
  - 设置为 0 或合规条款未确认 → 不发放。
  - `getSubscriptionPlanByIdTx` 找不到套餐 → 不发放，`SysError` 记一行服务端日志。查询无结果不会使 PostgreSQL 事务进入失败状态。
  - 其余情况调用 `CreateUserSubscriptionFromPlanTx(tx, user.Id, plan, "new_user")`；任何错误向上返回，整个建号事务回滚。
  - 套餐带 `UpgradeGroup` 时，`CreateUserSubscriptionFromPlanTx` 会在事务内改 `users.group`；同步更新内存中的 `user.Group`，避免后续逻辑用到旧分组。
- 订阅来源 `source = "new_user"`，在用户订阅列表中与 `admin`、`order` 区分。

### 4.3 前端

- `SubscriptionsPrimaryButtons`：「新建套餐」左侧加 outline 样式「设置」按钮（齿轮图标），合规条款未确认时与「新建套餐」一样禁用。
- 新增设置弹窗（复用 `@/components/dialog` 等项目封装，实现前按 `web/AGENTS.md` 检索）：一个选择框（"不发放" + 全部套餐，停用套餐标注"已停用"），说明文字"新创建的账号（含 SSO 自动创建）会自动获得该套餐；已有用户不受影响"，取消 / 保存。
- 新增 API 函数与 React Query 查询 / 变更；七种语言补翻译。

### 4.4 安全（OWASP）

- 授权全部在服务端：`AdminAuth` + 访问令牌 scope 规则；前端禁用状态只是提示。
- 设置变更写管理审计，不含任何凭据。
- 风险：若以后开放密码注册，每个新账号都会获得订阅，存在批量注册薅额度的可能；当前实例只开放 SSO。

### 4.5 测试

- 后端：一个测试文件覆盖 设置开启 → 新用户获得订阅（来源 `new_user`、额度与重置周期取自套餐）、设置为 0 → 不发放、套餐不存在 → 用户照常创建且无订阅；设置接口的参数校验。在真实 SQLite、MySQL、PostgreSQL 上运行。
- 前端：设置弹窗交互测试（加载当前值、选择并保存调用接口），放在 `__tests__/`。
- typecheck、lint（涉及文件）、构建、`i18n:sync`。

## 5. 非目标

- 不给已有用户补发（588 号等由管理员手动处理）。
- 不按用户组、邮箱域等条件区分发放。
- 不改变管理员手动发订阅、购买订阅的现有行为。

## 6. 留给实现者

- 设置弹窗的具体组件组合、测试文件命名、后端测试放在哪个现有测试文件（遵循"小改动最多新增一个测试文件"）。
- 合入 `internal-custom` 后补 `docs/internal/patches/` 详情与台账条目（单独 `docs(internal)` 提交）。
