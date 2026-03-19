package gateway

import (
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"sync"
	"time"

	"github.com/lknhd/proxbox-go/internal/container"
	"github.com/lknhd/proxbox-go/internal/models"

	"golang.org/x/crypto/ssh"
)

type Proxy struct {
	manager    *container.Manager
	gatewayKey ssh.Signer
}

func NewProxy(manager *container.Manager, gatewayKeyPath string) (*Proxy, error) {
	keyData, err := os.ReadFile(gatewayKeyPath)
	if err != nil {
		return nil, fmt.Errorf("read gateway key: %w", err)
	}

	signer, err := ssh.ParsePrivateKey(keyData)
	if err != nil {
		return nil, fmt.Errorf("parse gateway key: %w", err)
	}

	return &Proxy{
		manager:    manager,
		gatewayKey: signer,
	}, nil
}

func (p *Proxy) Connect(sess ssh.Channel, reqs <-chan *ssh.Request, user *models.User, ct *models.Container, ptyReq *ptyRequest) {
	// Auto-start or resume
	var err error
	if ct.Status == "paused" {
		fmt.Fprintf(sess, "Resuming container...\r\n")
		ct, err = p.manager.Resume(user, ct.Name)
	} else if ct.Status == "stopped" {
		fmt.Fprintf(sess, "Starting container...\r\n")
		ct, err = p.manager.Start(user, ct.Name)
	}
	if err != nil {
		fmt.Fprintf(sess, "Error: %v\r\n", err)
		return
	}

	if ct.IPAddress == "" {
		fmt.Fprintf(sess, "Error: Container has no IP address.\r\n")
		return
	}

	fmt.Fprintf(sess, "Connecting to %s (%s)...\r\n", ct.Name, ct.IPAddress)

	// Retry SSH connection to container
	var client *ssh.Client
	for attempt := 0; attempt < 10; attempt++ {
		clientCfg := &ssh.ClientConfig{
			User:            "root",
			Auth:            []ssh.AuthMethod{ssh.PublicKeys(p.gatewayKey)},
			HostKeyCallback: ssh.InsecureIgnoreHostKey(),
			Timeout:         10 * time.Second,
		}

		client, err = ssh.Dial("tcp", net.JoinHostPort(ct.IPAddress, "22"), clientCfg)
		if err == nil {
			break
		}
		if attempt == 9 {
			fmt.Fprintf(sess, "Failed to connect to container: %v\r\n", err)
			return
		}
		time.Sleep(2 * time.Second)
	}
	defer client.Close()

	// Open session on the container
	containerSess, err := client.NewSession()
	if err != nil {
		fmt.Fprintf(sess, "Failed to open session: %v\r\n", err)
		return
	}
	defer containerSess.Close()

	// Request PTY on the container
	if ptyReq != nil {
		if err := containerSess.RequestPty(ptyReq.Term, ptyReq.Rows, ptyReq.Cols, ssh.TerminalModes{}); err != nil {
			fmt.Fprintf(sess, "Failed to request PTY: %v\r\n", err)
			return
		}
	} else {
		if err := containerSess.RequestPty("xterm-256color", 24, 80, ssh.TerminalModes{}); err != nil {
			fmt.Fprintf(sess, "Failed to request PTY: %v\r\n", err)
			return
		}
	}

	// Get pipes
	containerStdin, err := containerSess.StdinPipe()
	if err != nil {
		fmt.Fprintf(sess, "Failed to pipe stdin: %v\r\n", err)
		return
	}

	containerStdout, err := containerSess.StdoutPipe()
	if err != nil {
		fmt.Fprintf(sess, "Failed to pipe stdout: %v\r\n", err)
		return
	}

	containerStderr, err := containerSess.StderrPipe()
	if err != nil {
		fmt.Fprintf(sess, "Failed to pipe stderr: %v\r\n", err)
		return
	}

	// Start shell
	if err := containerSess.Shell(); err != nil {
		fmt.Fprintf(sess, "Failed to start shell: %v\r\n", err)
		return
	}

	var wg sync.WaitGroup

	// Forward client -> container
	wg.Add(1)
	go func() {
		defer wg.Done()
		io.Copy(containerStdin, sess)
		containerStdin.Close()
	}()

	// Forward container stdout -> client
	wg.Add(1)
	go func() {
		defer wg.Done()
		io.Copy(sess, containerStdout)
	}()

	// Forward container stderr -> client
	wg.Add(1)
	go func() {
		defer wg.Done()
		io.Copy(sess, containerStderr)
	}()

	// Handle incoming requests (window-change, etc.)
	go func() {
		for req := range reqs {
			switch req.Type {
			case "window-change":
				if len(req.Payload) >= 8 {
					cols := int(req.Payload[0])<<24 | int(req.Payload[1])<<16 | int(req.Payload[2])<<8 | int(req.Payload[3])
					rows := int(req.Payload[4])<<24 | int(req.Payload[5])<<16 | int(req.Payload[6])<<8 | int(req.Payload[7])
					containerSess.WindowChange(rows, cols)
				}
				if req.WantReply {
					req.Reply(true, nil)
				}
			default:
				if req.WantReply {
					req.Reply(false, nil)
				}
			}
		}
	}()

	// Wait for container session to end
	containerSess.Wait()
	wg.Wait()

	// Pause on disconnect
	if _, err := p.manager.Pause(user, ct.Name); err != nil {
		log.Printf("Failed to pause container '%s': %v", ct.Name, err)
	} else {
		log.Printf("Container '%s' paused on disconnect", ct.Name)
	}
}
