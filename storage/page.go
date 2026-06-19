package storage

import (
	"encoding/binary"
	"errors" // You will need this for returning things like "page full" errors

	"aurasql/core"
)

// ==========================================
// 1. Constants and Structs
// ==========================================

const PageSize = 4096

// We use LittleEndian to standardize how we read/write numbers to bytes.
var endian = binary.LittleEndian

type Page struct {
	Data [PageSize]byte
}

// ==========================================
// 2. Initialization
// ==========================================

// NewPage creates a fresh page and initializes the free space pointer.
func NewPage() *Page {
	p := &Page{}
	// Set the Free Space Pointer (bytes 2-3) to the very end of the page
	endian.PutUint16(p.Data[2:4], PageSize)
	return p
}

// ==========================================
// 3. Serialization Helpers
// ==========================================

// Serialize takes a core.Row and flattens it into a raw byte slice.
const NullTag byte = 255

// Serialize flattens a core.Row into a raw byte slice.
func Serialize(row core.Row) []byte {
	var buf []byte // Think of this as an empty Python list that we will append to

	for _, v := range row.Values {
		if v.Null {
			buf = append(buf, NullTag)
			continue
		}

		// Write a 1-byte tag for the type (0 for Int, 1 for Text, 2 for Bool)
		buf = append(buf, byte(v.Type))

		// Write the actual payload based on the type
		switch v.Type {
		case core.TypeInt:
			b := make([]byte, 8) // Allocate 8 bytes
			endian.PutUint64(b, uint64(v.Int))
			buf = append(buf, b...) // The '...' unrolls the slice, like *args in Python

		case core.TypeBool:
			if v.Bool {
				buf = append(buf, 1)
			} else {
				buf = append(buf, 0)
			}

		case core.TypeText:
			strBytes := []byte(v.Str) // Cast string to raw bytes
			length := make([]byte, 2)
			endian.PutUint16(length, uint16(len(strBytes)))
			buf = append(buf, length...)
			buf = append(buf, strBytes...)
		}
	}
	return buf
}

// Deserialize reads a raw byte slice and reconstructs a core.Row.
func Deserialize(data []byte) core.Row {
	var values []core.Value
	cursor := 0 // Our memory pointer

	for cursor < len(data) {
		tag := data[cursor]
		cursor++

		if tag == NullTag {
			// We don't know the exact type if it's strictly null without schema,
			// but for core scope, returning a generic null value works.
			values = append(values, core.Value{Null: true})
			continue
		}

		colType := core.ColumnType(tag)

		switch colType {
		case core.TypeInt:
			val := endian.Uint64(data[cursor : cursor+8]) // Read 8 bytes
			values = append(values, core.NewInt(int64(val)))
			cursor += 8

		case core.TypeBool:
			val := data[cursor] == 1
			values = append(values, core.NewBool(val))
			cursor++

		case core.TypeText:
			strLen := int(endian.Uint16(data[cursor : cursor+2]))
			cursor += 2
			strVal := string(data[cursor : cursor+strLen])
			values = append(values, core.NewText(strVal))
			cursor += strLen
		}
	}

	return core.Row{Values: values}
}

// ==========================================
// 4. Core Page Operations
// ==========================================

// Insert attempts to pack rowBytes into the page.
// It returns the slot index, or an error if there isn't enough space.
// Insert attempts to pack rowBytes into the page.
func (p *Page) Insert(rowBytes []byte) (int, error) {
	// Read the header values
	slotCount := int(endian.Uint16(p.Data[0:2]))
	freeSpace := int(endian.Uint16(p.Data[2:4]))

	// Calculate memory offsets
	slotOffset := 4 + (slotCount * 4)
	requiredSpace := len(rowBytes) + 4 // Row size + 4 bytes for the new slot entry

	// Collision detection: is there enough room between the slots and the data?
	if freeSpace-slotOffset < requiredSpace {
		return 0, errors.New("page is full")
	}

	// Move the free space pointer DOWN
	newFreeSpace := freeSpace - len(rowBytes)

	// Copy the raw row bytes into the data section
	copy(p.Data[newFreeSpace:freeSpace], rowBytes)

	// Write the slot directory entry (2 bytes for offset, 2 bytes for length)
	endian.PutUint16(p.Data[slotOffset:slotOffset+2], uint16(newFreeSpace))
	endian.PutUint16(p.Data[slotOffset+2:slotOffset+4], uint16(len(rowBytes)))

	// Update the page header telemetry
	endian.PutUint16(p.Data[0:2], uint16(slotCount+1))
	endian.PutUint16(p.Data[2:4], uint16(newFreeSpace))

	return slotCount, nil // The slot index is just the old slot count
}

// Get retrieves a row's bytes using its slot index.
func (p *Page) Get(slotIndex int) ([]byte, error) {
	slotCount := int(endian.Uint16(p.Data[0:2]))

	if slotIndex < 0 || slotIndex >= slotCount {
		return nil, errors.New("invalid slot index")
	}

	// Find the slot entry
	slotOffset := 4 + (slotIndex * 4)

	// Read where the data lives and how long it is
	dataOffset := int(endian.Uint16(p.Data[slotOffset : slotOffset+2]))
	dataLength := int(endian.Uint16(p.Data[slotOffset+2 : slotOffset+4]))

	// Return exactly that slice of memory
	return p.Data[dataOffset : dataOffset+dataLength], nil
}

// Delete logically removes a row by zeroing out its slot directory entry.
// For Phase 1, we don't need to physically defragment or compact the page data.
func (p *Page) Delete(slotIndex int) error {
	slotCount := int(endian.Uint16(p.Data[0:2]))

	if slotIndex < 0 || slotIndex >= slotCount {
		return errors.New("invalid slot index")
	}

	slotOffset := 4 + (slotIndex * 4)

	// Zero out the offset and length to mark this slot as deleted (a tombstone)
	endian.PutUint16(p.Data[slotOffset:slotOffset+2], 0)
	endian.PutUint16(p.Data[slotOffset+2:slotOffset+4], 0)

	return nil
}

// Update attempts to replace the data for an existing slot in-place.
// If the new data is larger than the old data, it returns an error.
func (p *Page) Update(slotIndex int, rowBytes []byte) error {
	slotCount := int(endian.Uint16(p.Data[0:2]))
	if slotIndex < 0 || slotIndex >= slotCount {
		return errors.New("invalid slot index")
	}

	entryOffset := 4 + (slotIndex * 4)
	dataOffset := int(endian.Uint16(p.Data[entryOffset : entryOffset+2]))
	oldLength := int(endian.Uint16(p.Data[entryOffset+2 : entryOffset+4]))

	// Gear 1: The new row is the same size or smaller. Overwrite it in place!
	if len(rowBytes) <= oldLength {
		// Copy the new bytes directly over the old bytes
		copy(p.Data[dataOffset:dataOffset+len(rowBytes)], rowBytes)

		// Update the length in the slot directory (the offset stays exactly the same)
		endian.PutUint16(p.Data[entryOffset+2:entryOffset+4], uint16(len(rowBytes)))
		return nil
	}

	// Gear 2: The new row has a longer TEXT string and doesn't fit in the old slot.
	return errors.New("row too large")
}
