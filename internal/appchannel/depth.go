// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package appchannel

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/vmihailenco/msgpack/v5"
)

// maxMsgpackDepth is the deepest container nesting a peer frame may
// contain. msgpack/v5 walks arrays and maps by recursion (Skip and
// decode both), and a Go stack overflow is fatal to the process.
// 256 is far past any envelope an app sends; a hostile frame of
// one-element arrays hits it while still inside MaxFrameBytes.
const maxMsgpackDepth = 256

// unmarshalBounded decodes one MessagePack value after rejecting
// container nesting that would overflow the goroutine stack.
func unmarshalBounded(b []byte, dst any) error {
	if err := checkMsgpackDepth(b, maxMsgpackDepth); err != nil {
		return err
	}
	if err := msgpack.Unmarshal(b, dst); err != nil {
		return err
	}
	return nil
}

// checkMsgpackDepth walks one top-level value iteratively. Container
// depth counts the open arrays and maps around the value being read
// (the value itself is not yet open). A chain of 256 one-element
// arrays is accepted; the 257th is not.
func checkMsgpackDepth(b []byte, maxDepth int) error {
	// stack[0] is a virtual root holding the one top-level value.
	// Each further entry is an open container and how many values
	// remain inside it. len(stack)-1 is the container depth.
	stack := make([]int, 1, maxDepth+2)
	stack[0] = 1
	i := 0
	for len(stack) > 0 {
		top := len(stack) - 1
		if stack[top] == 0 {
			stack = stack[:top]
			continue
		}
		if i >= len(b) {
			return io.ErrUnexpectedEOF
		}
		stack[top]--
		children, next, container, err := msgpackHead(b, i)
		if err != nil {
			return err
		}
		i = next
		if !container {
			continue
		}
		if len(stack) > maxDepth {
			return fmt.Errorf("appchannel: msgpack nesting exceeds %d (code %d)", maxDepth, ErrCodeParse)
		}
		stack = append(stack, children)
	}
	return nil
}

// msgpackHead parses the value at b[i]. For an array or map, children
// is the number of following values (map entries count as two) and
// container is true. For every other type, next is the index just
// past the value.
func msgpackHead(b []byte, i int) (children, next int, container bool, err error) {
	c := b[i]
	i++
	switch {
	case c <= 0x7f || c >= 0xe0: // fixint
		return 0, i, false, nil
	case c >= 0x80 && c <= 0x8f: // fixmap
		return int(c&0x0f) * 2, i, true, nil
	case c >= 0x90 && c <= 0x9f: // fixarray
		return int(c & 0x0f), i, true, nil
	case c >= 0xa0 && c <= 0xbf: // fixstr
		return skipBytes(b, i, int(c&0x1f))
	case c == 0xc0 || c == 0xc2 || c == 0xc3: // nil, false, true
		return 0, i, false, nil
	case c == 0xc1:
		return 0, i, false, errors.New("appchannel: invalid msgpack code 0xc1")
	case c == 0xc4 || c == 0xd9: // bin8, str8
		n, next, err := u8len(b, i)
		if err != nil {
			return 0, i, false, err
		}
		return skipBytes(b, next, n)
	case c == 0xc5 || c == 0xda: // bin16, str16
		n, next, err := u16len(b, i)
		if err != nil {
			return 0, i, false, err
		}
		return skipBytes(b, next, n)
	case c == 0xc6 || c == 0xdb: // bin32, str32
		n, next, err := u32len(b, i)
		if err != nil {
			return 0, i, false, err
		}
		return skipBytes(b, next, n)
	case c == 0xc7: // ext8: len, type, data
		n, next, err := u8len(b, i)
		if err != nil {
			return 0, i, false, err
		}
		return skipBytes(b, next, n+1)
	case c == 0xc8: // ext16
		n, next, err := u16len(b, i)
		if err != nil {
			return 0, i, false, err
		}
		return skipBytes(b, next, n+1)
	case c == 0xc9: // ext32
		n, next, err := u32len(b, i)
		if err != nil {
			return 0, i, false, err
		}
		return skipBytes(b, next, n+1)
	case c == 0xca: // float32
		return skipBytes(b, i, 4)
	case c == 0xcb: // float64
		return skipBytes(b, i, 8)
	case c == 0xcc || c == 0xd0: // uint8, int8
		return skipBytes(b, i, 1)
	case c == 0xcd || c == 0xd1:
		return skipBytes(b, i, 2)
	case c == 0xce || c == 0xd2:
		return skipBytes(b, i, 4)
	case c == 0xcf || c == 0xd3:
		return skipBytes(b, i, 8)
	case c == 0xd4: // fixext 1
		return skipBytes(b, i, 2)
	case c == 0xd5:
		return skipBytes(b, i, 3)
	case c == 0xd6:
		return skipBytes(b, i, 5)
	case c == 0xd7:
		return skipBytes(b, i, 9)
	case c == 0xd8:
		return skipBytes(b, i, 17)
	case c == 0xdc: // array16
		n, next, err := u16len(b, i)
		if err != nil {
			return 0, i, false, err
		}
		return n, next, true, nil
	case c == 0xdd: // array32
		n, next, err := u32len(b, i)
		if err != nil {
			return 0, i, false, err
		}
		return n, next, true, nil
	case c == 0xde: // map16
		n, next, err := u16len(b, i)
		if err != nil {
			return 0, i, false, err
		}
		if n > (int(^uint(0)>>1))/2 {
			return 0, i, false, errors.New("appchannel: msgpack map too large")
		}
		return n * 2, next, true, nil
	case c == 0xdf: // map32
		n, next, err := u32len(b, i)
		if err != nil {
			return 0, i, false, err
		}
		if n > (int(^uint(0)>>1))/2 {
			return 0, i, false, errors.New("appchannel: msgpack map too large")
		}
		return n * 2, next, true, nil
	default:
		return 0, i, false, fmt.Errorf("appchannel: unknown msgpack code 0x%x", c)
	}
}

func skipBytes(b []byte, i, n int) (int, int, bool, error) {
	if n < 0 || i > len(b) || n > len(b)-i {
		return 0, i, false, io.ErrUnexpectedEOF
	}
	return 0, i + n, false, nil
}

func u8len(b []byte, i int) (int, int, error) {
	if i >= len(b) {
		return 0, i, io.ErrUnexpectedEOF
	}
	return int(b[i]), i + 1, nil
}

func u16len(b []byte, i int) (int, int, error) {
	if i > len(b) || len(b)-i < 2 {
		return 0, i, io.ErrUnexpectedEOF
	}
	return int(binary.BigEndian.Uint16(b[i:])), i + 2, nil
}

func u32len(b []byte, i int) (int, int, error) {
	if i > len(b) || len(b)-i < 4 {
		return 0, i, io.ErrUnexpectedEOF
	}
	return int(binary.BigEndian.Uint32(b[i:])), i + 4, nil
}
