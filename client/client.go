package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
)

type Client struct {
	conn net.Conn
	mu   sync.Mutex
}

func main() {
	if len(os.Args) != 3 {
		fmt.Printf("Usage: %s <file_path> <server_host:port>\n", os.Args[0])
		fmt.Println("Example: client myfile.txt localhost:8080")
		return
	}

	filePath := os.Args[1]
	serverAddr := os.Args[2]

	client := &Client{}
	if err := client.start(filePath, serverAddr); err != nil {
		fmt.Printf("Client error: %v\n", err)
		return
	}
}

func (c *Client) start(filePath, serverAddr string) error {
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt, syscall.SIGTERM)

	done := make(chan error, 1)

	go func() {
		done <- c.transferFile(filePath, serverAddr)
	}()

	select {
	case err := <-done:
		return err
	case <-interrupt:
		fmt.Printf("\nReceived interrupt signal. Closing connection...\n")
		c.closeConnection()

		err := <-done
		if err != nil {
			fmt.Printf("Transfer interrupted: %v\n", err)
		} else {
			fmt.Println("Transfer completed before interrupt")
		}
		return nil
	}
}

func (c *Client) transferFile(filePath, serverAddr string) error {
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		return fmt.Errorf("cannot access file: %v", err)
	}

	if fileInfo.Size() > 1<<40 {
		return fmt.Errorf("file too large: %d bytes", fileInfo.Size())
	}

	conn, err := net.Dial("tcp", serverAddr)
	if err != nil {
		return fmt.Errorf("cannot connect to server: %v", err)
	}

	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()

	defer c.closeConnection()

	fmt.Printf("Connected to server %s\n", serverAddr)

	fileName := filepath.Base(filePath)
	if err := sendFileMetadata(conn, fileName, fileInfo.Size()); err != nil {
		return fmt.Errorf("failed to send metadata: %v", err)
	}

	fmt.Printf("Sending file: %s (%d bytes)\n", fileName, fileInfo.Size())

	sentBytes, err := sendFileContent(conn, filePath)
	if err != nil {
		return fmt.Errorf("failed to send file content: %v", err)
	}

	success, message, err := receiveStatus(conn)
	if err != nil {
		return fmt.Errorf("failed to receive status: %v", err)
	}

	if success {
		fmt.Printf("Transfer successful: %s\n", message)
		fmt.Printf("Sent %d bytes\n", sentBytes)
	} else {
		fmt.Printf("Transfer failed: %s\n", message)
		fmt.Printf("Sent %d bytes\n", sentBytes)
	}

	return nil
}

func (c *Client) closeConnection() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
	}
}

func sendFileMetadata(conn net.Conn, fileName string, fileSize int64) error {
	fileNameBytes := []byte(fileName)
	fileNameSize := uint32(len(fileNameBytes))

	if fileNameSize > 4096 {
		return fmt.Errorf("filename too long: %d bytes", fileNameSize)
	}

	headerSize := 4 + len(fileNameBytes) + 8
	buffer := make([]byte, headerSize)

	binary.BigEndian.PutUint32(buffer[0:4], fileNameSize)

	copy(buffer[4:4+len(fileNameBytes)], fileNameBytes)

	binary.BigEndian.PutUint64(buffer[4+len(fileNameBytes):], uint64(fileSize))

	if _, err := conn.Write(buffer); err != nil {
		return fmt.Errorf("failed to send metadata: %v", err)
	}

	return nil
}

func sendFileContent(conn net.Conn, filePath string) (int64, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return 0, fmt.Errorf("cannot open file: %v", err)
	}
	defer file.Close()

	sent, err := io.Copy(conn, file)
	if err != nil {
		return sent, fmt.Errorf("failed to send file: %v", err)
	}

	return sent, nil
}

func receiveStatus(conn net.Conn) (bool, string, error) {
	typeBuf := make([]byte, 1)
	if _, err := io.ReadFull(conn, typeBuf); err != nil {
		return false, "", fmt.Errorf("failed to read status type: %v", err)
	}
	statusType := typeBuf[0]

	lenBuf := make([]byte, 4)
	if _, err := io.ReadFull(conn, lenBuf); err != nil {
		return false, "", fmt.Errorf("failed to read message length: %v", err)
	}
	messageLen := binary.BigEndian.Uint32(lenBuf)

	if messageLen > 1024*1024 {
		return false, "", fmt.Errorf("message too long: %d bytes", messageLen)
	}

	messageBuf := make([]byte, messageLen)
	if _, err := io.ReadFull(conn, messageBuf); err != nil {
		return false, "", fmt.Errorf("failed to read message: %v", err)
	}

	message := string(messageBuf)

	switch statusType {
	case 0: // Success
		return true, message, nil
	case 1: // Error
		return false, message, nil
	default:
		return false, "", fmt.Errorf("unknown status type: %d", statusType)
	}
}
