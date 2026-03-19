package models

import "time"

type User struct {
	ID          int64
	Username    string
	Fingerprint string
	PublicKey   string
	CreatedAt   time.Time
}

type Container struct {
	ID          int64
	UserID      int64
	Name        string
	VMID        int
	Size        string
	Status      string
	IPAddress   string
	HasSnapshot bool
	CreatedAt   time.Time
}

type ContainerSize struct {
	Name     string
	Cores    int
	MemoryMB int
	DiskGB   int
}

var Sizes = map[string]ContainerSize{
	"small":  {Name: "small", Cores: 1, MemoryMB: 1024, DiskGB: 8},
	"medium": {Name: "medium", Cores: 2, MemoryMB: 4096, DiskGB: 8},
	"large":  {Name: "large", Cores: 4, MemoryMB: 8192, DiskGB: 8},
}
