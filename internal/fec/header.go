package fec

import "encoding/binary"

const (
	HeaderSize = 6
	Magic      = 0xFE
)

type Header struct {
	GroupID    uint16
	Index     uint8
	DataCount uint8
	ParityCount uint8
}

func (h Header) Marshal(buf []byte) {
	buf[0] = Magic
	binary.BigEndian.PutUint16(buf[1:], h.GroupID)
	buf[3] = h.Index
	buf[4] = h.DataCount
	buf[5] = h.ParityCount
}

func ParseHeader(buf []byte) (Header, bool) {
	if len(buf) < HeaderSize || buf[0] != Magic {
		return Header{}, false
	}
	return Header{
		GroupID:     binary.BigEndian.Uint16(buf[1:]),
		Index:       buf[3],
		DataCount:   buf[4],
		ParityCount: buf[5],
	}, true
}

func IsFEC(buf []byte) bool {
	return len(buf) >= HeaderSize && buf[0] == Magic
}
