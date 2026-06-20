package storage

import (
	"encoding/binary"
	"fmt"

	"aurasql/core"
)

const (
	NodeTypeInternal uint16 = 0
	NodeTypeLeaf     uint16 = 1
)

// BTreeNode represents an in-memory view of a 4KB B+-Tree page.
type BTreeNode struct {
	PageID   int    // The physical page number where this node lives
	NodeType uint16 // NodeTypeInternal or NodeTypeLeaf
	NumKeys  uint16 // How many keys are currently in this node
	NextLeaf int    // Right sibling pointer for range scans (Leaf only. -1 means none)

	// In a B+-Tree, keys and pointers are kept in sorted order.
	Keys     []core.Value
	Pointers []uint64
}

// NewLeafNode creates a fresh, empty leaf node in memory.
func NewLeafNode(pageID int) *BTreeNode {
	return &BTreeNode{
		PageID:   pageID,
		NodeType: NodeTypeLeaf,
		NumKeys:  0,
		NextLeaf: -1, // -1 indicates no right sibling yet
		Keys:     make([]core.Value, 0),
		Pointers: make([]uint64, 0),
	}
}

// NewInternalNode creates a fresh, empty internal routing node.
func NewInternalNode(pageID int) *BTreeNode {
	return &BTreeNode{
		PageID:   pageID,
		NodeType: NodeTypeInternal,
		NumKeys:  0,
		NextLeaf: -1,
		Keys:     make([]core.Value, 0),
		Pointers: make([]uint64, 0),
	}
}

// fetchNode is a helper that grabs a 4KB page from the buffer pool
// and unpacks it into our BTreeNode struct.
func (e *Engine) fetchNode(pageID int) (*BTreeNode, error) {
	page, err := e.indexPool.FetchPage(pageID)
	if err != nil {
		return nil, err
	}

	data := page.Data[:]

	nodeType := binary.LittleEndian.Uint16(data[0:2])
	numKeys := binary.LittleEndian.Uint16(data[2:4])
	nextLeaf := int32(binary.LittleEndian.Uint32(data[4:8]))

	node := &BTreeNode{
		PageID:   pageID,
		NodeType: nodeType,
		NumKeys:  numKeys,
		NextLeaf: int(nextLeaf),
		Keys:     make([]core.Value, numKeys),
		Pointers: make([]uint64, numKeys),
	}

	offset := 12
	for i := 0; i < int(numKeys); i++ {
		node.Pointers[i] = binary.LittleEndian.Uint64(data[offset : offset+8])
		offset += 8

		keyVal := int64(binary.LittleEndian.Uint64(data[offset : offset+8]))
		node.Keys[i] = core.NewInt(keyVal)
		offset += 8
	}

	if nodeType == NodeTypeInternal {
		extraPointer := binary.LittleEndian.Uint64(data[offset : offset+8])
		node.Pointers = append(node.Pointers, extraPointer)
	}

	return node, nil
}

// writeNode packs an in-memory BTreeNode back into a 4KB page and marks it dirty.
func (e *Engine) writeNode(node *BTreeNode) error {
	page, err := e.indexPool.FetchPage(node.PageID)
	if err != nil {
		return err
	}

	data := page.Data[:]

	binary.LittleEndian.PutUint16(data[0:2], node.NodeType)
	binary.LittleEndian.PutUint16(data[2:4], node.NumKeys)
	binary.LittleEndian.PutUint32(data[4:8], uint32(node.NextLeaf))

	offset := 12
	for i := 0; i < int(node.NumKeys); i++ {
		binary.LittleEndian.PutUint64(data[offset:offset+8], node.Pointers[i])
		offset += 8

		binary.LittleEndian.PutUint64(data[offset:offset+8], uint64(node.Keys[i].Int))
		offset += 8
	}

	if node.NodeType == NodeTypeInternal {
		binary.LittleEndian.PutUint64(data[offset:offset+8], node.Pointers[node.NumKeys])
	}

	// FIX 2: Mark this index page dirty so FlushAll() persists it to disk on Close.
	// Without this, modified index pages live only in the buffer pool and are lost
	// on restart, since FlushAll only writes pages that are in the dirty set.
	e.indexPool.MarkDirty(node.PageID)

	return nil
}

// findLeaf traverses from the given root down to the leaf node that
// contains (or should contain) the target key.
func (e *Engine) findLeaf(rootPageID int, key core.Value) (*BTreeNode, error) {
	currPageID := rootPageID

	for {
		node, err := e.fetchNode(currPageID)
		if err != nil {
			return nil, err
		}

		if node.NodeType == NodeTypeLeaf {
			return node, nil
		}

		childIdx := 0
		for childIdx < int(node.NumKeys) {
			cmp, err := key.Compare(node.Keys[childIdx])
			if err != nil {
				return nil, err
			}

			if cmp < 0 {
				break
			}
			childIdx++
		}
		currPageID = int(node.Pointers[childIdx])
	}
}

// SeekIndex implements the Phase 3 contract. It finds the starting leaf
// for a given key and returns an iterator that scans horizontally.
func (e *Engine) SeekIndex(txn core.Txn, table, column string, key core.Value) (core.RowIterator, error) {
	rootPageID, err := e.getIndexRootPage(table, column)
	if err != nil {
		return nil, err
	}

	leafNode, err := e.findLeaf(rootPageID, key)
	if err != nil {
		return nil, err
	}

	startIdx := 0
	for startIdx < int(leafNode.NumKeys) {
		cmp, _ := leafNode.Keys[startIdx].Compare(key)
		if cmp >= 0 {
			break
		}
		startIdx++
	}

	return &btreeIterator{
		engine:   e,
		table:    table,
		seekKey:  key,
		currNode: leafNode,
		currIdx:  startIdx,
	}, nil
}

// btreeIterator scans horizontally across leaf nodes.
type btreeIterator struct {
	engine   *Engine
	table    string
	seekKey  core.Value
	currNode *BTreeNode
	currIdx  int
}

func (it *btreeIterator) Next() (core.RowID, core.Row, bool, error) {
	for {
		if it.currNode == nil {
			return 0, core.Row{}, false, nil
		}

		if it.currIdx >= int(it.currNode.NumKeys) {
			if it.currNode.NextLeaf == -1 {
				return 0, core.Row{}, false, nil
			}

			nextNode, err := it.engine.fetchNode(it.currNode.NextLeaf)
			if err != nil {
				return 0, core.Row{}, false, err
			}
			it.currNode = nextNode
			it.currIdx = 0
			continue
		}

		val := it.currNode.Keys[it.currIdx]
		cmp, err := val.Compare(it.seekKey)
		if err != nil {
			return 0, core.Row{}, false, err
		}

		if cmp > 0 {
			return 0, core.Row{}, false, nil
		}

		if cmp == 0 {
			rowID := core.RowID(it.currNode.Pointers[it.currIdx])
			it.currIdx++

			row, err := it.engine.fetchRowByID(it.table, rowID)
			if err != nil {
				return 0, core.Row{}, false, err
			}

			return rowID, row, true, nil
		}

		it.currIdx++
	}
}

func (it *btreeIterator) Close() error {
	return nil
}

// --- Helpers ---

// fetchRowByID decodes a RowID into a Page and Slot, then grabs that exact row from the HeapFile.
func (e *Engine) fetchRowByID(table string, id core.RowID) (core.Row, error) {
	meta, err := e.catalog.GetTable(table)
	if err != nil {
		return core.Row{}, fmt.Errorf("table %s not found", table)
	}

	// Use the same decoding as decodeRowID from heapfile.go
	pageID, slotID := decodeRowID(id)

	page, err := meta.HeapFile.pool.FetchPage(pageID)
	if err != nil {
		return core.Row{}, err
	}

	slotOffset := 4 + (slotID * 4)

	// Check if the page is large enough
	if len(page.Data) < slotOffset+4 {
		return core.Row{}, fmt.Errorf("slot %d out of bounds", slotID)
	}

	dataOffset := int(page.Data[slotOffset]) | int(page.Data[slotOffset+1])<<8
	length := int(page.Data[slotOffset+2]) | int(page.Data[slotOffset+3])<<8

	if length == 0 {
		return core.Row{}, fmt.Errorf("row %d has been deleted", id)
	}

	// Check bounds
	if dataOffset+length > len(page.Data) {
		return core.Row{}, fmt.Errorf("row data out of bounds")
	}

	rawRow := page.Data[dataOffset : dataOffset+length]

	return Deserialize(rawRow), nil
}

// getIndexRootPage checks the catalog to find the starting page of our B-Tree.
func (e *Engine) getIndexRootPage(table, column string) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	meta, err := e.catalog.GetTable(table)
	if err != nil {
		return 0, err
	}

	indexKey := column
	rootPage, exists := meta.IndexRoots[indexKey]
	if !exists {
		return 0, fmt.Errorf("index on %s.%s does not exist", table, column)
	}

	return rootPage, nil
}
