// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package playerweb

import (
	"encoding/binary"
	"fmt"
)

const (
	kVideoStreamMagic   = 0x53503256 // "SP2V"
	kSessionConfigMagic = 0x53503243 // "SP2C"
	kDeviceInfoMagic    = 0x53503244 // "SP2D"
	kTransportH264      = 0
	kCapH264            = 1 << 3
	kCapCommandStream   = 1 << 0
	deviceInfoCapOffset = 22
	deviceInfoWireSize  = 36
)

func annexBAccessUnits(annexB []byte) ([][]byte, error) {
	nals, err := splitAnnexBNALs(annexB)
	if err != nil {
		return nil, err
	}
	if len(nals) == 0 {
		return nil, fmt.Errorf("no NAL units in annex-B stream")
	}
	var aus [][]byte
	var cur []byte
	var curVCL bool
	for _, nal := range nals {
		if len(nal) == 0 {
			continue
		}
		nalType := nal[0] & 0x1f
		// AUD (9) is not part of the SP2V access unit payload.
		if nalType == 9 {
			continue
		}
		isVCL := nalType == 1 || nalType == 5
		if isVCL && curVCL {
			aus = append(aus, cur)
			cur = nil
			curVCL = false
		}
		if nalType == 7 && len(cur) > 0 {
			aus = append(aus, cur)
			cur = nil
			curVCL = false
		}
		cur = append(cur, 0, 0, 0, 1)
		cur = append(cur, nal...)
		if isVCL {
			curVCL = true
		}
	}
	if len(cur) > 0 {
		aus = append(aus, cur)
	}
	if len(aus) == 0 {
		return nil, fmt.Errorf("no access units assembled")
	}
	return aus, nil
}

func splitAnnexBNALs(annexB []byte) ([][]byte, error) {
	var nals [][]byte
	i := 0
	for i < len(annexB) {
		start, scLen := findStartCode(annexB, i)
		if start < 0 {
			break
		}
		i = start + scLen
		next, _ := findStartCode(annexB, i)
		end := len(annexB)
		if next >= 0 {
			end = next
		}
		if i >= end {
			return nil, fmt.Errorf("empty NAL at offset %d", start)
		}
		nals = append(nals, annexB[i:end])
		i = end
	}
	return nals, nil
}

func findStartCode(b []byte, from int) (int, int) {
	for i := from; i+3 <= len(b); i++ {
		if b[i] == 0 && b[i+1] == 0 {
			if b[i+2] == 1 {
				return i, 3
			}
			if i+3 < len(b) && b[i+2] == 0 && b[i+3] == 1 {
				return i, 4
			}
		}
	}
	return -1, 0
}

func annexBToAVCC(annexB []byte) ([]byte, error) {
	nals, err := splitAnnexBNALs(annexB)
	if err != nil {
		return nil, err
	}
	var out []byte
	for _, nal := range nals {
		if len(nal) == 0 {
			continue
		}
		l := uint32(len(nal))
		var hdr [4]byte
		binary.BigEndian.PutUint32(hdr[:], l)
		out = append(out, hdr[:]...)
		out = append(out, nal...)
	}
	return out, nil
}

func accessUnitIsKeyframe(annexB []byte) bool {
	nals, err := splitAnnexBNALs(annexB)
	if err != nil {
		return false
	}
	for _, nal := range nals {
		if len(nal) > 0 && (nal[0]&0x1f) == 5 {
			return true
		}
	}
	return false
}

func buildSessionConfigMessage(transport uint8) []byte {
	body := make([]byte, 8)
	binary.LittleEndian.PutUint32(body[0:], kSessionConfigMagic)
	body[6] = transport
	msg := make([]byte, 16)
	binary.LittleEndian.PutUint32(msg[0:], kSessionConfigMagic)
	binary.LittleEndian.PutUint32(msg[4:], uint32(len(body)))
	copy(msg[8:], body)
	return msg
}

func buildSP2VMessage(avcc []byte, seq uint32, keyframe bool) []byte {
	const fixed = 5
	length := uint32(fixed + len(avcc))
	msg := make([]byte, 8+length)
	binary.LittleEndian.PutUint32(msg[0:], kVideoStreamMagic)
	binary.LittleEndian.PutUint32(msg[4:], length)
	if keyframe {
		msg[8] = 1
	}
	binary.LittleEndian.PutUint32(msg[9:], seq)
	copy(msg[13:], avcc)
	return msg
}
