package gateway

import (
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/lknhd/proxbox-go/internal/config"
	"github.com/lknhd/proxbox-go/internal/db"
	"github.com/lknhd/proxbox-go/internal/models"

	gssh "github.com/gliderlabs/ssh"
	cryptossh "golang.org/x/crypto/ssh"
)

type contextKey string

const userContextKey contextKey = "user"

type Server struct {
	config config.GatewayConfig
	handler *Handler
	db     *db.DB
	server *gssh.Server
}

func NewServer(cfg config.GatewayConfig, handler *Handler, database *db.DB) (*Server, error) {
	s := &Server{
		config:  cfg,
		handler: handler,
		db:      database,
	}

	s.server = &gssh.Server{
		Addr: fmt.Sprintf("%s:%d", cfg.Host, cfg.Port),
		Handler:          s.sessionHandler,
		PublicKeyHandler: s.publicKeyHandler,
	}

	hostKeyData, err := os.ReadFile(cfg.HostKeyPath)
	if err != nil {
		return nil, fmt.Errorf("read host key: %w", err)
	}

	hostKey, err := cryptossh.ParsePrivateKey(hostKeyData)
	if err != nil {
		return nil, fmt.Errorf("parse host key: %w", err)
	}

	s.server.AddHostKey(hostKey)

	return s, nil
}

func (s *Server) publicKeyHandler(ctx gssh.Context, key gssh.PublicKey) bool {
	fingerprint := cryptossh.FingerprintSHA256(key)
	pubKeyStr := strings.TrimSpace(string(cryptossh.MarshalAuthorizedKey(key)))
	username := ctx.User()

	user, err := s.db.GetOrCreateUser(username, fingerprint, pubKeyStr)
	if err != nil {
		log.Printf("Failed to identify user: %v", err)
		return false
	}

	log.Printf("Authenticated user %s (%s...)", user.Username, fingerprint[:20])
	ctx.SetValue(userContextKey, user)
	return true
}

func (s *Server) sessionHandler(sess gssh.Session) {
	user, ok := sess.Context().Value(userContextKey).(*models.User)
	if !ok {
		fmt.Fprint(sess, "Error: authentication failed\n")
		sess.Exit(1)
		return
	}

	s.handler.Handle(sess, user)
}

func (s *Server) ListenAndServe() error {
	log.Printf("Proxbox gateway listening on port %d", s.config.Port)
	return s.server.ListenAndServe()
}
