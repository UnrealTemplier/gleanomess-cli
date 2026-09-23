package imageinfo

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// makeSyntheticAVIF creates a minimal valid AVIF header with the given width and height.
func makeSyntheticAVIF(width, height uint32) []byte {
	var buf bytes.Buffer

	// 1. ftyp box
	// size = 4 (size) + 4 ("ftyp") + 4 (major "avif") + 4 (minor 0) + 4 (compatible "mif1") = 20
	binary.Write(&buf, binary.BigEndian, uint32(20))
	buf.WriteString("ftyp")
	buf.WriteString("avif")
	binary.Write(&buf, binary.BigEndian, uint32(0))
	buf.WriteString("mif1")

	// 2. Build ispe box payload
	var ispeBuf bytes.Buffer
	binary.Write(&ispeBuf, binary.BigEndian, uint32(20)) // size = 20
	ispeBuf.WriteString("ispe")
	binary.Write(&ispeBuf, binary.BigEndian, uint32(0)) // version (1 byte) + flags (3 bytes)
	binary.Write(&ispeBuf, binary.BigEndian, width)
	binary.Write(&ispeBuf, binary.BigEndian, height)

	// 3. Build ipco box containing ispe
	var ipcoBuf bytes.Buffer
	binary.Write(&ipcoBuf, binary.BigEndian, uint32(8+ispeBuf.Len()))
	ipcoBuf.WriteString("ipco")
	ipcoBuf.Write(ispeBuf.Bytes())

	// 4. Build iprp box containing ipco
	var iprpBuf bytes.Buffer
	binary.Write(&iprpBuf, binary.BigEndian, uint32(8+ipcoBuf.Len()))
	iprpBuf.WriteString("iprp")
	iprpBuf.Write(ipcoBuf.Bytes())

	// 5. Build meta box (FullBox: 4 size + 4 "meta" + 4 version/flags + iprp)
	var metaBuf bytes.Buffer
	binary.Write(&metaBuf, binary.BigEndian, uint32(12+iprpBuf.Len()))
	metaBuf.WriteString("meta")
	binary.Write(&metaBuf, binary.BigEndian, uint32(0)) // version (1 byte) + flags (3 bytes)
	metaBuf.Write(iprpBuf.Bytes())

	buf.Write(metaBuf.Bytes())
	return buf.Bytes()
}

func TestAVIFConfig(t *testing.T) {
	data := makeSyntheticAVIF(1920, 1080)
	cfg, err := DecodeAVIFConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("DecodeAVIFConfig failed: %v", err)
	}
	if cfg.Width != 1920 || cfg.Height != 1080 {
		t.Errorf("expected 1920x1080, got %dx%d", cfg.Width, cfg.Height)
	}
}
