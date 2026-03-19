package gateway

import (
	"encoding/binary"
	"fmt"
	"log"
	"net"
	"os"

	"github.com/lknhd/proxbox-go/internal/config"
	"github.com/lknhd/proxbox-go/internal/db"
	"github.com/lknhd/proxbox-go/internal/models"

	"golang.org/x/crypto/ssh"
)

type ptyRequest struct {
	Term  string
	Cols  int
	Rows  int
	Modes ssh.TerminalModes
}

type Server struct {
	config  config.GatewayConfig
	handler *Handler
	db      *db.DB
	sshCfg  *ssh.ServerConfig
}

func NewServer(cfg config.GatewayConfig, handler *Handler, database *db.DB) (*Server, error) {
	hostKeyData, err := os.ReadFile(cfg.HostKeyPath)
	if err != nil {
		return nil, fmt.Errorf("read host key: %w", err)
	}

	hostKey, err := ssh.ParsePrivateKey(hostKeyData)
	if err != nil {
		return nil, fmt.Errorf("parse host key: %w", err)
	}

	s := &Server{
		config:  cfg,
		handler: handler,
		db:      database,
	}

	s.sshCfg = &ssh.ServerConfig{
		PublicKeyCallback: s.publicKeyCallback,
	}
	s.sshCfg.AddHostKey(hostKey)

	return s, nil
}

func (s *Server) publicKeyCallback(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
	fingerprint := ssh.FingerprintSHA256(key)
	pubKeyStr := string(ssh.MarshalAuthorizedKey(key))
	username := conn.User()

	user, err := s.db.GetOrCreateUser(username, fingerprint, pubKeyStr)
	if err != nil {
		return nil, fmt.Errorf("identify user: %w", err)
	}

	log.Printf("Authenticated user %s (%s...)", user.Username, fingerprint[:20])

	return &ssh.Permissions{
		Extensions: map[string]string{
			"user_id":     fmt.Sprintf("%d", user.ID),
			"username":    user.Username,
			"fingerprint": user.Fingerprint,
			"public_key":  user.PublicKey,
		},
	}, nil
}

func (s *Server) getUserFromPermissions(perms *ssh.Permissions) *models.User {
	var id int64
	fmt.Sscanf(perms.Extensions["user_id"], "%d", &id)
	return &models.User{
		ID:          id,
		Username:    perms.Extensions["username"],
		Fingerprint: perms.Extensions["fingerprint"],
		PublicKey:   perms.Extensions["public_key"],
	}
}

func (s *Server) ListenAndServe() error {
	addr := fmt.Sprintf("%s:%d", s.config.Host, s.config.Port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer listener.Close()

	log.Printf("Proxbox gateway listening on port %d", s.config.Port)

	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Printf("Accept error: %v", err)
			continue
		}
		go s.handleConnection(conn)
	}
}

func (s *Server) handleConnection(conn net.Conn) {
	defer conn.Close()

	sshConn, chans, reqs, err := ssh.NewServerConn(conn, s.sshCfg)
	if err != nil {
		log.Printf("SSH handshake failed: %v", err)
		return
	}
	defer sshConn.Close()

	user := s.getUserFromPermissions(sshConn.Permissions)
	log.Printf("SSH connection from %s (user: %s)", conn.RemoteAddr(), user.Username)

	go ssh.DiscardRequests(reqs)

	for newChan := range chans {
		if newChan.ChannelType() != "session" {
			newChan.Reject(ssh.UnknownChannelType, "unknown channel type")
			continue
		}

		channel, chanReqs, err := newChan.Accept()
		if err != nil {
			log.Printf("Channel accept error: %v", err)
			continue
		}

		go s.handleSession(channel, chanReqs, user)
	}
}

func (s *Server) handleSession(channel ssh.Channel, reqs <-chan *ssh.Request, user *models.User) {
	var command string
	var pty *ptyRequest

	// Buffer requests that come before exec/shell
	var bufferedReqs []*ssh.Request

	// Collect pty-req and exec/shell requests
	for req := range reqs {
		switch req.Type {
		case "pty-req":
			pty = parsePTYRequest(req.Payload)
			if req.WantReply {
				req.Reply(true, nil)
			}

		case "exec":
			if len(req.Payload) >= 4 {
				cmdLen := binary.BigEndian.Uint32(req.Payload[:4])
				if len(req.Payload) >= int(4+cmdLen) {
					command = string(req.Payload[4 : 4+cmdLen])
				}
			}
			if req.WantReply {
				req.Reply(true, nil)
			}

			// Create a channel for remaining requests
			remainReqs := make(chan *ssh.Request, len(bufferedReqs)+10)
			for _, r := range bufferedReqs {
				remainReqs <- r
			}
			// Forward remaining requests
			go func() {
				for r := range reqs {
					remainReqs <- r
				}
				close(remainReqs)
			}()

			s.handler.Handle(channel, remainReqs, user, command, pty)
			return

		case "shell":
			if req.WantReply {
				req.Reply(true, nil)
			}

			remainReqs := make(chan *ssh.Request, len(bufferedReqs)+10)
			for _, r := range bufferedReqs {
				remainReqs <- r
			}
			go func() {
				for r := range reqs {
					remainReqs <- r
				}
				close(remainReqs)
			}()

			s.handler.Handle(channel, remainReqs, user, "", pty)
			return

		default:
			bufferedReqs = append(bufferedReqs, req)
			if req.WantReply {
				req.Reply(false, nil)
			}
		}
	}

	channel.Close()
}

func parsePTYRequest(payload []byte) *ptyRequest {
	if len(payload) < 4 {
		return &ptyRequest{Term: "xterm-256color", Cols: 80, Rows: 24, Modes: ssh.TerminalModes{}}
	}

	termLen := binary.BigEndian.Uint32(payload[:4])
	if len(payload) < int(4+termLen+8) {
		return &ptyRequest{Term: "xterm-256color", Cols: 80, Rows: 24, Modes: ssh.TerminalModes{}}
	}

	term := string(payload[4 : 4+termLen])
	offset := 4 + termLen
	cols := int(binary.BigEndian.Uint32(payload[offset : offset+4]))
	rows := int(binary.BigEndian.Uint32(payload[offset+4 : offset+8]))
	offset += 16 // skip cols, rows, pixel_width, pixel_height

	// Parse terminal modes
	modes := ssh.TerminalModes{}
	if int(offset+4) <= len(payload) {
		modesLen := binary.BigEndian.Uint32(payload[offset : offset+4])
		offset += 4
		modesData := payload[offset:]
		if uint32(len(modesData)) >= modesLen {
			modesData = modesData[:modesLen]
		}
		for len(modesData) >= 5 {
			opcode := modesData[0]
			if opcode == 0 || opcode >= 160 { // TTY_OP_END or invalid
				break
			}
			value := binary.BigEndian.Uint32(modesData[1:5])
			modes[opcode] = value
			modesData = modesData[5:]
		}
	}

	return &ptyRequest{Term: term, Cols: cols, Rows: rows, Modes: modes}
}
