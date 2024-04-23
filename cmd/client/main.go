package main

import (
	"bufio"
	"flag"
	"fmt"
	"k8s.io/client-go/util/homedir"
	"math"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"smart-agent/config"
	"smart-agent/service"
	"smart-agent/util"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type ServerInfo struct {
	transferIp  string
	serviceIp   string
	serviceName string
	proxyPort   int32
	pingPort    int32
	delay       time.Duration
}

type AgentClient struct {
	clientId      string
	conn          net.Conn
	k8sCli        service.K8SClient
	k8sSvc        []service.Service
	serverInfo    []ServerInfo
	k8sIp         string
	prevClusterIp string
	currClusterIp string
	role          string
	receiverId    string
	senderIds     []string
	priority      int
}

type stringSlice []string

func (f *stringSlice) String() string {
	return fmt.Sprintf("%v", []string(*f))
}

func (f *stringSlice) Set(value string) error {
	*f = append(*f, value)
	return nil
}

func main() {
	var priority int
	var recvFroms stringSlice
	clientId := flag.String("client", "", "Client ID")
	sendTo := flag.String("sendto", "", "Receiver Client ID")
	flag.Var(&recvFroms, "recvfrom", "Sender Client IDs")
	kubeConfig := flag.String("config", "", "Kubernetes Config Path")
	flag.IntVar(&priority, "priority", 0, "Client Priority")
	flag.Parse()

	// Check if the input file flag is provided
	if *clientId == "" {
		fmt.Println("Client Id is required.")
		return
	}
	if *sendTo != "" && len(recvFroms) > 0 {
		fmt.Println("Can not be sender and receiver at the same time")
		return
	}
	cli := newAgentClient(*clientId, *kubeConfig, priority)
	cli.updateServerInfo()
	cli.etcdCleanup()
	if *sendTo != "" {
		cli.setSender(*sendTo)
	} else if len(recvFroms) > 0 {
		cli.setReceiver(recvFroms)
	}

	interruptChan := make(chan os.Signal, 1)
	eofCh := make(chan bool, 1)
	signal.Notify(interruptChan, syscall.SIGINT, syscall.SIGTERM)

	go servicePoller(cli)
	go repl(cli, eofCh)

	select {
	case <-interruptChan:
	case <-eofCh:
	}
}
func newAgentClient(clientId string, kubeconfig string, priority int) AgentClient {
	var configpath string
	if kubeconfig == "" {
		home := homedir.HomeDir()
		configpath = filepath.Join(home, ".kube", "config")
	} else {
		configpath = kubeconfig
	}
	ip := util.GetServerIpFromYaml(configpath)
	fmt.Println("config path:", configpath)
	fmt.Println("cluster ip:", ip)
	cli := AgentClient{
		clientId:      clientId,
		conn:          nil,
		k8sCli:        *service.NewK8SClient(configpath),
		prevClusterIp: "",
		k8sIp:         ip,
		priority:      priority,
	}
	return cli
}

func tryFunc(n int, f func() error) error {
	var err error
	for i := 0; i < n; i++ {
		err = f()
		if err == nil {
			break
		}
		time.Sleep(time.Millisecond * 200)
	}
	if err != nil {
		return err
	}
	return nil
}

func repl(cli AgentClient, eofCh chan bool) {
	fmt.Println("Welcome to Client REPL! Type '.help' for available commands.")
	scanner := bufio.NewScanner(os.Stdin)

	for {
		fmt.Print("> ")
		if !scanner.Scan() {
			eofCh <- true
			break
		}
		command := scanner.Text()

		tokens := strings.Split(command, " ")
		if len(tokens) > 2 {
			fmt.Println("Invalid command. Type '.help' for available commands.")
			continue
		}
		cmd := tokens[0]
		switch cmd {
		case ".help":
			printHelp(cli.role)
		case ".exit":
			cli.disconnect()
			eofCh <- true
			return
		case ".trans":
			policyName := tokens[1]
			cli.chTransPolicy(policyName)
		case ".service":
			cli.showService()
		case ".connect":
			svcName := tokens[1]
			cli.connectToService(svcName)
			fmt.Println("successfully connected")
			cli.roleTask()
		case ".send":
			cli.sendData(tokens[1])
		case ".sendfile":
			cli.sendFile(tokens[1])
		case ".sendfileToNode":
			cli.sendFileToNode(tokens[1])
		case ".fetch":
			var fetchClient string
			if len(tokens) == 1 {
				fetchClient = cli.clientId
			} else {
				fetchClient = tokens[1]
			}
			cli.fetchClientData(fetchClient)
		default:
			fmt.Println("Unknown command. Type '.help' for available commands.")
		}
	}
}

func printHelp(role string) {
	var help string
	if role == config.RoleSender {
		help = fmt.Sprintf(
			`Usage:
    %s <command> [arguments]
The commands and arguments are:
    .help
    .exit
    .trans    [policyName]
    .service
    .connect  [serviceName]
    .send     [data]
    .sendfile [filePath]
    .sendfileToNode [filePath]
    .fetch    [clientId]
`, os.Args[0])
	} else if role == config.RoleReceiver {
		help = fmt.Sprintf(
			`Usage:
    %s <command> [arguments]
The commands and arguments are:
    .help
    .exit
    .service
    .connect  [serviceName]
`, os.Args[0])
	}
	fmt.Println(help)
}

func servicePoller(cli AgentClient) {
	for {
		time.Sleep(time.Second * 100)
		cli.updateServerInfo()
	}
}

func (cli *AgentClient) setSender(peer string) {
	cli.role = config.RoleSender
	cli.receiverId = peer
}

func (cli *AgentClient) setReceiver(senders []string) {
	cli.role = config.RoleReceiver
	cli.senderIds = senders
}

func (cli *AgentClient) updateServerInfo() {
	cli.k8sSvc = cli.k8sCli.GetNamespaceServices(config.Namespace)
	serverNum := len(cli.k8sSvc) / 2
	serverInfo := make([]ServerInfo, serverNum)
	for _, svc := range cli.k8sSvc {
		re := regexp.MustCompile("\\d+")
		match := re.FindString(svc.SvcName)
		if match == "" {
			fmt.Println("Error extracting number from service name:", svc.SvcName)
			continue
		}
		Digit, err := strconv.Atoi(string(match))
		Digit--
		if err != nil {
			fmt.Println("Atoi Error:", err)
			continue
		}
		if strings.HasPrefix(svc.SvcName, config.ProxyServicePrefix) {
			serverInfo[Digit].serviceIp = svc.ClusterIp
			serverInfo[Digit].serviceName = svc.SvcName
			for _, portInfo := range svc.Ports {
				if portInfo.Name == "client-port" {
					serverInfo[Digit].proxyPort = portInfo.NodePort
				} else if portInfo.Name == "ping-port" {
					pingPort := portInfo.NodePort
					serverInfo[Digit].pingPort = pingPort
					serverInfo[Digit].delay, err = cli.getPingDelay(pingPort)
					if err != nil {
						fmt.Printf("fail to ping server on port %d\n", pingPort)
					}
				}
			}
		} else if strings.HasPrefix(svc.SvcName, config.ClusterServicePrefix) {
			serverInfo[Digit].transferIp = svc.ClusterIp
		}
	}
	cli.serverInfo = serverInfo
}

// 新增传输策略选择
func (cli *AgentClient) chTransPolicy(policyName string) {
	switch policyName {
	case "fullmesh":
		cmd := exec.Command("sysctl", "-w", "net.mptcp.mptcp_path_manager=fullmesh")
		err := cmd.Run()
		if err != nil {
			fmt.Println("Error executing fullmesh command:", err)
		}
	case "backup":
		cmd := exec.Command("ip", "link", "set", "dev", "ens33", "multipath", "backup")
		err := cmd.Run()
		if err != nil {
			fmt.Println("Error executing backup command:", err)
		}
	case "redundant":
		cmd := exec.Command("sysctl", "-w", "net.mptcp.mptcp_scheduler=redundant")
		err := cmd.Run()
		if err != nil {
			fmt.Println("Error executing redundant command:", err)
		}
	case "default":
		cmd1 := exec.Command("sysctl", "-w", "net.mptcp.mptcp_path_manager=default")
		cmd2 := exec.Command("ip", "link", "set", "dev", "ens33", "multipath", "off")
		cmd3 := exec.Command("sysctl", "-w", "net.mptcp.mptcp_scheduler=default")

		err1 := cmd1.Run()
		err2 := cmd2.Run()
		err3 := cmd3.Run()

		if err1 != nil || err2 != nil || err3 != nil {
			fmt.Println("Error executing default command")
		}
	default:
		fmt.Println("The transport policy does not exist")
	}

}

func (cli *AgentClient) showService() {
	cli.updateServerInfo()
	headers := []string{"Service Name", "Proxy IP", "Delay", "Latency jitter"}
	fmt.Printf("%-20s %-25s %-15s %-15s\n", headers[0], headers[1], headers[2], headers[3])
	fmt.Println(strings.Repeat("-", 70))
	for _, info := range cli.serverInfo {
		// TODO ip地址修改
		fmt.Printf("%-20s %-25s %-15s %-15s\n", info.serviceName, fmt.Sprintf("%s:%d", cli.k8sIp, info.proxyPort),
			fmt.Sprintf("%.3fms", float64(info.delay.Abs().Microseconds())/1000), fmt.Sprintf("%.3f", cli.getPingDelayJitter(info.pingPort)))
	}
	fmt.Println(strings.Repeat("-", 70))
}

func (cli *AgentClient) sendData(data string) {
	if cli.conn != nil {
		util.SendNetMessage(cli.conn, config.ClientData, data)
	}
}

func (cli *AgentClient) findTransferIp(svcName string) string {
	var transferIp string
	for _, info := range cli.serverInfo {
		if info.serviceName == svcName {
			transferIp = info.transferIp
		}
	}
	return transferIp
}

func (cli *AgentClient) findProxyPort(svcName string) int32 {
	var port int32
	for _, info := range cli.serverInfo {
		if info.serviceName == svcName {
			port = info.proxyPort
		}
	}
	return port
}

func (cli *AgentClient) findPingPort(svcName string) int32 {
	var port int32
	for _, info := range cli.serverInfo {
		if info.serviceName == svcName {
			port = info.pingPort
		}
	}
	return port
}

func (cli *AgentClient) getPingDelay(port int32) (time.Duration, error) {
	serverAddr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", cli.k8sIp, port))
	if err != nil {
		fmt.Println("Error resolving server address:", err)
		return 0, err
	}

	conn, err := net.DialUDP("udp", nil, serverAddr)
	if err != nil {
		fmt.Println("Error connecting to server:", err)
		return 0, err
	}
	defer conn.Close()

	message := []byte("ping")
	start := time.Now()

	_, err = conn.Write(message)
	if err != nil {
		fmt.Println("Error sending ping:", err)
		return 0, err
	}

	buffer := make([]byte, 1024)
	conn.SetReadDeadline(time.Now().Add(2 * time.Second)) // Set read timeout

	_, err = conn.Read(buffer)
	if err != nil {
		fmt.Println("Error receiving pong:", err)
		return 0, err
	}

	elapsed := time.Since(start)
	return elapsed, nil
}

func (cli *AgentClient) getPingDelayJitter(port int32) float64 {
	//use getPingDelay * 5
	var pingDelay []float64
	for i := 0; i < 5; i++ {
		delay, err := cli.getPingDelay(port)
		if err != nil {
			fmt.Println("Error using getPingDelay func:", err)
			continue
		}
		//fmt.Println(delay)
		mDelay := float64(delay) / 1000000 //将数值转换为ms
		//fmt.Println(mDelay)
		pingDelay = append(pingDelay, mDelay)
	}
	if len(pingDelay) == 0 {
		fmt.Println("Error using getPingDelay func:")
		return math.Inf(1) //返回正无穷大
	}
	//计算平均时延
	sumDelay := 0.0
	for _, data := range pingDelay {
		sumDelay += data
	}
	meanDelay := sumDelay / float64(len(pingDelay))

	//计算平方差和
	squaredDifferencesSum := 0.0
	for _, delay := range pingDelay {
		squaredDifferences := (delay - meanDelay) * (delay - meanDelay)
		squaredDifferencesSum += squaredDifferences
	}

	//计算方差
	variance := squaredDifferencesSum / float64(len(pingDelay))
	return variance
}

// TODO 感觉有问题
func (cli *AgentClient) connectToService(svcName string) {
	proxyPort := cli.findProxyPort(svcName)
	clusterIp := cli.findTransferIp(svcName)

	cli.prevClusterIp = cli.currClusterIp
	cli.currClusterIp = clusterIp
	fmt.Printf("change cluster ip from %s to %s\n", cli.prevClusterIp, cli.currClusterIp)
	cli.connectToIpPort(cli.k8sIp, proxyPort) // 不一定是master节点，集群中任一节点都可以
}

// used for local debugging
func (cli *AgentClient) debugConnect(ip string, port int32) {
	cli.prevClusterIp = cli.currClusterIp
	cli.currClusterIp = ip
	fmt.Printf("change cluster ip from %s to %s\n", cli.prevClusterIp, cli.currClusterIp)
	cli.connectToIpPort(ip, port)
}

func (cli *AgentClient) etcdCleanup() {
	err := tryFunc(3, func() error {
		return cli.k8sCli.EtcdDelete(cli.clientId)
	})
	if err != nil {
		fmt.Println("failed to clean:", err)
		os.Exit(1)
	}
}

func (cli *AgentClient) roleTask() {
	err := tryFunc(3, func() error {
		return cli.k8sCli.EtcdPut(cli.clientId, cli.currClusterIp)
	})
	if err != nil {
		fmt.Println("failed to put cluster ip:", err)
		os.Exit(1)
	}
	if cli.role == config.RoleSender {
		util.SendNetMessage(cli.conn, config.ClientId, cli.receiverId)
	} else if cli.role == config.RoleReceiver {
		util.SendNetMessage(cli.conn, config.RecvfromNum, strconv.Itoa(len(cli.senderIds)))
		for _, senderId := range cli.senderIds {
			util.SendNetMessage(cli.conn, config.ClientId, senderId)
		}
		fmt.Println("receiving data:")
		endCount := 0
		for {
			cmd, data := util.RecvNetMessage(cli.conn)
			if cmd == config.ClientData {
				fmt.Println("data:", data)
			} else if cmd == config.TransferEnd {
				endCount++
				fmt.Println("receive all data from:", data)
				if endCount >= len(cli.senderIds) {
					fmt.Println("receiving data ends")
					break
				}
			}
		}
	} else {
		fmt.Println("unknown role type:", cli.role)
		os.Exit(1)
	}
}

func (cli *AgentClient) connectToIpPort(ip string, port int32) {
	fmt.Printf("connect to %s:%d\n", ip, port)

	sockfile, conn := util.CreateMptcpConnection(ip, port)
	if conn == nil {
		os.Exit(1)
	}
	// TODO: handle conn == nil
	defer sockfile.Close()

	if cli.conn != nil {
		util.SendNetMessage(cli.conn, config.ClientExit, "")
		cli.conn.Close()
	}
	util.SendNetMessage(conn, config.ClientId, cli.clientId)
	util.SendNetMessage(conn, config.ClientType, cli.role)
	util.SendNetMessage(conn, config.ClientPriority, strconv.Itoa(cli.priority))
	util.SendNetMessage(conn, config.ClusterIp, cli.currClusterIp)
	util.SendNetMessage(conn, config.ClusterIp, cli.prevClusterIp)
	finished, _ := util.RecvNetMessage(conn)
	if finished != config.TransferFinished {
		fmt.Println("Fail to receive TransferFinished after sending ClientId")
		os.Exit(1)
	}
	fmt.Println("server has fetched old data")
	cli.conn = conn
}

func (cli *AgentClient) disconnect() {
	if cli.conn != nil {
		util.SendNetMessage(cli.conn, config.ClientExit, "")
		cli.conn.Close()
	}
}

func (cli *AgentClient) sendFile(filePath string) {
	if cli.conn == nil {
		return
	}
	// Open the file for reading
	file, err := os.Open(filePath)
	if err != nil {
		fmt.Println("Failed to open file:", err)
		return
	}
	defer file.Close()

	// Create a scanner to read the file line by line
	scanner := bufio.NewScanner(file)

	// Read the file line by line
	for scanner.Scan() {
		line := scanner.Text()
		util.SendNetMessage(cli.conn, config.ClientData, line)
	}
}

func (cli *AgentClient) sendFileToNode(filePath string) {
	if cli.conn == nil {
		return
	}
	// Open the file for reading
	file, err := os.Open(filePath)
	if err != nil {
		fmt.Println("Failed to open file:", err)
		return
	}
	defer file.Close()

	// Create a scanner to read the file line by line
	scanner := bufio.NewScanner(file)

	// Create a conn Between server and Node
	util.SendNetMessage(cli.conn, config.CreateConnBetweenServerAndNode, "")

	// Read the file line by line
	for scanner.Scan() {
		line := scanner.Text()
		util.SendNetMessage(cli.conn, config.ClientDataToLocal, line)
		util.SendNetMessage(cli.conn, config.ClientId, cli.clientId)
	}
	// disconn Between server and Node
	util.SendNetMessage(cli.conn, config.DisconnBetweenServerAndNode, "")
}

func (cli *AgentClient) fetchClientData(clientId string) {
	clusterIp, err := cli.k8sCli.EtcdGet(clientId)
	if err != nil {
		fmt.Printf("Failed to fetch %s's clusterIp: %v\n", clientId, err)
		return
	}
	util.SendNetMessage(cli.conn, config.FetchClientData, clientId)
	util.SendNetMessage(cli.conn, config.ClusterIp, clusterIp)
	dataset := []string{}
	for {
		cmd, data := util.RecvNetMessage(cli.conn)
		if cmd == config.TransferData {
			dataset = append(dataset, data)
		} else if cmd == config.TransferEnd {
			break
		}
	}
	fmt.Printf("%s data:\n", clientId)
	for _, data := range dataset {
		fmt.Println(data)
	}
}
