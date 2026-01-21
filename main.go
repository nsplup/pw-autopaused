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

	currentDefaultSink string

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

func GetDeviceIDByNodeName(nodeName string) (int, bool) {
    nodeID, ok := GetNodeIDByName(nodeName)
    if !ok {
				// fmt.Printf("[Debug] 未找到名为 %s confirmed 的 Node\n", nodeName)
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


func checkDeviceCategory(dev Device, keywords []string) bool {
	// 1. 获取该设备中优先级最高的输出路由
	topRoute, ok := GetHighestPriorityOutputRoute(dev)
	if !ok {
		return false
	}

	// 2. 解析路由属性
	parsed := ParseRouteProperties(topRoute.Info)
	// fmt.Printf("[Debug] Device %d Route Props: %v\n", dev.ID, parsed.Properties)
	// 3. 匹配关键词
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

// --- 处理逻辑 ---

func handleDefaultRouteChange(newDev Device) {
	// 1. 获取当前默认 Sink 对应的 Device ID
	currentDevID, ok := GetDeviceIDByNodeName(currentDefaultSink)
	if !ok {
		return
	}

	// 2. 只有当发生变化的设备正是当前的默认输出设备时，才继续逻辑
	if newDev.ID != currentDevID {
		return
	}

	// 3. 获取旧的设备信息（从缓存中）
	devsMu.RLock()
	oldDev, exists := GlobalDevices[newDev.ID]
	devsMu.RUnlock()

	if !exists {
		return
	}

	// fmt.Printf("%v\n", IsPrivateDevice(oldDev))
	// fmt.Printf("%v\n", IsPublicDevice(newDev))

	// 4. 调用转换判断逻辑
	if IsPrivateDevice(oldDev) && IsPublicDevice(newDev) {
		fmt.Printf("[Logic] 检测到路由自动切换：从私有状态(Headphones) 切换到了 公共状态(Speaker)！\n")
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
        // 1. 只处理我们关心的 Key，避免其他 Metadata 干扰 nodeName 的解析
        if entry.Key != "default.audio.sink" && entry.Key != "default.configured.audio.sink" {
            continue
        }

        // 2. 解析 nodeName
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

        // 3. 处理逻辑
        switch entry.Key {
        case "default.audio.sink":
            fmt.Printf("[Debug] 检测到 Sink 变化，目标节点: %s (当前记录为: %s)\n", nodeName, currentDefaultSink)
            
            // 如果是第一次运行（currentDefaultSink为空），只赋值不判断逻辑
            if currentDefaultSink == "" {
                currentDefaultSink = nodeName
                fmt.Printf("[Init] 初始默认 Sink 设置为: %s\n", nodeName)
                continue
            }

            // 获取新旧设备信息进行判断
            oldDevID, oldOk := GetDeviceIDByNodeName(currentDefaultSink)
            newDevID, newOk := GetDeviceIDByNodeName(nodeName)

            if oldOk && newOk {
                devsMu.RLock()
                oldDev := GlobalDevices[oldDevID]
                newDev := GlobalDevices[newDevID]
                devsMu.RUnlock()
                
                if IsPrivateDevice(oldDev) && IsPublicDevice(newDev) {
                    fmt.Printf("[Logic] 检测到 Sink 切换：从私有状态 切换到了 公共状态！\n")
                }
            }

            // 无论判断是否通过，都要更新当前变量
            currentDefaultSink = nodeName
            IsUserOperation = false

        case "default.configured.audio.sink":
            IsUserOperation = true
            fmt.Printf("[Event] 用户切换 Sink 为: %s \n", nodeName)
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