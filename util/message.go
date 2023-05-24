package util

import (
	"encoding/binary"
	"log"
	"net"
)

func SendNetMessage(conn net.Conn, cmd uint32, data string) {
	totalLength := 4 + 4 + len(data)
	buf := make([]byte, totalLength)
	binary.LittleEndian.PutUint32(buf, uint32(totalLength-4))
	binary.LittleEndian.PutUint32(buf[4:], cmd)
	copy(buf[8:], data)
	conn.Write(buf)
}

func RecvNetMessage(conn net.Conn) (uint32, string) {
	readConn := func(total uint32) []byte {
		buffer := make([]byte, total)
		var totalRead uint32 = 0
		for totalRead < total {
			n, err := conn.Read(buffer[totalRead:])
			if err != nil {
				log.Fatalln("Failed	to read data from client")
			}
			totalRead += uint32(n)
		}
		return buffer
	}
	lenBuffer := readConn(4)
	dataLength := binary.LittleEndian.Uint32(lenBuffer)
	dataBuffer := readConn(dataLength)
	cmd := binary.LittleEndian.Uint32(dataBuffer)
	data := string(dataBuffer[4:])
	return cmd, data
}
