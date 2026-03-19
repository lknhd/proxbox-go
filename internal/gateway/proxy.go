package gateway

import (
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/creack/pty"
	"github.com/lknhd/proxbox-go/internal/container"
	"github.com/lknhd/proxbox-go/internal/models"

	gssh "github.com/gliderlabs/ssh"
)

type Proxy struct {
	manager        *container.Manager
	gatewayKeyPath string
}

func NewProxy(manager *container.Manager, gatewayKeyPath string) (*Proxy, error) {
	if _, err := os.Stat(gatewayKeyPath); err != nil {
		return nil, fmt.Errorf("gateway key not found: %w", err)
	}

	return &Proxy{
		manager:        manager,
		gatewayKeyPath: gatewayKeyPath,
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

	// Wait for container SSH to be ready
	if err := p.waitForSSH(ct.IPAddress, 10); err != nil {
		fmt.Fprintf(sess, "Failed to connect to container: %v\r\n", err)
		sess.Exit(1)
		return
	}

	// Build native ssh command
	cmd := exec.Command("ssh",
		"-tt",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		"-o", "ConnectTimeout=10",
		"-i", p.gatewayKeyPath,
		fmt.Sprintf("root@%s", ct.IPAddress),
	)

	// Get initial PTY size from client
	ptyReq, winCh, isPty := sess.Pty()

	var initialSize *pty.Winsize
	if isPty {
		initialSize = &pty.Winsize{
			Cols: uint16(ptyReq.Window.Width),
			Rows: uint16(ptyReq.Window.Height),
		}
	} else {
		initialSize = &pty.Winsize{Cols: 80, Rows: 24}
	}

	// Start command with PTY
	ptmx, err := pty.StartWithSize(cmd, initialSize)
	if err != nil {
		fmt.Fprintf(sess, "Failed to start SSH: %v\r\n", err)
		sess.Exit(1)
		return
	}
	defer ptmx.Close()

	var wg sync.WaitGroup

	// Forward client -> PTY (stdin)
	wg.Add(1)
	go func() {
		defer wg.Done()
		io.Copy(ptmx, sess)
	}()

	// Forward PTY -> client (stdout)
	wg.Add(1)
	go func() {
		defer wg.Done()
		io.Copy(sess, ptmx)
	}()

	// Forward window-change events
	if isPty {
		go func() {
			for win := range winCh {
				pty.Setsize(ptmx, &pty.Winsize{
					Cols: uint16(win.Width),
					Rows: uint16(win.Height),
				})
			}
		}()
	}

	// Wait for SSH command to finish
	cmd.Wait()
	wg.Wait()

	// Pause on disconnect
	if _, err := p.manager.Pause(user, ct.Name); err != nil {
		log.Printf("Failed to pause container '%s': %v", ct.Name, err)
	} else {
		log.Printf("Container '%s' paused on disconnect", ct.Name)
	}

	sess.Exit(0)
}

// waitForSSH polls the container's SSH port until it's reachable.
func (p *Proxy) waitForSSH(ip string, maxAttempts int) error {
	for i := 0; i < maxAttempts; i++ {
		cmd := exec.Command("ssh",
			"-o", "StrictHostKeyChecking=no",
			"-o", "UserKnownHostsFile=/dev/null",
			"-o", "LogLevel=ERROR",
			"-o", "ConnectTimeout=2",
			"-i", p.gatewayKeyPath,
			fmt.Sprintf("root@%s", ip),
			"true",
		)
		if err := cmd.Run(); err == nil {
			return nil
		}
		if i < maxAttempts-1 {
			time.Sleep(2 * time.Second)
		}
	}
	return fmt.Errorf("SSH not ready after %d attempts", maxAttempts)
}
