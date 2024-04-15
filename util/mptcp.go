package util

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"syscall"
)

func CreateMptcpListener(port int32) net.Listener {
	// Create a socket
	proto := getSockProto()
	if proto == 0 {
		fmt.Println("use tcp")
	} else {
		fmt.Println("use mptcp:", proto)
	}
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, proto)
	if err != nil {
		fmt.Println("Failed to create socket", err)
		return nil
	}
	// defer syscall.Close(fd)

	// Bind the socket to a specific address and port
	addr := syscall.SockaddrInet4{
		Port: int(port),
		Addr: [4]byte{0, 0, 0, 0}, // 0.0.0.0
	}
	err = syscall.Bind(fd, &addr)
	if err != nil {
		fmt.Println("Socket bind failed:", err)
		return nil
	}

	// Listen on the socket
	err = syscall.Listen(fd, 10)
	if err != nil {
		fmt.Println("Socket listen failed:", err)
		return nil
	}

	// Create a net.Listener from the socket
	file := os.NewFile(uintptr(fd), "")
	// defer file.Close()
	listener, err := net.FileListener(file)
	if err != nil {
		fmt.Println("FileListener creation failed:", err)
		return nil
	}
	fmt.Printf("listener on port %d has been created\n", port)
	return listener
}

// NOTE: use defer file.Close() after this function
func CreateMptcpConnection(ip string, port int32) (*os.File, net.Conn) {
	// Create a socket
	proto := getSockProto()
	if proto == 0 {
		fmt.Println("use tcp")
	} else {
		fmt.Println("use mptcp:", proto)
	}
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, proto)
	if err != nil {
		fmt.Println("Failed to create socket", err)
		return nil, nil
	}
	defer syscall.Close(fd)

	tokens := strings.Split(ip, ".")
	if len(tokens) != 4 {
		fmt.Println("Error address is not valid ipv4")
		return nil, nil
	}
	var addr [4]byte
	for i, tok := range tokens {
		num, err := strconv.Atoi(tok)
		if err != nil {
			fmt.Printf("Error converting string to integer: %v\n", err)
			return nil, nil
		}
		addr[i] = byte(num)
	}
	// Prepare the server address
	serverAddr := &syscall.SockaddrInet4{
		Port: int(port), // Server port
		Addr: addr,      // Server IP address
	}

	// Connect to the server
	err = syscall.Connect(fd, serverAddr)
	if err != nil {
		fmt.Println("Failed to connect:", err)
		return nil, nil
	}

	// Convert the sockfile descriptor to a net.Conn
	sockfile := os.NewFile(uintptr(fd), "")
	defer sockfile.Close()

	conn, err := net.FileConn(sockfile)
	if err != nil {
		fmt.Println("Failed to create net.Conn:", err)
		return nil, nil
	}
	return sockfile, conn
}

func getSockProto() int {
	command := "grep MPTCP /usr/include/linux/in.h"
	output, err := exec.Command("bash", "-c", command).Output()
	if err != nil {
		// fmt.Printf("Cannot find mptcp in %s\n", socketpath)
		return 0
	}
	re := regexp.MustCompile(`\d+`)
	mptcpCode, err := strconv.Atoi(re.FindString(string(output)))
	if err != nil {
		fmt.Printf("Failed to fetch mptcp code from output %s: %v", output, err)
		return 0
	}
	return mptcpCode
}
