package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"aurasql/core"
)
// TableMetadata holds everything the system needs to know about a single table.
type TableMetadata struct {
	Name     string
	Schema   core.Schema
	HeapFile *HeapFile
}

// Catalog tracks all tables currently existing in the database.
type Catalog struct {
	tables map[string]*TableMetadata
}

// NewCatalog creates a fresh, empty in-memory catalog.
func NewCatalog() *Catalog {
	return &Catalog{
		tables: make(map[string]*TableMetadata),
	}
}

// AddTable registers a new table with the catalog.
func (c *Catalog) AddTable(name string, schema core.Schema, hf *HeapFile) error {
	if _, exists := c.tables[name]; exists {
		return fmt.Errorf("table %q already exists", name)
	}

	c.tables[name] = &TableMetadata{
		Name:     name,
		Schema:   schema,
		HeapFile: hf,
	}
	return nil
}

// GetTable retrieves a table's metadata by its name.
func (c *Catalog) GetTable(name string) (*TableMetadata, error) {
	meta, exists := c.tables[name]
	if !exists {
		return nil, fmt.Errorf("table %q does not exist", name)
	}
	return meta, nil
}

// DropTable removes a table from the catalog.
// Note: This just removes it from the map; the physical file deletion happens in the Engine.
func (c *Catalog) DropTable(name string) error {
	if _, exists := c.tables[name]; !exists {
		return fmt.Errorf("table %q does not exist", name)
	}
	delete(c.tables, name)
	return nil
}

// diskMetadata is a simplified struct just for JSON serialization
type diskMetadata struct {
	Name   string      `json:"name"`
	Schema core.Schema `json:"schema"`
}

// Save writes the current catalog state to a JSON file.
func (c *Catalog) Save(dataDir string) error {
	path := filepath.Join(dataDir, "catalog.json")

	// Extract just the data we need to save
	var serializable []diskMetadata
	for _, meta := range c.tables {
		serializable = append(serializable, diskMetadata{
			Name:   meta.Name,
			Schema: meta.Schema,
		})
	}

	// Convert to JSON
	data, err := json.MarshalIndent(serializable, "", "  ")
	if err != nil {
		return err
	}

	// Write to disk
	return os.WriteFile(path, data, 0666)
}

// Load reads the JSON file and reconstructs the catalog map.
func (c *Catalog) Load(dataDir string) error {
	path := filepath.Join(dataDir, "catalog.json")

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // It's a brand new database, nothing to load yet
		}
		return err
	}

	var savedData []diskMetadata
	if err := json.Unmarshal(data, &savedData); err != nil {
		return err
	}

	for _, item := range savedData {
		// Re-open the physical file for this table
		tablePath := filepath.Join(dataDir, item.Name+".db")
		hf, err := NewHeapFile(tablePath)
		if err != nil {
			return err
		}

		// Reattach the buffer pool
		pool := NewBufferPool(hf, 100)
		hf.SetBufferPool(pool)

		// Put it back in the memory map
		c.tables[item.Name] = &TableMetadata{
			Name:     item.Name,
			Schema:   item.Schema,
			HeapFile: hf,
		}
	}

	return nil
}
