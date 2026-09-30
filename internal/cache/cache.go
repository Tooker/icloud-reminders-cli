// Package cache manages the local JSON cache for iCloud Reminders.
package cache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"icloud-reminders/internal/storage"
)

// ConfigDir is the default config/session directory.
var ConfigDir = filepath.Join(os.Getenv("HOME"), ".config", "icloud-reminders")

// CacheFile is the path to the reminders JSON cache file.
var CacheFile = filepath.Join(ConfigDir, "ck_cache.json")

// SessionFile is the path to the auth session JSON file.
var SessionFile = filepath.Join(ConfigDir, "session.json")

// ReminderData holds raw cached data for a single reminder.
type ReminderData struct {
	Title          string  `json:"title"`
	Completed      bool    `json:"completed"`
	CompletionDate *string `json:"completion_date,omitempty"`
	Due            *string `json:"due,omitempty"`
	Priority       int     `json:"priority"`
	Notes          *string `json:"notes,omitempty"`
	ListRef        *string `json:"list_ref,omitempty"`
	ParentRef      *string `json:"parent_ref,omitempty"`
	ModifiedTS     *int64  `json:"modified_ts,omitempty"`
	ChangeTag      *string `json:"change_tag,omitempty"`
}

// Cache holds the local cache of reminders and lists.
type Cache struct {
	directory string
	Reminders map[string]*ReminderData `json:"reminders"`
	Lists     map[string]string        `json:"lists"`
	SyncToken *string                  `json:"sync_token,omitempty"`
	OwnerID   *string                  `json:"owner_id,omitempty"`
	UpdatedAt *string                  `json:"updated_at,omitempty"`
}

// NewCache returns an empty Cache.
func NewCache() *Cache {
	return &Cache{
		Reminders: make(map[string]*ReminderData),
		Lists:     make(map[string]string),
	}
}

// Load loads the cache from disk; returns empty cache on error.
func Load() *Cache {
	return LoadFrom(ConfigDir)
}

// LoadFrom loads an account's cache from its configured data directory.
func LoadFrom(directory string) *Cache {
	c := NewCache()
	c.directory = directory
	data, err := os.ReadFile(filepath.Join(directory, "ck_cache.json"))
	if err != nil {
		return c
	}
	if err := json.Unmarshal(data, c); err != nil {
		c = NewCache()
		c.directory = directory
		return c
	}
	if c.Reminders == nil {
		c.Reminders = make(map[string]*ReminderData)
	}
	if c.Lists == nil {
		c.Lists = make(map[string]string)
	}
	return c
}

// Save writes the cache to disk.
func (c *Cache) Save() error {
	directory := c.directory
	if directory == "" {
		directory = ConfigDir
	}
	now := time.Now().Format("2006-01-02T15:04:05")
	c.UpdatedAt = &now
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return storage.WritePrivate(filepath.Join(directory, "ck_cache.json"), data)
}

// Reset clears sync state while retaining the account's storage location.
func (c *Cache) Reset() *Cache {
	fresh := NewCache()
	fresh.directory = c.directory
	return fresh
}

// SetDirectory configures storage for a CLI invocation before any operation.
func SetDirectory(directory string) {
	ConfigDir = directory
	CacheFile = filepath.Join(directory, "ck_cache.json")
	SessionFile = filepath.Join(directory, "session.json")
}
