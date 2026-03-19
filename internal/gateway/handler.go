package gateway

import (
	"fmt"
	"strings"

	"github.com/lknhd/proxbox-go/internal/container"
	"github.com/lknhd/proxbox-go/internal/models"

	"golang.org/x/crypto/ssh"
)

const banner = `
 ____  ____   _____  ______   _____  __
|  _ \|  _ \ / _ \ \/ / __ ) / _ \ \/ /
| |_) | |_) | | | \  /|  _ \| | | \  /
|  __/|  _ <| |_| /  \| |_) | |_| /  \
|_|   |_| \_\\___/_/\_\____/ \___/_/\_\

`

const helpText = `COMMANDS:
  create <name> [size]    Create a new container (sizes: small, medium, large)
  list                    List your containers
  start <name>            Start a stopped container
  stop <name>             Stop a running container
  ssh <name>              Connect to a container (auto-starts/resumes)
  destroy <name>          Permanently delete a container
  help                    Show this help message

SIZES:
  small     1 vCPU,  1 GB RAM,  8 GB disk  (default)
  medium    2 vCPU,  4 GB RAM,  8 GB disk
  large     4 vCPU,  8 GB RAM,  8 GB disk

EXAMPLES:
  ssh proxbox create dev
  ssh proxbox create big-box large
  ssh proxbox ssh dev
  ssh proxbox list
  ssh proxbox destroy dev
`

type Handler struct {
	manager *container.Manager
	proxy   *Proxy
}

func NewHandler(manager *container.Manager, proxy *Proxy) *Handler {
	return &Handler{manager: manager, proxy: proxy}
}

func (h *Handler) Handle(sess ssh.Channel, reqs <-chan *ssh.Request, user *models.User, command string, ptyReq *ptyRequest) {
	if command == "" {
		fmt.Fprint(sess, banner+helpText)
		sess.Close()
		return
	}

	parts := strings.Fields(command)
	cmd := parts[0]
	args := parts[1:]

	var err error
	switch cmd {
	case "help":
		fmt.Fprint(sess, banner+helpText)
	case "create":
		err = h.handleCreate(sess, user, args)
	case "list", "ls":
		err = h.handleList(sess, user)
	case "start":
		err = h.handleStart(sess, user, args)
	case "stop":
		err = h.handleStop(sess, user, args)
	case "ssh", "connect":
		h.handleSSH(sess, reqs, user, args, ptyReq)
		return
	case "destroy", "rm":
		err = h.handleDestroy(sess, user, args)
	default:
		fmt.Fprintf(sess, "Error: Unknown command: %s\n", cmd)
		fmt.Fprint(sess, "Run 'help' to see available commands.\n")
	}

	if err != nil {
		fmt.Fprintf(sess, "Error: %v\n", err)
	}

	sess.Close()
}

func (h *Handler) handleCreate(sess ssh.Channel, user *models.User, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("Usage: create <name> [small|medium|large]")
	}

	name := args[0]
	size := "small"
	if len(args) > 1 {
		size = args[1]
	}

	fmt.Fprintf(sess, "Creating container '%s' (size: %s)...\n", name, size)
	ct, err := h.manager.Create(user, name, size)
	if err != nil {
		return err
	}

	fmt.Fprintf(sess, "Container '%s' created (VMID: %d)\n", name, ct.VMID)
	fmt.Fprintf(sess, "Connect with: ssh proxbox ssh %s\n", name)
	return nil
}

func (h *Handler) handleList(sess ssh.Channel, user *models.User) error {
	containers, err := h.manager.List(user)
	if err != nil {
		return err
	}

	if len(containers) == 0 {
		fmt.Fprint(sess, "No containers. Create one with: create <name>\n")
		return nil
	}

	fmt.Fprintf(sess, "%-16s %-8s %-10s %-16s\n", "NAME", "SIZE", "STATUS", "IP ADDRESS")
	fmt.Fprintf(sess, "%-16s %-8s %-10s %-16s\n",
		strings.Repeat("─", 15),
		strings.Repeat("─", 7),
		strings.Repeat("─", 9),
		strings.Repeat("─", 15),
	)

	for _, c := range containers {
		ip := c.IPAddress
		if ip == "" {
			ip = "-"
		}
		fmt.Fprintf(sess, "%-16s %-8s %-10s %-16s\n", c.Name, c.Size, c.Status, ip)
	}

	return nil
}

func (h *Handler) handleStart(sess ssh.Channel, user *models.User, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("Usage: start <name>")
	}

	name := args[0]
	fmt.Fprintf(sess, "Starting '%s'...\n", name)
	ct, err := h.manager.Start(user, name)
	if err != nil {
		return err
	}

	ip := ct.IPAddress
	if ip == "" {
		ip = "pending"
	}
	fmt.Fprintf(sess, "Container '%s' started (IP: %s)\n", name, ip)
	return nil
}

func (h *Handler) handleStop(sess ssh.Channel, user *models.User, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("Usage: stop <name>")
	}

	name := args[0]
	fmt.Fprintf(sess, "Stopping '%s'...\n", name)
	if _, err := h.manager.Stop(user, name); err != nil {
		return err
	}

	fmt.Fprintf(sess, "Container '%s' stopped.\n", name)
	return nil
}

func (h *Handler) handleSSH(sess ssh.Channel, reqs <-chan *ssh.Request, user *models.User, args []string, ptyReq *ptyRequest) {
	if len(args) == 0 {
		fmt.Fprint(sess, "Error: Usage: ssh <name>\n")
		sess.Close()
		return
	}

	name := args[0]
	ct, err := h.manager.Get(user, name)
	if err != nil {
		fmt.Fprintf(sess, "Error: %v\n", err)
		sess.Close()
		return
	}
	if ct == nil {
		fmt.Fprintf(sess, "Error: Container '%s' not found\n", name)
		sess.Close()
		return
	}

	// Proxy handles the interactive session and closes the channel
	h.proxy.Connect(sess, reqs, user, ct, ptyReq)
	sess.Close()
}

func (h *Handler) handleDestroy(sess ssh.Channel, user *models.User, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("Usage: destroy <name>")
	}

	name := args[0]
	fmt.Fprintf(sess, "Destroying '%s'...\n", name)
	if err := h.manager.Destroy(user, name); err != nil {
		return err
	}

	fmt.Fprintf(sess, "Container '%s' destroyed.\n", name)
	return nil
}
