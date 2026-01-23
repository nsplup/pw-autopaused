*本项目与 README 由 `Gemini` 完成，`我` 监制*

# Pipewire Auto Pause Daemon

这是一个使用 Go 编写的轻量级守护进程，旨在提升 Linux 桌面用户的音频隐私体验。它通过监听 **PipeWire** 事件并利用 **MPRIS DBus** 接口来控制媒体播放。

## 已知问题

* 当触发事件为【设备路由变更】时无法通过静音输出设备彻底屏蔽正在输出的流

## 核心功能

* **智能切换识别**：自动识别音频输出从耳机/耳麦（Private）切换到扬声器/HDMI（Public）的行为。
* **自动暂停播放**：一旦触发切换，程序会通过 DBus 向所有支持 MPRIS 协议的播放器（如 Chrome, Spotify, VLC, MPV 等）发送 `Pause` 指令。
* **临时静音保护**：在发送暂停指令的同时，程序会短暂静音 PipeWire 节点，确保在播放器响应暂停请求前的瞬间不会有声音外放。
* **用户操作识别**：能够区分“耳机断开连接”触发的自动切换和“用户在设置中手动切换”的行为，避免干扰用户的正常操作。

## 工作原理

程序通过以下两个关键进程进行协同工作：

1. **`pw-dump --monitor`**：实时监听 PipeWire 的状态变化，包括节点（Node）、设备（Device）和元数据（Metadata）的更新。
2. **`pw-cli`**：用于在必要时向 PipeWire 发送控制指令（如设置静音参数）。
3. **DBus (MPRIS)**：检测系统中运行的媒体播放器并控制其播放状态。

### 设备分类逻辑

程序通过检查 PipeWire 路由的 `port.type` 来分类设备：

* **私有设备 (Private)**：关键字包含 `headphones`, `headset`。
* **公共设备 (Public)**：关键字包含 `speaker`, `hdmi`, `displayport`。

---

## 安装与运行

### 依赖要求

* **PipeWire**: 确保系统正在使用 PipeWire 作为音频服务器。
* **命令行工具**: 需安装 `pipewire-bin` (包含 `pw-dump` 和 `pw-cli`)。
* **Go 环境**: 编译代码需要 Go 1.18 或更高版本。

### 编译

```bash
go mod tidy
make // or go build -o pw-pauser .

```

### 运行

可以直接运行编译后的二进制文件：

```bash
./pw-pauser

```

日志会实时输出当前的设备切换状态及暂停动作。

---

## 注意事项

* **用户手动切换**：如果用户通过系统设置手动更改默认输出设备，程序会识别为 `IsUserOperation` 并跳过自动暂停逻辑，以保证用户体验的连贯性。
* **并发安全**：代码内部使用了 `sync.RWMutex` 来确保全局节点和设备映射表在多线程环境下的数据安全。
