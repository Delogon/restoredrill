package ulid

import (
 "math/big"
 "testing"
 "time"
)

// referenceEncode is an independent big.Int-based implementation used only to
// cross-check the bit-manipulation encoder.
func referenceEncode(raw [16]byte) string {
 const alpha = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
 n := new(big.Int).SetBytes(raw[:])
 base := big.NewInt(32)
 mod := new(big.Int)
 var out []byte
 for i := 0; i < 26; i++ {
  n.DivMod(n, base, mod)
  out = append([]byte{alpha[mod.Int64()]}, out...)
 }
 return string(out)
}

func TestAgainstReference(t *testing.T) {
 for i := 0; i < 500; i++ {
  id := NewAt(time.UnixMilli(int64(1794489590123) + int64(i)))
  raw, err := decode(id)
  if err != nil { t.Fatal(err) }
  if got := referenceEncode(raw); got != id {
   t.Fatalf("encode mismatch:\n got %s\nwant %s", id, got)
  }
 }
}
