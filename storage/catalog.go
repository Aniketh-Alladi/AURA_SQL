package storage

import (
	"fmt"

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
