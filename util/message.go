package util

import (
	"encoding/binary"
	"log"
	"net"
)

func SendNetMessage(conn net.Conn, cmd uint32, data string) {
	if conn == nil {
		log.Panicln("conn cannot be nil")
	}
	totalLength := 4 + 4
	if data != "" {
		totalLength += len(data)
	}
	buf := make([]byte, totalLength)
	binary.LittleEndian.PutUint32(buf, uint32(totalLength-4))
	binary.LittleEndian.PutUint32(buf[4:], cmd)
	if data != "" {
		copy(buf[8:], data)
	}
	conn.Write(buf)
}

func RecvNetMessage(conn net.Conn) (uint32, string) {
	if conn == nil {
		log.Panicln("conn cannot be nil")
	}
	readConn := func(total uint32) []byte {
		buffer := make([]byte, total)
		var totalRead uint32 = 0
		for totalRead < total {
			n, err := conn.Read(buffer[totalRead:])
			if err != nil {
				log.Panicln("Failed to read data from conn:", err)
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
