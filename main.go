package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

var (
	IsUserOperation bool
	nodesMu         sync.RWMutex
	devsMu          sync.RWMutex

	currentDefaultSink string

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
			return 0, false
    }
    
    nodesMu.RLock()
    node, exists := GlobalNodes[nodeID]
    nodesMu.RUnlock()
    
    if !exists {
			return 0, false
    }
    return node.Info.Props.DeviceID, true
}

func ParseRouteProperties(infoArray []interface{}) RouteData {
	data := RouteData{
		Properties: make(map[string]string),
	}
	if len(infoArray) < 3 {
		return data
	}
	for i := 1; i+1 < len(infoArray); i += 2 {
		key, kOk := infoArray[i].(string)
		val, vOk := infoArray[i+1].(string)
		if kOk && vOk {
			data.Properties[key] = val
		}
	}
	return data
}

func GetHighestPriorityOutputRoute(dev Device) (RouteInfo, bool) {
	var outputRoutes []RouteInfo
	for _, r := range dev.Info.Params.Route {
		if strings.EqualFold(r.Direction, "output") {
			outputRoutes = append(outputRoutes, r)
		}
	}
	if len(outputRoutes) == 0 {
		return RouteInfo{}, false
	}
	sort.Slice(outputRoutes, func(i, j int) bool {
		return outputRoutes[i].Priority > outputRoutes[j].Priority
	})
	return outputRoutes[0], true
}


func checkDeviceCategory(dev Device, keywords []string) bool {
	topRoute, ok := GetHighestPriorityOutputRoute(dev)
	if !ok {
		return false
	}

	parsed := ParseRouteProperties(topRoute.Info)
	if portType, exists := parsed.Properties["port.type"]; exists {
		portType = strings.ToLower(portType)
		for _, kw := range keywords {
			if strings.Contains(portType, kw) {
				return true
			}
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

func pauseAllPlayers(ctx context.Context) {
	conn, err := dbus.SessionBus()
	if err != nil {
		fmt.Printf("[ERROR] 无法连接 DBus 会话总线: %v", err)
		return
	}
	defer conn.Close()

	var names []string
	
	err = conn.BusObject().Call("org.freedesktop.DBus.ListNames", 0).Store(&names)
	if err != nil {
		fmt.Printf("[ERROR] 获取名单列表失败：%v", err)
		return
	}

	for _, name := range names {
		if strings.HasPrefix(name, "org.mpris.MediaPlayer2.") {
			select {
			case <-ctx.Done():
				return
			default:
			}

			done := make(chan error, 1)
			go func(playerName string) {
				call := conn.Object(playerName, "/org/mpris/MediaPlayer2").Call("org.mpris.MediaPlayer2.Player.Pause", 0)
				done <- call.Err
			}(name)

			select {
			case <-done:
			case <-time.After(200 * time.Millisecond):
				fmt.Printf("[WARN] 暂停播放器 %s 时超时", name)
			case <-ctx.Done():
				return
			}
		}
	}
}

func setPipewireMute(nodeID string, mute bool) {
	volume := "1.0"
	if mute {
		volume = "0.0"
	}
	param := fmt.Sprintf(`{ "volume": %s }`, volume)
	cmd := exec.Command("pw-cli", "set-param", nodeID, "Props", param)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	
	cmd.Start()
	
	go cmd.Wait()
}

func pauseWithMute(nodeID string) {
	setPipewireMute(nodeID, true)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()

		pauseAllPlayers(ctx)

		select {
		case <-time.After(500 * time.Millisecond):
		case <-ctx.Done():
			fmt.Println("[WARN] 操作超时")
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
		fmt.Printf("[Event] 检测到路由自动切换：从私有状态(Headphones) 切换到了 公共状态(Speaker)！\n")
		nodeIDStr := fmt.Sprintf("%d", nodeID)
		pauseWithMute(nodeIDStr)
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
			if currentDefaultSink == "" {
				currentDefaultSink = nodeName
				fmt.Printf("[Init] 初始默认 Sink 设置为: %s\n", nodeName)
				continue
			}

			oldDevID, oldOk := GetDeviceIDByNodeName(currentDefaultSink)
			newDevID, newOk := GetDeviceIDByNodeName(nodeName)
			nodeID, nOk := GetNodeIDByName(nodeName)

			if oldOk && newOk && nOk {
				devsMu.RLock()
				oldDev := GlobalDevices[oldDevID]
				newDev := GlobalDevices[newDevID]
				devsMu.RUnlock()
				
				if !IsUserOperation && IsPrivateDevice(oldDev) && IsPublicDevice(newDev) {
					fmt.Printf("[Event] 检测到 Sink 切换：从私有状态 切换到了 公共状态！\n")
					nodeIDStr := fmt.Sprintf("%d", nodeID)
					pauseWithMute(nodeIDStr)
				}
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
	cmd := exec.Command("pw-dump", "--monitor")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		panic(err)
	}
	if err := cmd.Start(); err != nil {
		panic(err)
	}

	fmt.Println("正在监听 PipeWire 设备切换事件...")

	decoder := json.NewDecoder(stdout)
	for {
		var rawObjects []json.RawMessage
		if err := decoder.Decode(&rawObjects); err != nil {
			if err == io.EOF {
				break
			}
			continue
		}
		dispatcher(rawObjects)
	}
	cmd.Wait()
}