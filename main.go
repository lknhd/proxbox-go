package main

import (
	"log"
	"os"
	"strings"

	"github.com/lknhd/proxbox-go/internal/config"
	"github.com/lknhd/proxbox-go/internal/container"
	"github.com/lknhd/proxbox-go/internal/db"
	"github.com/lknhd/proxbox-go/internal/gateway"
	"github.com/lknhd/proxbox-go/internal/proxmox"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	cfg, err := config.Load("")
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// Read gateway public key
	gatewayPubKey, err := os.ReadFile(cfg.Gateway.GatewayKeyPath + ".pub")
	if err != nil {
		log.Fatalf("Failed to read gateway public key: %v", err)
	}

	// Open database
	database, err := db.Open(cfg.Gateway.DBPath)
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}
	defer database.Close()

	// Initialize components
	pxClient := proxmox.NewClient(cfg.Proxmox)

	mgr := container.NewManager(database, pxClient, cfg.Proxmox)
	mgr.SetGatewayPublicKey(strings.TrimSpace(string(gatewayPubKey)))

	proxy, err := gateway.NewProxy(mgr, cfg.Gateway.GatewayKeyPath)
	if err != nil {
		log.Fatalf("Failed to create SSH proxy: %v", err)
	}

	handler := gateway.NewHandler(mgr, proxy)

	server, err := gateway.NewServer(cfg.Gateway, handler, database)
	if err != nil {
		log.Fatalf("Failed to create gateway server: %v", err)
	}

	log.Printf("Proxbox is ready.")
	log.Printf("Connect with: ssh -p %d <this-host> help", cfg.Gateway.Port)

	if err := server.ListenAndServe(); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}
