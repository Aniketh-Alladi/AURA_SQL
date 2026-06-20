package storage

import (
	"aurasql/core"
	"fmt"
)

// MaxKeys is the maximum number of keys a 4KB page can hold.
// (4096 bytes / 16 bytes per pair = 256. We use 250 to leave room for the header).
const MaxKeys = 250

// InsertIntoIndex is the main entry point for adding a new RowID to an existing index.
func (e *Engine) InsertIntoIndex(table, column string, key core.Value, rowID core.RowID) error {
	rootPageID, err := e.getIndexRootPage(table, column)
	if err != nil {
		return err
	}

	// 1. Walk down the tree to the leaf, but KEEP TRACK of every parent node we pass.
	// We need this "path" history so we know exactly who to kick keys up to if we split.
	leaf, path, err := e.findLeafWithPath(rootPageID, key)
	if err != nil {
		return err
	}

	// 2. Insert the key and RowID directly into the leaf's arrays in sorted order
	insertIntoNodeArrays(leaf, key, uint64(rowID))

	// 3. THE HAPPY PATH: If the leaf isn't full, just save it and we are done!
	if leaf.NumKeys < MaxKeys {
		return e.writeNode(leaf)
	}

	// 4. THE SPLIT CASCADE: The leaf is overflowing. We must split it.
	return e.handleSplitCascade(table, column, leaf, path)
}

// findLeafWithPath does exactly what findLeaf does, but returns the history of parent PageIDs.
func (e *Engine) findLeafWithPath(rootPageID int, key core.Value) (*BTreeNode, []int, error) {
	currPageID := rootPageID
	var path []int // Acts as a Stack

	for {
		node, err := e.fetchNode(currPageID)
		if err != nil {
			return nil, nil, err
		}

		if node.NodeType == NodeTypeLeaf {
			return node, path, nil
		}

		// Record this internal node in our path history before we drop down to the child
		path = append(path, currPageID)

		childIdx := 0
		for childIdx < int(node.NumKeys) {
			cmp, _ := key.Compare(node.Keys[childIdx])
			if cmp < 0 {
				break
			}
			childIdx++
		}
		currPageID = int(node.Pointers[childIdx])
	}
}

// handleSplitCascade walks backward up the path we took, splitting nodes as necessary.
func (e *Engine) handleSplitCascade(table, column string, overflowingNode *BTreeNode, path []int) error {
	var upKey core.Value
	var rightNodePageID int
	var err error

	// We enter this loop with an overflowing node (first a Leaf, then possibly Internal nodes)
	for overflowingNode != nil {
		// Split the node. This returns the key to kick up, and the ID of the newly created right-half page.
		if overflowingNode.NodeType == NodeTypeLeaf {
			upKey, rightNodePageID, err = e.splitLeafNode(overflowingNode)
		} else {
			upKey, rightNodePageID, err = e.splitInternalNode(overflowingNode)
		}
		if err != nil {
			return err
		}

		// Pop the parent off our path stack
		if len(path) == 0 {
			// WE REACHED THE ROOT AND IT SPLIT!
			// We must create a brand new Root node to sit above the two halves.
			return e.createNewRoot(table, column, overflowingNode.PageID, rightNodePageID, upKey)
		}

		parentPageID := path[len(path)-1]
		path = path[:len(path)-1] // Pop

		// Fetch the parent, insert the kicked-up key and new right pointer
		parent, err := e.fetchNode(parentPageID)
		if err != nil {
			return err
		}

		insertIntoNodeArrays(parent, upKey, uint64(rightNodePageID))

		// If the parent didn't overflow, write it to disk and we are completely done!
		if parent.NumKeys < MaxKeys {
			return e.writeNode(parent)
		}

		// If the parent DID overflow, loop again to split the parent!
		overflowingNode = parent
	}

	return nil
}

// ==========================================
// B-Tree Split & Insert Math
// ==========================================

// insertIntoNodeArrays finds the correct sorted position for a key/pointer pair
// and shifts the existing elements to the right to make room.
func insertIntoNodeArrays(node *BTreeNode, key core.Value, ptr uint64) {
	insertIdx := 0
	for insertIdx < int(node.NumKeys) {
		cmp, _ := key.Compare(node.Keys[insertIdx])
		if cmp < 0 {
			break // We found the spot where the new key belongs
		}
		insertIdx++
	}

	// 1. Extend the slices by 1
	node.Keys = append(node.Keys, core.Value{})
	node.Pointers = append(node.Pointers, 0)

	// 2. Shift everything to the right of insertIdx over by 1
	copy(node.Keys[insertIdx+1:], node.Keys[insertIdx:])

	// Internal nodes have N+1 pointers, so the pointer shift is slightly different
	if node.NodeType == NodeTypeInternal {
		// Shift pointers from insertIdx+1 onwards
		copy(node.Pointers[insertIdx+2:], node.Pointers[insertIdx+1:])
		node.Pointers[insertIdx+1] = ptr // The new child goes to the right of the key
	} else {
		// Leaf nodes have N pointers
		copy(node.Pointers[insertIdx+1:], node.Pointers[insertIdx:])
		node.Pointers[insertIdx] = ptr
	}

	// 3. Drop the new key in
	node.Keys[insertIdx] = key
	node.NumKeys++
}

// splitLeafNode takes a full leaf, chops it in half, creates a new right sibling,
// and returns the key that needs to be pushed up to the parent.
func (e *Engine) splitLeafNode(leftNode *BTreeNode) (core.Value, int, error) {
	// 1. Get a new page for the right half
	pageCount, err := e.indexFile.getPageCount()
	if err != nil {
		return core.Value{}, 0, err
	}
	rightNodeID := pageCount
	rightNode := NewLeafNode(rightNodeID)

	// 2. Figure out where to chop (exactly in the middle)
	splitIdx := int(leftNode.NumKeys) / 2

	// 3. Move the upper half of the keys and pointers to the right node
	rightNode.Keys = append(rightNode.Keys, leftNode.Keys[splitIdx:]...)
	rightNode.Pointers = append(rightNode.Pointers, leftNode.Pointers[splitIdx:]...)
	rightNode.NumKeys = uint16(len(rightNode.Keys))

	// 4. Truncate the left node's arrays
	leftNode.Keys = leftNode.Keys[:splitIdx]
	leftNode.Pointers = leftNode.Pointers[:splitIdx]
	leftNode.NumKeys = uint16(len(leftNode.Keys))

	// 5. Update the NextLeaf Linked List!
	rightNode.NextLeaf = leftNode.NextLeaf
	leftNode.NextLeaf = rightNode.PageID

	// 6. Write both nodes to disk
	if err := e.writeNode(leftNode); err != nil {
		return core.Value{}, 0, err
	}
	if err := e.writeNode(rightNode); err != nil {
		return core.Value{}, 0, err
	}

	// In a B+-Tree leaf split, the key that goes up is a COPY of the first key in the right node
	upKey := rightNode.Keys[0]
	return upKey, rightNode.PageID, nil
}

// createNewRoot is called when the old Root node splits. The tree grows taller by 1 level!
func (e *Engine) createNewRoot(table, column string, leftPageID, rightPageID int, upKey core.Value) error {
	// 1. Allocate a new page for the new Root
	pageCount, err := e.indexFile.getPageCount()
	if err != nil {
		return err
	}
	newRootID := pageCount

	// 2. The new root is ALWAYS an Internal Node
	newRoot := NewInternalNode(newRootID)

	// 3. Wire up the initial state: 1 Key, 2 Pointers
	newRoot.Keys = append(newRoot.Keys, upKey)
	newRoot.Pointers = append(newRoot.Pointers, uint64(leftPageID), uint64(rightPageID))
	newRoot.NumKeys = 1

	// 4. Save the new root to disk
	if err := e.writeNode(newRoot); err != nil {
		return err
	}

	// 5. Update the Catalog to point to the new Root Page ID
	e.mu.Lock()
	meta, err := e.catalog.GetTable(table)
	if err != nil {
		e.mu.Unlock()
		return err
	}
	meta.IndexRoots[column] = newRootID
	e.mu.Unlock()

	// 6. Save the updated catalog to disk
	return e.catalog.Save(e.dataDir)
}

// splitInternalNode is a stub for now.
// A 4KB internal node holding integers can route roughly 250^2 (62,500) rows
// before it ever needs to split itself.
func (e *Engine) splitInternalNode(node *BTreeNode) (core.Value, int, error) {
	return core.Value{}, 0, fmt.Errorf("internal node split cascade not yet implemented - tree too large!")
}
