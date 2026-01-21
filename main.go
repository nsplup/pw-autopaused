package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strings"
	"sync"
)

// --- 全局变量与状态存储 ---

var (
	IsUserOperation bool
	nodesMu         sync.RWMutex
	devsMu          sync.RWMutex

	GlobalNodes   = make(map[int]Node)
	GlobalDevices = make(map[int]Device)

	// 全局分类关键词
	publicDevice  = []string{"speaker", "hdmi", "displayport"}
	privateDevice = []string{"headphones", "headset"}
)

// --- 类型声明 ---

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

// --- 逻辑实现 ---

// ParseRouteProperties 解析 Route 中的 info 数组
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

// GetHighestPriorityOutputRoute 抽离的通用函数：获取优先级最高的输出路由
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

// getCategoryByPortType 根据 port.type 字符串判断分类
func getCategoryByPortType(portType string) string {
	portType = strings.ToLower(portType)
	for _, kw := range privateDevice {
		if strings.Contains(portType, kw) {
			return "【私有设备】"
		}
	}
	for _, kw := range publicDevice {
		if strings.Contains(portType, kw) {
			return "【公共设备】"
		}
	}
	return "未知类型"
}

// checkDeviceCategory 适配重构后的逻辑
func checkDeviceCategory(deviceID int, keywords []string) bool {
	devsMu.RLock()
	dev, ok := GlobalDevices[deviceID]
	devsMu.RUnlock()
	if !ok {
		return false
	}

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

func IsPublicDevice(deviceID int) bool {
	return checkDeviceCategory(deviceID, publicDevice)
}

func IsPrivateDevice(deviceID int) bool {
	return checkDeviceCategory(deviceID, privateDevice)
}

// --- 处理逻辑 ---

func handleDefaultRouteChange(newDev Device) {
	devsMu.RLock()
	oldDev, exists := GlobalDevices[newDev.ID]
	devsMu.RUnlock()

	if !exists {
		return
	}

	oldRoute, oldOk := GetHighestPriorityOutputRoute(oldDev)
	newRoute, newOk := GetHighestPriorityOutputRoute(newDev)

	// 如果新旧状态至少有一个没有输出路由，则不处理对比
	if !oldOk || !newOk {
		return
	}

	oldProps := ParseRouteProperties(oldRoute.Info)
	newProps := ParseRouteProperties(newRoute.Info)

	oldPortType := oldProps.Properties["port.type"]
	newPortType := newProps.Properties["port.type"]

	// 只有当 port.type 发生变化时才触发
	if oldPortType != newPortType && newPortType != "" {
		oldCat := getCategoryByPortType(oldPortType)
		newCat := getCategoryByPortType(newPortType)

		fmt.Printf("[Event] 设备内部路由变化 (Device ID: %d):\n", newDev.ID)
		fmt.Printf(" -> 从: %s (%s)\n", oldPortType, oldCat)
		fmt.Printf(" -> 到: %s (%s)\n", newPortType, newCat)
	}
}

func onDeviceUpdate(data []byte) {
	var dev Device
	if err := json.Unmarshal(data, &dev); err == nil {
		// 1. 在更新全局缓存前，对比新旧路由
		handleDefaultRouteChange(dev)

		// 2. 更新全局缓存
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
		if entry.Key == "default.configured.audio.sink" {
			IsUserOperation = true
			var nodeName string
			if valMap, ok := entry.Value.(map[string]interface{}); ok {
				if name, ok := valMap["name"].(string); ok {
					nodeName = name
				}
			} else {
				nodeName = fmt.Sprintf("%v", entry.Value)
			}

			if nodeName == "" {
				continue
			}

			if id, ok := GetNodeIDByName(nodeName); ok {
				nodesMu.RLock()
				node := GlobalNodes[id]
				nodesMu.RUnlock()
				
				devID := node.Info.Props.DeviceID
				fmt.Printf("[Event] 用户切换 Sink 为: %s (Device ID: %d)\n", nodeName, devID)
				
				if IsPrivateDevice(devID) {
					fmt.Println(" -> 识别结果: 【私有设备】 (Headphones/Headset)")
				} else if IsPublicDevice(devID) {
					fmt.Println(" -> 识别结果: 【公共设备】 (Speaker/HDMI/DP)")
				} else {
					fmt.Println(" -> 识别结果: 未知类型设备")
				}
			}
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