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

	gssh "github.com/gliderlabs/ssh"
	cryptossh "golang.org/x/crypto/ssh"
)

type Proxy struct {
	manager    *container.Manager
	gatewayKey cryptossh.Signer
}

func NewProxy(manager *container.Manager, gatewayKeyPath string) (*Proxy, error) {
	keyData, err := os.ReadFile(gatewayKeyPath)
	if err != nil {
		return nil, fmt.Errorf("read gateway key: %w", err)
	}

	signer, err := cryptossh.ParsePrivateKey(keyData)
	if err != nil {
		return nil, fmt.Errorf("parse gateway key: %w", err)
	}

	return &Proxy{
		manager:    manager,
		gatewayKey: signer,
	}, nil
}

func (p *Proxy) Connect(sess gssh.Session, user *models.User, ct *models.Container) {
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
		sess.Exit(1)
		return
	}

	if ct.IPAddress == "" {
		fmt.Fprintf(sess, "Error: Container has no IP address.\r\n")
		sess.Exit(1)
		return
	}

	fmt.Fprintf(sess, "Connecting to %s (%s)...\r\n", ct.Name, ct.IPAddress)

	// Retry SSH connection to container
	var client *cryptossh.Client
	for attempt := 0; attempt < 10; attempt++ {
		clientCfg := &cryptossh.ClientConfig{
			User:            "root",
			Auth:            []cryptossh.AuthMethod{cryptossh.PublicKeys(p.gatewayKey)},
			HostKeyCallback: cryptossh.InsecureIgnoreHostKey(),
			Timeout:         10 * time.Second,
		}

		client, err = cryptossh.Dial("tcp", net.JoinHostPort(ct.IPAddress, "22"), clientCfg)
		if err == nil {
			break
		}
		if attempt == 9 {
			fmt.Fprintf(sess, "Failed to connect to container: %v\r\n", err)
			sess.Exit(1)
			return
		}
		time.Sleep(2 * time.Second)
	}
	defer client.Close()

	// Open session on the container
	containerSess, err := client.NewSession()
	if err != nil {
		fmt.Fprintf(sess, "Failed to open session: %v\r\n", err)
		sess.Exit(1)
		return
	}
	defer containerSess.Close()

	// Request PTY on the container, forwarding client's PTY settings
	ptyReq, winCh, isPty := sess.Pty()
	if isPty {
		if err := containerSess.RequestPty(ptyReq.Term, ptyReq.Window.Height, ptyReq.Window.Width, cryptossh.TerminalModes{}); err != nil {
			fmt.Fprintf(sess, "Failed to request PTY: %v\r\n", err)
			sess.Exit(1)
			return
		}
	} else {
		// Fallback: request a default PTY
		if err := containerSess.RequestPty("xterm-256color", 24, 80, cryptossh.TerminalModes{
			cryptossh.ECHO:   1,
			cryptossh.ISIG:   1,
			cryptossh.ICANON: 1,
			cryptossh.OPOST:  1,
			cryptossh.ONLCR:  1,
		}); err != nil {
			fmt.Fprintf(sess, "Failed to request PTY: %v\r\n", err)
			sess.Exit(1)
			return
		}
	}

	// Get pipes
	containerStdin, err := containerSess.StdinPipe()
	if err != nil {
		fmt.Fprintf(sess, "Failed to pipe stdin: %v\r\n", err)
		sess.Exit(1)
		return
	}

	containerStdout, err := containerSess.StdoutPipe()
	if err != nil {
		fmt.Fprintf(sess, "Failed to pipe stdout: %v\r\n", err)
		sess.Exit(1)
		return
	}

	containerStderr, err := containerSess.StderrPipe()
	if err != nil {
		fmt.Fprintf(sess, "Failed to pipe stderr: %v\r\n", err)
		sess.Exit(1)
		return
	}

	// Start shell
	if err := containerSess.Shell(); err != nil {
		fmt.Fprintf(sess, "Failed to start shell: %v\r\n", err)
		sess.Exit(1)
		return
	}

	var wg sync.WaitGroup

	// Forward client -> container (stdin)
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

	// Forward window-change events
	if isPty {
		go func() {
			for win := range winCh {
				containerSess.WindowChange(win.Height, win.Width)
			}
		}()
	}

	// Forward signals from client to container
	sigCh := make(chan gssh.Signal, 3)
	sess.Signals(sigCh)
	go func() {
		for sig := range sigCh {
			containerSess.Signal(cryptossh.Signal(sig))
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

	sess.Exit(0)
}
