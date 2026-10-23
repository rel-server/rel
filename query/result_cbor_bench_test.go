package query

import (
	"encoding/binary"
	"testing"
)

// BenchmarkAppendValue_Scalars is the per-value dispatch cost of ResultEncoder.appendValue over a
// mix of the types a typical result holds.
func BenchmarkAppendValue_Scalars(b *testing.B) {
	be32 := func(v uint32) []byte { return binary.BigEndian.AppendUint32(nil, v) }
	be64 := func(v uint64) []byte { return binary.BigEndian.AppendUint64(nil, v) }
	type item struct {
		oid  uint32
		data []byte
	}
	items := []item{
		{oidInt4, be32(123456)},
		{oidText, []byte("hello world")},
		{oidInt8, be64(9876543210)},
		{oidTimestamptz, be64(700000000000000)},
		{oidBool, []byte{1}},
		{oidUUID, make([]byte, 16)},
		{oidFloat8, be64(0x400921fb54442d18)},
		{oidInt4, be32(7)},
		{oidVarchar, []byte("abc")},
		{oidDate, be32(8000)},
	}
	e := NewValueEncoder(nil)
	dst := make([]byte, 0, 4096)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dst = dst[:0]
		for _, it := range items {
			var err error
			if dst, err = e.appendValue(dst, it.oid, it.data, nil); err != nil {
				b.Fatal(err)
			}
		}
	}
}
