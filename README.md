*本项目 GO 由 `Gemini` 完成，`我` 监制*

*本项目 CGO 由 `ChatGLM` 完成，`我` 监制*

*本项目 README 由 `ChatGLM` 完成，`我` 监制*

# PipeWire 音频自动暂停助手
这是一个基于 Go 语言编写的后台程序，用于监听 PipeWire 音频设备切换事件。该工具旨在保护用户隐私，当检测到音频输出从“私人设备”（如耳机、耳麦）自动切换到“公共设备”（如扬声器、HDMI、DisplayPort）时，它会自动暂停所有正在播放的媒体播放器。
## 功能特性
*   **智能设备识别**：根据 PipeWire 设备的端口类型（Port Type）自动区分“私人设备”（Headphones/Headset）和“公共设备”（Speaker/HDMI/DisplayPort）。
*   **自动暂停播放**：当发生从私人到公共的音频路由切换时，自动通过 DBus 发送暂停指令给所有支持 MPRIS 协议的媒体播放器（如 Spotify, VLC, mpv, Firefox 视频等）。
*   **平滑静音处理**：在暂停过程中，会自动对音频节点进行静音/取消静音操作，防止切换瞬间产生爆音。
	-   **【已知问题】由设备路由更新触发的暂停无法解决爆音问题**
*   **双重检测机制**：同时监听设备路由更新和默认 Sink 变化，确保在各种切换场景下都能准确触发。
## 环境要求
*   **操作系统**：Linux
*   **音频系统**：PipeWire
*   **工具依赖**：
    *   `pw-dump` (PipeWire 监控工具，用于获取事件)
*   **运行时**：DBus 会话总线
*   **开发语言**：Go 1.16+
## 使用方法
### 1. 直接运行
直接运行编译后的二进制文件即可开始监听：
```bash
./pipewire-auto-pause
```
### 2. 后台运行 (可选)
如果希望程序在后台持续运行：
```bash
nohup ./pipewire-auto-pause > /dev/null 2>&1 &
```
或者在 systemd 中创建一个服务文件进行管理。
## 配置说明
目前程序主要通过内置的设备类别列表进行判断，无需额外配置文件：
*   **公共设备**：包含 "speaker", "hdmi", "displayport" 关键字。
*   **私人设备**：包含 "headphones", "headset" 关键字。
如果需要修改判断逻辑，可以直接编辑源码中的 `publicDevice` 和 `privateDevice` 变量。
## 工作原理
1.  **监听事件**：通过 `pw-dump --monitor` 实时订阅 PipeWire 的事件流。
2.  **解析数据**：解析 JSON 格式的数据，主要关注 `PipeWire:Interface:Device` 和 `PipeWire:Interface:Metadata` 类型的消息。
3.  **状态判断**：
    *   检查当前默认 Sink 的变化。
    *   获取设备 ID 对应的物理设备，并解析其 Route（路由）信息，找出优先级最高的输出端口。
    *   根据端口的 `port.type` 属性判断设备类别。
4.  **执行动作**：
    *   如果检测到从 `Private` -> `Public` 的切换，且并非用户手动操作：
        *   静音当前节点。
        *   通过 DBus 遍历所有 `org.mpris.MediaPlayer2.*` 服务，调用 `Pause` 方法。
        *   等待片刻后恢复音量。
## 注意事项
*   程序需要访问 Session Bus 以控制媒体播放器。
*   需要用户具备执行 `pw-dump` 的权限。
*   确保你的媒体播放器支持 MPRIS2 D-Bus 接口。