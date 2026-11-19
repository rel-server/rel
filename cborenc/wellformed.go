package cborenc

import "encoding/binary"

// WellFormed reports whether b is exactly one well-formed CBOR data item (RFC 8949 section 5.3.1) : no truncation,
// no trailing bytes, no reserved additional information, no misplaced break. Text strings are not checked for UTF-8
// and tags not for their content's type. Nesting depth is unbounded and costs no recursion.
func WellFormed(b []byte) bool {
	ok, _ := Scan(b)
	return ok
}

// Scan is WellFormed, also reporting whether a cbor-x structure tag (a definition or a reference) appears.
func Scan(b []byte) (wellFormed, structures bool) {
	type frame struct {
		left  uint64 // items still to come in a definite container (a map counts keys and values)
		seen  uint64 // items read so far in an indefinite one
		indef bool
		isMap bool
	}
	var backing [16]frame
	stack := backing[:0]
	pos := 0
	tagged := false // a tag was read and its content not yet
	for {
		if pos >= len(b) {
			return false, false
		}
		ib := b[pos]
		pos++
		major, ai := ib>>5, ib&0x1f
		if ib == Break {
			if len(stack) == 0 || tagged {
				return false, false
			}
			top := stack[len(stack)-1]
			if !top.indef || (top.isMap && top.seen%2 != 0) {
				return false, false
			}
			stack = stack[:len(stack)-1]
		} else {
			var val uint64
			indef := ai == 31
			if !indef {
				v, hl, ok := headArg(b[pos-1:])
				if !ok {
					return false, false
				}
				val, pos = v, pos-1+hl
			}
			tagged = false
			rest := uint64(len(b) - pos)
			switch major {
			case 0, 1:
				if indef {
					return false, false
				}
			case 2, 3:
				if !indef {
					if val > rest {
						return false, false
					}
					pos += int(val)
					break
				}
				// chunks of the same major type, each of definite length, then a break
				for {
					if pos >= len(b) {
						return false, false
					}
					if b[pos] == Break {
						pos++
						break
					}
					if b[pos]>>5 != major || b[pos]&0x1f == 31 {
						return false, false
					}
					chunk := b[pos:]
					n, hl, ok := headArg(chunk)
					if !ok || n > uint64(len(chunk)-hl) {
						return false, false
					}
					pos += hl + int(n)
				}
			case 4, 5:
				if indef {
					stack = append(stack, frame{indef: true, isMap: major == 5})
					continue
				}
				if major == 5 {
					if val > rest/2 {
						return false, false
					}
					val *= 2
				} else if val > rest {
					return false, false
				}
				if val > 0 {
					stack = append(stack, frame{left: val})
					continue
				}
			case 6:
				if indef {
					return false, false
				}
				if val == TagStructureDef || (val >= StructureTagBase && val < StructureTagBase+MaxStructures) {
					structures = true
				}
				tagged = true
				continue // the content that follows is the item
			case 7:
				if indef || (ai == 24 && val < 32) {
					return false, false
				}
			}
		}
		// an item is complete : count it in the containers it closes
		for {
			if len(stack) == 0 {
				return pos == len(b), structures
			}
			top := &stack[len(stack)-1]
			if top.indef {
				top.seen++
				break
			}
			if top.left--; top.left > 0 {
				break
			}
			stack = stack[:len(stack)-1]
		}
	}
}

// headArg reads the argument of the head at the start of b, and the head's length ; ok is false when b is too short.
func headArg(b []byte) (val uint64, n int, ok bool) {
	ai := b[0] & 0x1f
	switch {
	case ai < 24:
		return uint64(ai), 1, true
	case ai <= 27:
		size := 1 << (ai - 24)
		if len(b) < 1+size {
			return 0, 0, false
		}
		switch size {
		case 1:
			return uint64(b[1]), 2, true
		case 2:
			return uint64(binary.BigEndian.Uint16(b[1:])), 3, true
		case 4:
			return uint64(binary.BigEndian.Uint32(b[1:])), 5, true
		}
		return binary.BigEndian.Uint64(b[1:]), 9, true
	}
	return 0, 0, false
}
