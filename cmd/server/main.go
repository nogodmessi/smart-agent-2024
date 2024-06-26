package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"os"
	"os/exec"
	"smart-agent/config"
	"smart-agent/service"
	"smart-agent/util"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-redis/redis/v8"
)

type SenderBuffer struct {
	senderId      string
	priority      int
	triggerSendCh chan bool
	receiverId    string
}

// Record used for receiver to receive data from many senders
type SenderRecord struct {
	conn   net.Conn
	waitCh chan bool
}

type AgentServer struct {
	redisCli     *redis.Client
	myClusterIp  string
	senderMap    map[string]SenderRecord
	bufferMap    map[string]SenderBuffer
	k8sCli       *service.K8SClient
	mu           sync.Mutex
	connWithNode net.Conn
	isFirstData  bool
}

func main() {
	// Create redis client
	redisCli := redis.NewClient(&redis.Options{
		Addr:     fmt.Sprintf("localhost:%d", config.RedisPort), // Redis server address
		Password: "",                                            // Redis server password
		DB:       0,                                             // Redis database number
	})
	defer redisCli.Close()

	// Ping the Redis server to check the connection
	pong, err := redisCli.Ping(context.Background()).Result()
	if err != nil {
		log.Println("Failed to connect to Redis:", err)
	}
	log.Println("Connected to Redis:", pong)

	ser := AgentServer{
		redisCli:    redisCli,
		myClusterIp: "",
		senderMap:   make(map[string]SenderRecord),
		bufferMap:   make(map[string]SenderBuffer),
		k8sCli:      service.NewK8SClientInCluster(),
		isFirstData: true,
	}

	var wg sync.WaitGroup
	wg.Add(5)

	go func() {
		defer wg.Done()
		listener := util.CreateMptcpListener(config.ClientServePort)
		defer listener.Close()
		// Accept and handle client connections
		for {
			conn, err := listener.Accept()
			if err != nil {
				log.Println("Failed to accept client connection:", err)
				continue
			}

			go ser.handleClient(conn)
		}
	}()

	go func() {
		defer wg.Done()
		listener := util.CreateMptcpListener(config.DataTransferPort)
		defer listener.Close()
		// Accept and handle client connections
		for {
			conn, err := listener.Accept()
			if err != nil {
				log.Println("Failed to accept transfer connection:", err)
				continue
			}

			go ser.handleTransfer(conn)
		}
	}()

	go func() {
		defer wg.Done()
		serverAddr, err := net.ResolveUDPAddr("udp", fmt.Sprintf(":%d", config.PingPort))
		if err != nil {
			log.Println("Error resolving server address:", err)
			return
		}

		conn, err := net.ListenUDP("udp", serverAddr)
		if err != nil {
			log.Println("Error listening:", err)
			return
		}
		defer conn.Close()

		buffer := make([]byte, 1024)

		for {
			_, addr, err := conn.ReadFromUDP(buffer)
			if err != nil {
				log.Println("Error reading message:", err)
				continue
			}

			// log.Printf("Received ping from %s: %s\n", addr.String(), string(buffer[:n]))

			_, err = conn.WriteToUDP([]byte("pong"), addr)
			if err != nil {
				log.Println("Error sending pong:", err)
			}
		}
	}()

	go func() {
		defer wg.Done()
		for {
			ser.GetLossAwareness()
		}
	}()

	go func() {
		defer wg.Done()
		for {
			ser.GetLatencyAwareness()
		}

	}()

	wg.Wait()
}

func checkCmdType(cmd uint32, target uint32) {
	if cmd != target {
		log.Fatalln("expected cmd type: %d, actual: %d", target, cmd)
	}
}

func (ser *AgentServer) isFirstPriority(senderId string) bool {
	ser.mu.Lock()
	defer ser.mu.Unlock()

	sbf, ok := ser.bufferMap[senderId]
	if !ok {
		log.Fatalf("sender %s record is not created in isFirstPriority", senderId)
	}

	maxPri := 0
	for cli, bf := range ser.bufferMap {
		if cli == senderId || bf.receiverId != sbf.receiverId {
			continue
		}
		if bf.priority > maxPri {
			maxPri = bf.priority
		}
	}
	return sbf.priority >= maxPri
}

func (ser *AgentServer) handleClient(conn net.Conn) {
	defer conn.Close()

	_, cliId := util.RecvNetMessage(conn)
	_, clientType := util.RecvNetMessage(conn)
	_, cliPriorityStr := util.RecvNetMessage(conn)
	priority, _ := strconv.Atoi(cliPriorityStr)
	_, currClusterIp := util.RecvNetMessage(conn)

	// 实现读取宿主机的物理ip地址，并存到map中
	nodeIP, err := ioutil.ReadFile("node/ip.txt")
	if err != nil {
		log.Println("Error reading node IP file :", err)
		return
	}
	key := cliId + "nodeIP"
	ser.k8sCli.EtcdPut(key, string(nodeIP))
	if ser.myClusterIp == "" {
		ser.myClusterIp = currClusterIp
		log.Printf("my cluster ip = %s\n", ser.myClusterIp)
	}
	_, prevClusterIp := util.RecvNetMessage(conn)
	// fetch old data
	if prevClusterIp != "" && prevClusterIp != ser.myClusterIp {
		for _, data := range ser.fetchData(cliId, prevClusterIp) {
			log.Println("rpush", cliId, data)
			ser.redisCli.RPush(context.Background(), cliId, data)
		}
	}
	log.Println(cliId, clientType, currClusterIp, prevClusterIp)
	util.SendNetMessage(conn, config.TransferFinished, "")

	if clientType == config.RoleSender {
		log.Println("serve for sender", cliId)

		_, receiverId := util.RecvNetMessage(conn)

		senderExit := false
		triggerSendCh := make(chan bool, 1)
		exitCh := make(chan bool, 1)
		exitWaitCh := make(chan bool, 1)
		bufferedData := []string{}
		var receiverClusterIp string = ""
		var transferConn net.Conn = nil

		ser.mu.Lock()
		ser.bufferMap[cliId] = SenderBuffer{
			senderId:      cliId,
			priority:      priority,
			triggerSendCh: triggerSendCh,
			receiverId:    receiverId,
		}
		ser.mu.Unlock()

		beginTransfer := func() {
			sockfile, tconn := util.CreateMptcpConnection(receiverClusterIp, config.DataTransferPort)
			transferConn = tconn
			if conn == nil {
				log.Fatalln("Failed to create connection when create peer transfer conn")
			}
			defer sockfile.Close()
			util.SendNetMessage(transferConn, config.SendFreshData, "")
			util.SendNetMessage(transferConn, config.ClientId, cliId)
		}
		waitUntilConnCreated := func() {
			for {
				_, ok := ser.senderMap[cliId]
				if ok {
					break
				}
				time.Sleep(time.Millisecond * 10)
			}
		}
		endTransfer := func() {
			//if receiverClusterIp == "" {
			//	log.Fatalln("receiver cluster ip is empty when sending data")
			//}
			if receiverClusterIp != currClusterIp {
				if transferConn != nil {
					util.SendNetMessage(transferConn, config.TransferEnd, "")
				}
			} else {
				waitUntilConnCreated()
				util.SendNetMessage(ser.senderMap[cliId].conn, config.TransferEnd, cliId)
			}
		}
		sendBuffferedData := func() {
			if receiverClusterIp == "" {
				log.Fatalln("receiver cluster ip is empty when sending data")
			}
			if receiverClusterIp != currClusterIp {
				if transferConn == nil {
					// 和对端的云化代理建立通信并发送两个指令
					beginTransfer()
				}
				for _, data := range bufferedData {
					util.SendNetMessage(transferConn, config.ClientData, data)
					log.Printf("send %s to %s\n", data, receiverClusterIp)
				}
			} else {
				waitUntilConnCreated()
				for _, data := range bufferedData {
					util.SendNetMessage(ser.senderMap[cliId].conn, config.ClientData, data)
				}
			}
			bufferedData = []string{}
		}
		transferData := func(data string) {
			if receiverClusterIp == "" {
				log.Fatalln("receiver cluster ip is empty when sending data")
			}
			if receiverClusterIp != currClusterIp {
				if transferConn == nil {
					// 发送方连接的云化代理与接收方所连接的云化代理建立三次握手阶段
					beginTransfer()
				}
				log.Printf("send %s to %s\n", data, receiverClusterIp)
				util.SendNetMessage(transferConn, config.ClientData, data)
			} else {
				waitUntilConnCreated()
				util.SendNetMessage(ser.senderMap[cliId].conn, config.ClientData, data)
			}
		}

		// pull receiver cluster ip in a loop
		// wait until receiver connects into the cluster
		go func() {
			for {
				ip, err := ser.k8sCli.EtcdGet(receiverId)
				if err != nil {
					log.Fatalln("Failed to get receiver cluster ip:", err)
				}
				if ip == "" {
					time.Sleep(time.Millisecond * 300)
				} else {
					receiverClusterIp = ip
					log.Printf("get receiver %s cluster ip: %s\n", receiverId, receiverClusterIp)
					select {
					case triggerSendCh <- true:
					default:
					}
					break
				}
			}
		}()

		// send buffered data loop
		go func() {
		sendloop:
			for {
				select {
				case <-triggerSendCh:
				case <-exitCh:
					break sendloop
				}
				if ser.isFirstPriority(cliId) {
					log.Println("send buffered data...")
					sendBuffferedData()
				}
				if senderExit {
					exitWaitCh <- true
				}
			}
		}()

		for {
			cmd, data := util.RecvNetMessage(conn)
			log.Println("将要转发的数据为:", data)
			if cmd == config.ClientData {
				// if the peer hasn't connected into k8s, buffer the data first
				if receiverClusterIp == "" {
					log.Println("buffer data (receiver not connected):", data)
					bufferedData = append(bufferedData, data)
				} else {
					if ser.isFirstPriority(cliId) {
						for len(bufferedData) > 0 {
							time.Sleep(time.Millisecond * 10)
						}
						transferData(data)
					} else {
						// if not the first priority, buffer data
						log.Println("buffer data (not first priority):", data)
						bufferedData = append(bufferedData, data)
					}
				}
			} else if cmd == config.ClientExit {
				// sender disconnect before receiver connects
				senderExit = true
				if len(bufferedData) > 0 {
					<-exitWaitCh
				}
				// TODO 当前不需要接收方连接云化代理，所以不需要判断
				endTransfer()
				select {
				case exitCh <- true:
				default:
				}
				ser.mu.Lock()
				delete(ser.bufferMap, cliId)
				ser.mu.Unlock()
				ser.triggerNextPriority(receiverId)
				log.Printf("sender %s Exit", cliId)
				break
			} else if cmd == config.FetchClientData {
				targetClientId := data
				_, targetClusterIp := util.RecvNetMessage(conn)
				for _, data := range ser.fetchData(targetClientId, targetClusterIp) {
					util.SendNetMessage(conn, config.TransferData, data)
				}
				util.SendNetMessage(conn, config.TransferEnd, "")
			} else if cmd == config.CreateConnBetweenServerAndNode {
				// 读取node/ip.txt文件，获取本node的ip
				nodeIP, err := ioutil.ReadFile("node/ip.txt")
				if err != nil {
					log.Println("Error reading node IP file :", err)
					return
				}
				nodeAddr := strings.TrimSpace(string(nodeIP))
				// 本云化代理与node建立连接（使用ip + 端口号），并把data发送给node
				_, ser.connWithNode = util.CreateMptcpConnection(nodeAddr, config.ClientNode)
				if ser.connWithNode == nil {
					log.Fatalln("Failed to create connection when server transfer to node")
				}
			} else if cmd == config.ClientDataToLocal {
				_, clientId := util.RecvNetMessage(conn)
				// 在传输数据到Node之前需要在云化代理的本地缓存中记录数据
				log.Println("rpush", clientId, data)
				ser.redisCli.RPush(context.Background(), clientId, data)

				if ser.isFirstData {
					key := receiverId + "nodeIP"
					ip, _ := ser.k8sCli.EtcdGet(key)
					parts := strings.Split(data, "|")
					if len(parts) >= 3 {
						if net.ParseIP(parts[2]) != nil {
							// Replace the IP address in data with the retrieved IP
							parts[2] = ip
							data = strings.Join(parts, "|")
							ser.isFirstData = false
						}
					}

				}
				// 转发数据到Node上
				_, err := ser.connWithNode.Write([]byte(data))
				if err != nil {
					fmt.Println("Error sending data to node:", err)
					return
				}
			} else if cmd == config.DisconnBetweenServerAndNode {
				log.Println("agent and node close the connection")
				ser.connWithNode.Close()
				ser.isFirstData = true
			}
		}
	} else if clientType == config.RoleReceiver {
		_, recvNumStr := util.RecvNetMessage(conn)
		recvNum, _ := strconv.Atoi(recvNumStr)
		senderIds := []string{}
		for i := 0; i < recvNum; i++ {
			_, senderId := util.RecvNetMessage(conn)
			senderIds = append(senderIds, senderId)
		}

		log.Printf("%s recv from %d senders: %v\n", cliId, recvNum, senderIds)

		go func() {
			for {
				for {
					content, err := ioutil.ReadFile("node/flag.txt")
					if err != nil {
						log.Println("Error reading file:", err)
						return
					}
					flag := strings.TrimSpace(string(content))
					if flag == "Ready" {
						break
					}
					time.Sleep(time.Millisecond * 2000)
				}
				// 读取node/file.txt文件中的内容转发给接收端
				fileContent, err := ioutil.ReadFile("node/file.txt")
				if err != nil {
					log.Println("Error reading file:", err)
					return
				}
				scanner := bufio.NewScanner(strings.NewReader(string(fileContent)))
				for scanner.Scan() {
					line := scanner.Text()
					util.SendNetMessage(conn, config.ClientData, line)
				}
				util.SendNetMessage(conn, config.TransferEnd, "")
				if err := scanner.Err(); err != nil {
					log.Println("Error scanning file content:", err)
					return
				}
				err = ioutil.WriteFile("node/flag.txt", []byte("NotReady"), 0644)
				if err != nil {
					log.Println("Error writing file:", err)
					return
				}
				log.Println("flag.txt file updated successfully")

			}
		}()
		wg := sync.WaitGroup{}
		wg.Add(len(senderIds))
		for _, senderId := range senderIds {
			go func(senderId string) {
				defer wg.Done()
				log.Printf("set conn map [%s]\n", senderId)
				ch := make(chan bool)
				ser.mu.Lock()
				ser.senderMap[senderId] = SenderRecord{
					conn:   conn,
					waitCh: ch,
				}
				ser.mu.Unlock()
				<-ch
				ser.mu.Lock()
				delete(ser.senderMap, senderId)
				ser.mu.Unlock()
				log.Printf("sender %s finsihed\n", senderId)
			}(senderId)
		}
		wg.Wait()
	} else {
		log.Fatalln("unknown client type:", clientType)
	}
}

func (ser *AgentServer) triggerNextPriority(receiverId string) {
	ser.mu.Lock()
	defer ser.mu.Unlock()

	var nextBf SenderBuffer
	maxPri := 0
	for _, bf := range ser.bufferMap {
		if bf.receiverId != receiverId {
			continue
		}
		if bf.priority > maxPri {
			nextBf = bf
			maxPri = bf.priority
		}
	}
	if maxPri > 0 {
		select {
		case nextBf.triggerSendCh <- true:
		default:
		}
		log.Println("trigger next priority:", nextBf.senderId)
	}
}

func (ser *AgentServer) fetchData(clientId string, clusterIp string) []string {
	if clusterIp == ser.myClusterIp {
		result, err := ser.redisCli.LRange(context.Background(), clientId, 0, -1).Result()
		if err != nil {
			log.Println("Error during redis lrange:", err)
			return []string{}
		}
		return result
	}
	log.Printf("start fetching data for %s from %s:%d\n", clientId, clusterIp, config.DataTransferPort)
	sockfile, conn := util.CreateMptcpConnection(clusterIp, config.DataTransferPort)
	if conn == nil {
		log.Println("Failed to create connection when fetching data")
		return []string{}
	}
	defer sockfile.Close()
	util.SendNetMessage(conn, config.FetchOldData, "")
	util.SendNetMessage(conn, config.ClientId, clientId)
	dataset := []string{}
	for {
		cmd, data := util.RecvNetMessage(conn)
		if cmd == config.TransferData {
			dataset = append(dataset, data)
		} else if cmd == config.TransferEnd {
			break
		}
	}
	log.Printf("finish fetching data for %s from %s\n", clientId, clusterIp)
	return dataset
}

func (ser *AgentServer) handleTransfer(conn net.Conn) {
	defer conn.Close()

	cmd, _ := util.RecvNetMessage(conn)
	_, clientId := util.RecvNetMessage(conn)
	if cmd == config.FetchOldData {
		log.Printf("Send %s data to %s\n", clientId, conn.LocalAddr().String())
		result, err := ser.redisCli.LRange(context.Background(), clientId, 0, -1).Result()
		if err != nil {
			log.Println("Error during redis lrange:", err)
			return
		}
		for _, element := range result {
			util.SendNetMessage(conn, config.TransferData, element)
		}
		util.SendNetMessage(conn, config.TransferEnd, "")
		keysDel, err := ser.redisCli.Del(context.Background(), clientId).Result()
		if err != nil {
			log.Println("Failed to delete list", clientId)
		}
		log.Printf("Delete list %s, number of keys deleted: %d\n", clientId, keysDel)
		log.Printf("Send %s data finished\n", clientId)
	} else if cmd == config.SendFreshData {
		for {
			cmd, data := util.RecvNetMessage(conn)
			if cmd == config.ClientData {
				log.Printf("relay data %s to receiver\n", data)
				ser.mu.Lock()
				util.SendNetMessage(ser.senderMap[clientId].conn, config.ClientData, data)
				ser.mu.Unlock()
				log.Println("rpush", clientId, data)
				ser.redisCli.RPush(context.Background(), clientId, data)
			} else if cmd == config.TransferEnd {
				log.Printf("relay end")
				ser.mu.Lock()
				sr := ser.senderMap[clientId]
				util.SendNetMessage(sr.conn, config.TransferEnd, clientId)
				sr.waitCh <- true
				ser.mu.Unlock()
				break
			}
		}
	}
}

// 丢包率测试
func (ser *AgentServer) GetLossAwareness() {
	servers := ser.k8sCli.GetNameSpacePods(config.Namespace)
	ch := make(chan string, len(servers)*2)
	localIpaddr := getIPv4ForInterface("eth0")

	localServerName := ""
	for _, server := range servers {
		if localIpaddr == server.PodIP {
			localServerName = server.PodName
		}
	}
	content, err := ioutil.ReadFile("node/node.txt")
	if err != nil {
		fmt.Println("Error reading file:", err)
		return
	}

	nodeName := strings.TrimSpace(string(content))

	ser.k8sCli.EtcdPut(localServerName, nodeName)

	var wg sync.WaitGroup // WaitGroup 用于等待所有 goroutine 完成
	for _, server := range servers {
		serverIP := server.PodIP
		serverName := server.PodName
		if localIpaddr != serverIP {
			wg.Add(1) // 增加 WaitGroup 的计数器

			go func(serverIP, serverName string) {
				defer wg.Done() // 减少 WaitGroup 的计数器

				packetLoss, _, err := runPingCommand(serverIP, "50", "0.01")

				otherName, _ := ser.k8sCli.EtcdGet(serverName)
				if err != nil {
					log.Println("Failed to get Loss:", err)
					packetLoss = "100%"
				}
				ch <- fmt.Sprintf("/%sand%s/loss: %s\n", nodeName, otherName, packetLoss)
			}(serverIP, serverName)
		}
	}

	go func() {
		wg.Wait() // 等待所有 goroutine 完成
		close(ch) // 关闭通道，表示数据发送完成
	}()

	var result strings.Builder
	for data := range ch {
		result.WriteString(data)
	}

	err = os.WriteFile("node/data1.txt", []byte(result.String()), 0644)
	if err != nil {
		fmt.Println("Error writing to file:", err)
	}
}

// 时延测试
func (ser *AgentServer) GetLatencyAwareness() {
	servers := ser.k8sCli.GetNameSpacePods(config.Namespace)
	ch := make(chan string, len(servers))
	localIpaddr := getIPv4ForInterface("eth0")

	localServerName := ""
	for _, server := range servers {
		if localIpaddr == server.PodIP {
			localServerName = server.PodName
		}
	}
	content, err := ioutil.ReadFile("node/node.txt")
	if err != nil {
		fmt.Println("Error reading file:", err)
		return
	}

	nodeName := strings.TrimSpace(string(content))

	ser.k8sCli.EtcdPut(localServerName, nodeName)

	var wg sync.WaitGroup // WaitGroup 用于等待所有 goroutine 完成
	for _, server := range servers {
		serverIP := server.PodIP
		serverName := server.PodName
		if localIpaddr != serverIP {
			wg.Add(1) // 增加 WaitGroup 的计数器

			go func(serverIP, serverName string) {
				defer wg.Done() // 减少 WaitGroup 的计数器

				_, avgRTT, err := runPingCommand(serverIP, "2", "0.1")
				otherName, _ := ser.k8sCli.EtcdGet(serverName)
				if err != nil {
					avgRTT = "9999"
				}
				ch <- fmt.Sprintf("/%sand%s/delay: %s\n", nodeName, otherName, avgRTT)
			}(serverIP, serverName)
		}
	}

	go func() {
		wg.Wait() // 等待所有 goroutine 完成
		close(ch) // 关闭通道，表示数据发送完成
	}()

	var result strings.Builder
	for data := range ch {
		result.WriteString(data)
	}

	err = os.WriteFile("node/data3.txt", []byte(result.String()), 0644)
	if err != nil {
		fmt.Println("Error writing to file:", err)
	}
}

// 实现客户端发送ping命令（测指定次数的平均时延以及数据丢失率）
func runPingCommand(serverIp string, times string, interval string) (string, string, error) {
	cmd := exec.Command("ping", "-c", times, "-i", interval, "-W", "1", serverIp)
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	if err != nil {
		return "", "", err
	}
	output := out.String()
	lines := strings.Split(output, "\n")
	packetLossLine := lines[len(lines)-3]
	statisticsLine := lines[len(lines)-2]

	packetLoss := strings.Split(packetLossLine, ",")[2]
	packetLoss = strings.TrimSpace(strings.Split(packetLoss, " ")[1])

	avgRTT := strings.Split(statisticsLine, "/")[4]
	//fmt.Println("Packet Loss:", packetLoss)
	//fmt.Println("Average RTT:", avgRTT)
	return packetLoss, avgRTT, nil
}

func getIPv4ForInterface(ifaceName string) string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return ""
	}

	for _, iface := range interfaces {
		if iface.Name == ifaceName {
			addrs, err := iface.Addrs()
			if err != nil {
				return ""
			}

			for _, addr := range addrs {
				ipNet, ok := addr.(*net.IPNet)
				if ok && !ipNet.IP.IsLoopback() {
					if ipNet.IP.To4() != nil {
						return ipNet.IP.String()
					}
				}
			}
		}
	}

	return ""
}
