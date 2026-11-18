# ADR 0001：客户端技术栈 —— Wails（桌面）+ Flutter（移动）

- 状态：已接受
- 日期：2026-11-18
- 决策者：项目所有者
- 影响范围：客户端选型、WS 鉴权方式、客户端仓库边界

## 背景

rym 服务端已经具备注册登录、单聊消息、拉取/补洞、未读/已读，并准备补用户发现与 WebSocket 实时推送。
客户端需要同时覆盖桌面和移动，但两者的人群、交互和发布节奏不同。

约束：

- 单人项目，Go 熟手，TypeScript 有基础，Flutter 新手；
- 小项目，**稳定性要求不高**，优先迭代速度和追新特性；
- 服务端要保持 proto 契约干净，客户端能直接生成类型；
- 桌面端需要托盘、通知、本地缓存、重连、自动更新等能力。

## 决策

1. **桌面客户端使用 Wails v3，并且始终使用 latest。**
   - 不锁小版本；接受破坏性变更，出现后当轮修复。
   - 使用 `wails3` CLI 与当前 latest 的 `github.com/wailsapp/wails/v3`。
   - 当前基线：v3.1.0（2026-11-01），仅作记录，不作为锁定版本。

2. **移动客户端使用 Flutter 最新 stable。**
   - 跟随 stable channel；移动端发布不使用 beta/master。
   - 需要尝鲜新特性时可以临时切 beta，但发布前回到 stable。
   - Flutter 版本记录在客户端仓库，不在服务端仓库锁定。

3. **客户端独立仓库。**
   - 建议命名：`rym-desktop`（Wails）与 `rym-mobile`（Flutter）。
   - 本仓库（`rym`）只保留服务端、proto 契约、迁移与文档。
   - 客户端通过 `api/**/*.proto` 生成 TypeScript / Dart 类型，不手写协议。

4. **服务端不为某个客户端做专用接口。**
   - HTTP 路径、响应信封、WS 事件格式保持客户端无关。
   - 客户端差异只在“怎么连”（Authorization 头 vs ticket），不在业务接口。

5. **WS 鉴权双模。**
   - Wails 的 Go 后端可以设置 `Authorization: Bearer`，HTTP 与 WS 都直接带 JWT；
   - Flutter 移动端（`web_socket_channel` 的 IO 实现）也支持自定义 header；
   - Flutter Web / 浏览器不能设置 WS header，使用 30 秒单次 ticket（`?ticket=...`）；
   - 服务端必须同时支持 header 与 ticket，ticket 端点保留。

6. **不用 Wails 的移动端。**
   - Wails v3 的 iOS/Android 仍是 experimental，不作为移动方案；
   - 移动端只走 Flutter。

## 理由

- **构建速度**：Wails 的 Go 编译循环远快于 Tauri 的 Rust release/LTO，直接解决当前痛点；
- **语言匹配**：桌面客户端的 WS、重连、本地缓存、outbox、通知、托盘都可以用 Go 写，正好是 IM 客户端最难的部分；
- **前端复用**：Wails 使用 WebView + 任意 Web 前端，React/TS 可以继续用；
- **移动端成熟度**：Flutter 的移动端生态和工具链比 Wails experimental 稳得多；
- **小项目取舍**：稳定性让位于迭代速度，Wails latest 的破坏性变更可以快速修，能持续吃到新特性。

## 后果

### 正面

- 桌面端开发/调试循环以秒计；
- Go 客户端核心逻辑可测试、可复用；
- 桌面与移动各自使用最合适的 UI 技术；
- 服务端契约保持单一来源。

### 负面 / 需要接受

- **两套 UI**（React + Flutter），不共享界面代码；只共享协议与设计；
- Wails latest 可能引入破坏性变更，升级后需要当轮修复；
- Wails v3 较新（2026-10 才 stable），生态与资料少于 Tauri，遇到问题需要读源码；
- Wails Linux 依赖 GTK4 + WebKitGTK 6.0（v3 默认），分发给旧 Linux 需要测试；
- Wails 的移动端不可用，移动端必须维护 Flutter 工程；
- 桌面端与移动端的发布、签名、商店流程分别维护。

### 对服务端的要求

- WS 握手同时支持 `Authorization` 头与 `ticket` query；
- 事件协议带版本号（`v`），客户端可以忽略未知类型；
- 契约变更必须保持向后兼容，或明确记录破坏性变更；
- 不把 Wails/Flutter 的依赖带进服务端 `go.mod`。

## 备选方案与不选原因

| 方案 | 不选原因 |
|---|---|
| Tauri v2 桌面 | Rust 编译慢；当前瓶颈正是构建时间；Go 复用度低 |
| Electron | 体积/内存大，不符合轻量目标 |
| Wails 同时做桌面 + 移动 | 移动端 experimental，不满足移动交付 |
| 一套代码跨桌面 + 移动（Tauri 2 mobile / Flutter desktop） | 桌面/移动都想要各自最优；接受两套 UI |
| 纯 Web / PWA | 缺少托盘、系统通知、本地缓存、原生窗口等桌面能力；可作为前端调试形态 |

## 版本与升级策略

- **Wails**：`go get -u github.com/wailsapp/wails/v3@latest` + latest `wails3` CLI；不写死版本；升级后跑客户端冒烟（登录、WS、发消息、托盘、通知、打包）。
- **Flutter**：`flutter upgrade` 跟随 stable；客户端仓库记录当前版本；发布前固定 stable 版本号。
- **服务端**：Go 1.27、kratos v3；不因客户端升级而改服务端依赖。
- 客户端 CI 使用 latest 构建；失败时优先修客户端，不回退服务端契约。

## 跟进事项

1. WS 服务端实现双模鉴权（Authorization + ticket）；
2. 生成 Wails 前端所需的 TypeScript 类型与 Flutter 所需的 Dart 类型；
3. 创建 `rym-desktop` 与 `rym-mobile` 仓库骨架；
4. 桌面端优先：登录 → 会话列表 → 聊天 → WS 推送 → 托盘/通知；
5. 移动端随后：复用同一套 HTTP/WS 契约，Flutter 侧实现重连与补洞。

## 参考

- Wails v3 发布：<https://wails.io/blog/wails-v3-released>
- Wails v3 状态与移动端实验性：<https://wails.io/status>
- Wails 构建速度对比（单点实测）：<https://blog.crstian.me/en/post/2026-11-10-wails-vs-tauri-linux>
- Tauri v2 移动端：<https://v2.tauri.app/blog/tauri-20>
