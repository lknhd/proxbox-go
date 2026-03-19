package container

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/lknhd/proxbox-go/internal/config"
	"github.com/lknhd/proxbox-go/internal/db"
	"github.com/lknhd/proxbox-go/internal/models"
	"github.com/lknhd/proxbox-go/internal/proxmox"
)

type Manager struct {
	db               *db.DB
	proxmox          *proxmox.Client
	config           config.ProxmoxConfig
	gatewayPublicKey string
}

func NewManager(database *db.DB, pxClient *proxmox.Client, cfg config.ProxmoxConfig) *Manager {
	return &Manager{
		db:      database,
		proxmox: pxClient,
		config:  cfg,
	}
}

func (m *Manager) SetGatewayPublicKey(key string) {
	m.gatewayPublicKey = key
}

func (m *Manager) Create(user *models.User, name, sizeName string) (*models.Container, error) {
	if sizeName == "" {
		sizeName = "small"
	}

	size, ok := models.Sizes[sizeName]
	if !ok {
		names := make([]string, 0, len(models.Sizes))
		for k := range models.Sizes {
			names = append(names, k)
		}
		return nil, fmt.Errorf("invalid size '%s'. Choose: %s", sizeName, strings.Join(names, ", "))
	}

	existing, err := m.db.GetContainer(user.ID, name)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return nil, fmt.Errorf("container '%s' already exists", name)
	}

	vmid, err := m.db.NextAvailableVMID(m.config.VMIDStart, m.config.VMIDEnd)
	if err != nil {
		return nil, err
	}

	hostname := fmt.Sprintf("proxbox-%s-%s", user.Username, name)

	sshKeys := user.PublicKey
	if m.gatewayPublicKey != "" {
		sshKeys = user.PublicKey + "\n" + m.gatewayPublicKey
	}

	if err := m.proxmox.CreateContainer(vmid, hostname, size, sshKeys); err != nil {
		return nil, err
	}

	return m.db.CreateContainer(user.ID, name, vmid, sizeName)
}

func (m *Manager) Start(user *models.User, name string) (*models.Container, error) {
	ct, err := m.getContainer(user, name)
	if err != nil {
		return nil, err
	}
	if ct.Status == "running" {
		return nil, fmt.Errorf("container '%s' is already running", name)
	}

	if err := m.proxmox.StartContainer(ct.VMID); err != nil {
		return nil, err
	}

	ip := m.proxmox.WaitForIP(ct.VMID, 60*time.Second)
	snap := false
	if err := m.db.UpdateContainer(ct.ID, "running", ip, &snap); err != nil {
		return nil, err
	}

	ct.Status = "running"
	ct.IPAddress = ip
	return ct, nil
}

func (m *Manager) Stop(user *models.User, name string) (*models.Container, error) {
	ct, err := m.getContainer(user, name)
	if err != nil {
		return nil, err
	}
	if ct.Status == "stopped" {
		return nil, fmt.Errorf("container '%s' is already stopped", name)
	}

	if err := m.proxmox.StopContainer(ct.VMID); err != nil {
		return nil, err
	}

	if err := m.db.UpdateContainer(ct.ID, "stopped", "", nil); err != nil {
		return nil, err
	}

	ct.Status = "stopped"
	ct.IPAddress = ""
	return ct, nil
}

func (m *Manager) Pause(user *models.User, name string) (*models.Container, error) {
	ct, err := m.getContainer(user, name)
	if err != nil {
		return nil, err
	}
	if ct.Status != "running" {
		return ct, nil
	}

	log.Printf("Pausing container '%s' (VMID %d)", name, ct.VMID)

	if ct.HasSnapshot {
		_ = m.proxmox.DeleteSnapshot(ct.VMID)
	}

	if err := m.proxmox.CreateSnapshot(ct.VMID); err != nil {
		return nil, err
	}
	if err := m.proxmox.StopContainer(ct.VMID); err != nil {
		return nil, err
	}

	snap := true
	if err := m.db.UpdateContainer(ct.ID, "paused", "", &snap); err != nil {
		return nil, err
	}

	ct.Status = "paused"
	ct.IPAddress = ""
	ct.HasSnapshot = true
	return ct, nil
}

func (m *Manager) Resume(user *models.User, name string) (*models.Container, error) {
	ct, err := m.getContainer(user, name)
	if err != nil {
		return nil, err
	}
	if ct.Status != "paused" {
		return nil, fmt.Errorf("container '%s' is not paused", name)
	}

	log.Printf("Resuming container '%s' (VMID %d)", name, ct.VMID)

	if ct.HasSnapshot {
		if err := m.proxmox.RollbackSnapshot(ct.VMID); err != nil {
			return nil, err
		}
		_ = m.proxmox.DeleteSnapshot(ct.VMID)
	}

	if err := m.proxmox.StartContainer(ct.VMID); err != nil {
		return nil, err
	}

	ip := m.proxmox.WaitForIP(ct.VMID, 60*time.Second)
	snap := false
	if err := m.db.UpdateContainer(ct.ID, "running", ip, &snap); err != nil {
		return nil, err
	}

	ct.Status = "running"
	ct.IPAddress = ip
	ct.HasSnapshot = false
	return ct, nil
}

func (m *Manager) Destroy(user *models.User, name string) error {
	ct, err := m.getContainer(user, name)
	if err != nil {
		return err
	}

	if ct.Status == "running" {
		if err := m.proxmox.StopContainer(ct.VMID); err != nil {
			return err
		}
	}

	if ct.HasSnapshot {
		_ = m.proxmox.DeleteSnapshot(ct.VMID)
	}

	if err := m.proxmox.DestroyContainer(ct.VMID); err != nil {
		return err
	}

	return m.db.DeleteContainer(ct.ID)
}

func (m *Manager) List(user *models.User) ([]*models.Container, error) {
	return m.db.GetContainersForUser(user.ID)
}

func (m *Manager) Get(user *models.User, name string) (*models.Container, error) {
	return m.db.GetContainer(user.ID, name)
}

func (m *Manager) getContainer(user *models.User, name string) (*models.Container, error) {
	ct, err := m.db.GetContainer(user.ID, name)
	if err != nil {
		return nil, err
	}
	if ct == nil {
		return nil, fmt.Errorf("container '%s' not found", name)
	}
	return ct, nil
}
