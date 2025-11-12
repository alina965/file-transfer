package main

import (
	"encoding/binary"
	"file_transfer/server/utils"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"
)

type Server struct {
	listener net.Listener
	wg       sync.WaitGroup
	mu       sync.RWMutex
	closing  bool
}

func main() {
	if len(os.Args) != 2 {
		fmt.Println("Usage: server <port>")
		return
	}

	port := os.Args[1]

	server := &Server{}
	if err := server.start(port); err != nil {
		fmt.Println("Error starting server:", err)
		os.Exit(1)
	}
}

func (s *Server) start(port string) error {
	listener, err := net.Listen("tcp", ":"+port)
	if err != nil {
		return err
	}
	s.listener = listener

	signalCh := make(chan os.Signal, 1)
	signal.Notify(signalCh, syscall.SIGINT, syscall.SIGTERM)

	done := make(chan struct{})

	fmt.Println("Server listening on " + port)

	go func() {
		sig := <-signalCh
		fmt.Printf("\nReceived signal: %v\n", sig)
		s.shutdown()
		close(done)
	}()

	for {
		conn, err := s.listener.Accept()
		if err != nil {
			s.mu.RLock()
			closing := s.closing
			s.mu.RUnlock()

			if closing {
				break
			}
			fmt.Println("Accept error: ", err)
			continue
		}

		s.wg.Add(1)
		go func(c net.Conn) {
			defer s.wg.Done()
			err := s.handleUserConnection(c)
			if err != nil {
				return
			}
		}(conn)
	}

	<-done
	s.wg.Wait()
	fmt.Println("Server shutdown complete")
	return nil
}

func (s *Server) shutdown() {
	fmt.Println("Initiating shutdown...")

	s.mu.Lock()
	s.closing = true
	s.mu.Unlock()

	if s.listener != nil {
		err := s.listener.Close()
		if err != nil {
			return
		}
	}

	fmt.Println("Waiting for all active connections to complete...")

	s.wg.Wait()

	fmt.Println("All connections completed")
}

func (s *Server) handleUserConnection(conn net.Conn) error {
	defer conn.Close()

	s.mu.RLock()
	closing := s.closing
	s.mu.RUnlock()

	if closing {
		fmt.Println("Rejecting new connection - server is shutting down")
		err := s.sendErrorStatus(conn, "Server is shutting down")
		return err
	}

	clientAddr := conn.RemoteAddr().String()
	fmt.Println("New client! ", clientAddr)

	fileName, fileSize, err := s.receiveFileData(conn)
	if err != nil {
		return fmt.Errorf("Error receiving data from %s: %v\n", clientAddr, err)
	}

	fmt.Printf("Receiving file: %s (size: %d bytes) from %s\n",
		fileName, fileSize, clientAddr)

	if err := utils.EnsureUploadsDir(); err != nil {
		return s.sendErrorStatus(conn, "Cannot create uploads directory")
	}

	receivedSize, err := s.receiveFileContent(conn, fileName, fileSize)
	if err != nil {
		return s.sendErrorStatus(conn, fmt.Sprintf("File transfer failed: %v", err))
	}

	if receivedSize == fileSize {
		err = s.sendSuccessStatus(conn, "File transferred successfully")
		fmt.Printf("File %s received successfully from %s\n", fileName, clientAddr)
		return err
	} else {
		err = s.sendErrorStatus(conn, fmt.Sprintf("Size mismatch: expected %d, got %d",
			fileSize, receivedSize))
		return err
	}
}

func (s *Server) receiveFileData(conn net.Conn) (fileName string, fileSize int64, err error) {
	var nameSize uint32
	err = binary.Read(conn, binary.BigEndian, &nameSize)
	if err != nil {
		return "", 0, fmt.Errorf("failed to read filename size: %v", err)
	}

	if nameSize > 4096 {
		return "", 0, fmt.Errorf("filename too long: %d bytes", nameSize)
	}

	filenameBytes := make([]byte, nameSize)
	_, err = io.ReadFull(conn, filenameBytes)
	if err != nil {
		return "", 0, fmt.Errorf("failed to read filename: %v", err)
	}

	if !utf8.Valid(filenameBytes) {
		return "", 0, fmt.Errorf("filename is not valid UTF-8")
	}

	filename := string(filenameBytes)
	if len(strings.TrimSpace(filename)) == 0 {
		return "", 0, fmt.Errorf("filename cannot be empty")
	}

	err = binary.Read(conn, binary.BigEndian, &fileSize)
	if err != nil {
		return "", 0, fmt.Errorf("failed to read file size: %v", err)
	}

	if fileSize > 1<<40 { // 1 ТБ
		return "", 0, fmt.Errorf("file too large: %d bytes", fileSize)
	}

	if fileSize < 0 {
		return "", 0, fmt.Errorf("invalid file size: %d", fileSize)
	}

	return filename, fileSize, nil
}

func (s *Server) receiveFileContent(conn net.Conn, fileName string, fileSize int64) (int64, error) {
	filePath := filepath.Join("uploads", fileName)

	file, err := os.Create(filePath)
	if err != nil {
		return 0, fmt.Errorf("cannot create file: %v", err)
	}
	defer file.Close()

	monitor := utils.NewSpeedMonitor(conn.RemoteAddr().String())

	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	done := make(chan bool, 1)
	defer close(done)

	progressReader := &ProgressReader{
		Reader: conn,
		OnRead: func(bytes int64) {
			monitor.Update(bytes)
		},
	}

	go func() {
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				monitor.CheckAndPrint()
			}
		}
	}()

	received, err := io.CopyN(file, progressReader, fileSize)
	if err != nil {
		os.Remove(filePath)
		return received, fmt.Errorf("error receiving file content: %v", err)
	}

	done <- true
	monitor.CheckAndPrint()

	return received, nil
}

type ProgressReader struct {
	Reader io.Reader
	OnRead func(bytes int64)
}

func (pr *ProgressReader) Read(p []byte) (int, error) {
	n, err := pr.Reader.Read(p)
	if n > 0 && pr.OnRead != nil {
		pr.OnRead(int64(n))
	}
	return n, err
}

type StatusType byte

const (
	StatusSuccess StatusType = 0
	StatusError   StatusType = 1
)

func (s *Server) sendSuccessStatus(conn net.Conn, message string) error {
	return s.sendStatus(conn, StatusSuccess, message)
}

func (s *Server) sendErrorStatus(conn net.Conn, message string) error {
	return s.sendStatus(conn, StatusError, message)
}

func (s *Server) sendStatus(conn net.Conn, statusType StatusType, message string) error {
	messageBytes := []byte(message)
	messageLen := uint32(len(messageBytes))

	// буфер для записи: [1 байт тип] [4 байта длина сообщения] [сообщение]
	buffer := make([]byte, 1+4+len(messageBytes))

	buffer[0] = byte(statusType)

	binary.BigEndian.PutUint32(buffer[1:5], messageLen)

	copy(buffer[5:], messageBytes)

	_, err := conn.Write(buffer)
	if err != nil {
		return fmt.Errorf("failed to send status: %v", err)
	}

	return nil
}
