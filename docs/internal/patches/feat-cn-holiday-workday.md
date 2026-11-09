# feat/cn-holiday-workday

| 项 | 值 |
| -- | -- |
| 分支 | 直接提交到 `internal-custom` |
| 基线 | `internal-custom` @ `d067ca33c` |
| 合入 commit | 见 README 生效中的 patch 表 |
| 合入日期 | 2026-11-09 |
| 状态 | `internal-only`（未提交） |
| 上游 PR | 未提交 |

## 为什么要这个改动

计费表达式原本只能用 `hour()` / `weekday()` 等函数按时段计价，分不出中国大陆的法定节假日和调休上班日，峰谷计价会把春节等假期按工作日收高峰价，把调休的周六按周末收低谷价。

## 改动内容

- `pkg/cnholiday`：解析并校验小米节假日 API（`https://api.comm.miui.com/holiday/holiday.jsp`）的日历，每年给出 `freeday`（放假日）与 `workday`（调休上班日）两组"当年第几天"。判断规则：放假日为非工作日，调休日为工作日，其余按周一至周五；日历没有覆盖的年份也按周一至周五。拒绝空日历、越界日期、同一天既放假又上班、重复年份，避免坏数据覆盖好数据。
- 系统任务 `holiday_calendar_sync`：主节点每 6 小时拉取一次，规范化后与 `HolidayCalendar` option 比较，有变化才写库；其他节点经常规 option 同步加载。拉取失败保留已存日历，失败记录在系统信息页的任务历史中。环境变量 `HOLIDAY_CALENDAR_URL` 覆盖数据源，`HOLIDAY_CALENDAR_SYNC_ENABLED=false` 关闭任务。
- 计费表达式新增 `is_workday(tz) → bool`，与 `hour()` 一样属于请求探针，用它写的倍率规则会记进消费日志的 `request_rules`。预扣和结算各按当时日期求值一次。
- 公开接口 `GET /api/holiday_calendar` 返回已存日历（ETag 缓存），前端用同一份数据判断定价页当前生效档位；日历未加载完时列出全部档位而不猜测。
- 可视化定价分支编辑器新增"工作日状态"条件（工作日 / 非工作日）；条件文案显示为"工作日 09:00至18:00（Asia/Shanghai）"。请求倍率规则编辑器未加该选项，原始表达式模式可用。
- 通用 `GET /api/option` 不返回 `HolidayCalendar`。

示例：

```
is_workday("Asia/Shanghai") && hour("Asia/Shanghai") >= 9 && hour("Asia/Shanghai") < 18
  ? tier("peak", p * 3 + c * 15)
  : tier("off_peak", p * 1.5 + c * 7.5)
```

测试：`pkg/cnholiday/calendar_test.go`（2026 年真实数据：1 月 4 日周日调休、2 月 14 日周六调休、2 月 16 日春节等）、`pkg/billingexpr/billingexpr_test.go` 的 `TestIsWorkday*`、前端 `billing-expression.test.ts` 的 "mainland China workday pricing" 与 `visual-billing-editor.test.tsx` 的工作日条件编辑用例。

## 部署影响

无表结构变化。新增 option key `HolidayCalendar`（约 2.8 KB）与 `system_tasks` 新类型 `holiday_calendar_sync`。容器需能访问 `api.comm.miui.com`（8850 已确认可达）。只有表达式里用了 `is_workday` 的模型计费才会变化。
