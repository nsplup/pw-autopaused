package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
	"go.uber.org/zap"
)

var (
	IsUserOperation    bool
	currentDefaultSink string
	pwCliStdin io.WriteCloser

	nodesMu         sync.RWMutex
	devsMu          sync.RWMutex
	stdinMu    sync.Mutex

	GlobalNodes   = make(map[int]Node)
	GlobalDevices = make(map[int]Device)

	publicDevice  = []string{"speaker", "hdmi", "displayport"}
	privateDevice = []string{"headphones", "headset"}
)

type PwObject struct {
	ID   int    `json:"id"`
	Type string `json:"type"`
}

type Node struct {
	ID   int `json:"id"`
	Info struct {
		Props struct {
			NodeName   string `json:"node.name"`
			DeviceID   int    `json:"device.id"`
			MediaClass string `json:"media.class"`
		} `json:"props"`
	} `json:"info"`
}

type Device struct {
	ID   int `json:"id"`
	Info struct {
		Props struct {
			DeviceName  string `json:"device.name"`
			DeviceAlias string `json:"device.alias"`
		} `json:"props"`
		Params struct {
			Route   []RouteInfo   `json:"Route"`
			Profile []interface{} `json:"Profile"`
		} `json:"params"`
	} `json:"info"`
}

type RouteInfo struct {
	Index     int           `json:"index"`
	Name      string        `json:"name"`
	Direction string        `json:"direction"`
	Priority  int           `json:"priority"`
	Info      []interface{} `json:"info"`
}

type RouteData struct {
	Properties map[string]string
}

type MetadataEntry struct {
	Subject int         `json:"subject"`
	Key     string      `json:"key"`
	Type    string      `json:"type"`
	Value   interface{} `json:"value"`
}

type MetadataUpdate struct {
	ID       int             `json:"id"`
	Metadata []MetadataEntry `json:"metadata"`
}

func GetDeviceIDByNodeName(nodeName string) (int, bool) {
	nodeID, ok := GetNodeIDByName(nodeName)
	if !ok {
		// zap.L().Warn("无法找到节点索引", zap.String("name", nodeName))
		return 0, false
	}

	nodesMu.RLock()
	node, exists := GlobalNodes[nodeID]
	nodesMu.RUnlock()

	if !exists {
		// zap.L().Warn("无法找到节点", zap.Int("id", nodeID))
		return 0, false
	}
	return node.Info.Props.DeviceID, true
}

func GetHighestPriorityOutputRoute(dev Device) (RouteInfo, bool) {
	var bestRoute RouteInfo
	found := false

	for _, r := range dev.Info.Params.Route {
		if strings.EqualFold(r.Direction, "output") {
			if !found || r.Priority > bestRoute.Priority {
				bestRoute = r
				found = true
			}
		}
	}
	return bestRoute, found
}

func checkDeviceCategory(dev Device, keywords []string) bool {
	topRoute, ok := GetHighestPriorityOutputRoute(dev)
	if !ok {
		return false
	}

	info := topRoute.Info
	if len(info) < 3 {
		return false
	}

	for i := 1; i+1 < len(info); i += 2 {
		key, kOk := info[i].(string)
		if kOk && key == "port.type" {
			val, vOk := info[i+1].(string)
			if vOk {
				portType := strings.ToLower(val)
				for _, kw := range keywords {
					if strings.Contains(portType, kw) {
						return true
					}
				}
			}
			break
		}
	}
	return false
}

func IsPublicDevice(dev Device) bool {
	return checkDeviceCategory(dev, publicDevice)
}

func IsPrivateDevice(dev Device) bool {
	return checkDeviceCategory(dev, privateDevice)
}

func setPipewireMute(nodeID int, mute bool) {
	if pwCliStdin == nil {
		return
	}

	volume := "[1.0, 1.0]"
	if mute {
		volume = "[0.0, 0.0]"
	}

	cmd := fmt.Sprintf("set-param %d Props { channelVolumes: %s }\n", nodeID, volume)

	stdinMu.Lock()
	defer stdinMu.Unlock()
	_, err := io.WriteString(pwCliStdin, cmd)
	if err != nil {
		zap.L().Error("向控制进程发送指令失败", zap.Error(err))
	}
}

func pauseAllPlayers(ctx context.Context) {
	conn, err := dbus.SessionBus()
	if err != nil {
		zap.L().Error("无法连接会话总线", zap.Error(err))
		return
	}

	var names []string
	err = conn.BusObject().Call("org.freedesktop.DBus.ListNames", 0).Store(&names)
	if err != nil {
		zap.L().Error("获取名单列表失败", zap.Error(err))
		conn.Close()
		return
	}

	var wg sync.WaitGroup

	for _, name := range names {
		if strings.HasPrefix(name, "org.mpris.MediaPlayer2.") {
			wg.Add(1)
			go func(playerName string) {
				defer wg.Done()

				call := conn.Object(playerName, "/org/mpris/MediaPlayer2").Call("org.mpris.MediaPlayer2.Player.Pause", 0)

				if call.Err != nil {
					zap.L().Debug("暂停播放器失败", zap.String("player", playerName), zap.Error(call.Err))
				}
			}(name)
		}
	}

	go func() {
		wg.Wait()
		conn.Close() 
	}()
}

func pauseWithMute(nodeID int) {
	go setPipewireMute(nodeID, true)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()

		pauseAllPlayers(ctx)

		select {
		case <-time.After(1000 * time.Millisecond):
		case <-ctx.Done():
			zap.L().Warn("暂停播放器时超时")
			return
		}

		setPipewireMute(nodeID, false)
	}()
}

func handleDefaultRouteChange(newDev Device) {
	currentDevID, ok := GetDeviceIDByNodeName(currentDefaultSink)
	if !ok {
		return
	}

	if newDev.ID != currentDevID {
		return
	}

	devsMu.RLock()
	oldDev, exists := GlobalDevices[newDev.ID]
	devsMu.RUnlock()

	if !exists {
		return
	}

	nodeID, nOk := GetNodeIDByName(currentDefaultSink)
	if !nOk {
		return
	}
	if IsPrivateDevice(oldDev) && IsPublicDevice(newDev) {
		// FIXME: 无法通过静音输出设备彻底屏蔽正在输出的流
		zap.L().Info("暂停播放器，触发事件为【设备路由变更】")
		pauseWithMute(nodeID)
	}
}

func onDeviceUpdate(data []byte) {
	var dev Device
	if err := json.Unmarshal(data, &dev); err == nil {
		handleDefaultRouteChange(dev)

		devsMu.Lock()
		GlobalDevices[dev.ID] = dev
		devsMu.Unlock()
	}
}

func GetNodeIDByName(nodeName string) (int, bool) {
	nodesMu.RLock()
	defer nodesMu.RUnlock()
	for id, node := range GlobalNodes {
		if node.Info.Props.NodeName == nodeName {
			return id, true
		}
	}
	return 0, false
}

func onNodeUpdate(data []byte) {
	var node Node
	if err := json.Unmarshal(data, &node); err == nil {
		nodesMu.Lock()
		GlobalNodes[node.ID] = node
		nodesMu.Unlock()
	}
}

func handleDefaultSinkChange(metadata []MetadataEntry) {
	for _, entry := range metadata {
		if entry.Key != "default.audio.sink" && entry.Key != "default.configured.audio.sink" {
			continue
		}

		var nodeName string
		switch v := entry.Value.(type) {
		case map[string]interface{}:
			nodeName, _ = v["name"].(string)
		case string:
			var subMap map[string]interface{}
			if err := json.Unmarshal([]byte(v), &subMap); err == nil {
				nodeName, _ = subMap["name"].(string)
			} else {
				nodeName = strings.Trim(v, "\"")
			}
		}

		if nodeName == "" {
			continue
		}

		switch entry.Key {
		case "default.audio.sink":
			oldDevID, oldOk := GetDeviceIDByNodeName(currentDefaultSink)
			newDevID, newOk := GetDeviceIDByNodeName(nodeName)
			nodeID, nOk := GetNodeIDByName(nodeName)

			if oldOk && newOk && nOk {
				devsMu.RLock()
				oldDev := GlobalDevices[oldDevID]
				newDev := GlobalDevices[newDevID]
				devsMu.RUnlock()

				if !IsUserOperation && IsPrivateDevice(oldDev) && IsPublicDevice(newDev) {
					zap.L().Info("暂停播放器，触发事件为【输出设备变更】")
					pauseWithMute(nodeID)
				}
			}

			if currentDefaultSink == "" {
				zap.L().Info("默认输出设备初始化为", zap.String("sink", nodeName))
			}
			currentDefaultSink = nodeName
			IsUserOperation = false
		case "default.configured.audio.sink":
			IsUserOperation = true
		}
	}
}

func onMetadataUpdate(data []byte) {
	var meta MetadataUpdate
	if err := json.Unmarshal(data, &meta); err == nil {
		handleDefaultSinkChange(meta.Metadata)
	}
}

func dispatcher(rawObjects []json.RawMessage) {
	for _, raw := range rawObjects {
		var base PwObject
		if err := json.Unmarshal(raw, &base); err != nil {
			continue
		}
		switch base.Type {
		case "PipeWire:Interface:Node":
			onNodeUpdate(raw)
		case "PipeWire:Interface:Metadata":
			onMetadataUpdate(raw)
		case "PipeWire:Interface:Device":
			onDeviceUpdate(raw)
		}
	}
}

func main() {
	cfg := zap.NewDevelopmentConfig()
	cfg.EncoderConfig.TimeKey = ""
	cfg.EncoderConfig.CallerKey = ""
	logger, _ := cfg.Build()
	zap.ReplaceGlobals(logger)
	defer logger.Sync()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	zap.L().Info("正在启动控制进程...")

	cliCmd := exec.CommandContext(ctx, "pw-cli")
	var err error
	pwCliStdin, err = cliCmd.StdinPipe()
	if err != nil {
		zap.L().Fatal("无法创建控制进程输入管道", zap.Error(err))
	}
	if err := cliCmd.Start(); err != nil {
		zap.L().Fatal("无法启动控制进程", zap.Error(err))
	}

	go func() {
		err := cliCmd.Wait()
		zap.L().Warn("控制进程已退出", zap.Error(err))
		cancel()
	}()

	zap.L().Info("正在启动监听进程...")

	dumpCmd := exec.CommandContext(ctx, "pw-dump", "--monitor", "--no-colors")
	stdout, err := dumpCmd.StdoutPipe()
	if err != nil {
		zap.L().Fatal("无法创建监听进程输出管道", zap.Error(err))
	}
	if err := dumpCmd.Start(); err != nil {
		zap.L().Fatal("无法启动监听进程", zap.Error(err))
	}

	go func() {
		zap.L().Info("正在监听事件...")
		decoder := json.NewDecoder(stdout)
		for {
			var rawObjects []json.RawMessage
			if err := decoder.Decode(&rawObjects); err != nil {
				if err == io.EOF {
					break
				}
				zap.L().Warn("从监听进程解析事件发生错误", zap.Error(err))
				break
			}
			dispatcher(rawObjects)
		}
		cancel()
	}()

	go func() {
		err := dumpCmd.Wait()
		zap.L().Warn("监听进程已退出", zap.Error(err))
		cancel()
	}()

	<-ctx.Done()
	
	zap.L().Info("子进程意外退出，正在退出主进程...")
	time.Sleep(500 * time.Millisecond)
	os.Exit(1)
}