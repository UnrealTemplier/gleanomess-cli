package imageinfo

import (
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"io"
)

var (
	errNotAVIF     = errors.New("not an AVIF image")
	errInvalidISPE = errors.New("invalid or missing ispe box in AVIF")
)

func init() {
	// Register AVIF format with image package.
	// AVIF files start with a 4-byte size followed by "ftyp".
	image.RegisterFormat("avif", "????ftyp", decodeAVIFStub, DecodeAVIFConfig)
}

// decodeAVIFStub is registered for image.Decode.
// GleanoMess only inspects dimensions and does not decode full AVIF pixels.
func decodeAVIFStub(r io.Reader) (image.Image, error) {
	return nil, errors.New("full AVIF pixel decoding is not supported; use DecodeAVIFConfig for dimensions")
}

// IsAVIF checks if the provided header bytes match an AVIF container.
func IsAVIF(header []byte) bool {
	if len(header) < 12 {
		return false
	}
	if string(header[4:8]) != "ftyp" {
		return false
	}
	majorBrand := string(header[8:12])
	if majorBrand == "avif" || majorBrand == "avis" {
		return true
	}
	// Check compatible brands (from offset 16 onwards)
	boxSize := binary.BigEndian.Uint32(header[0:4])
	limit := int(boxSize)
	if limit > len(header) || limit == 0 {
		limit = len(header)
	}
	for i := 16; i+4 <= limit; i += 4 {
		brand := string(header[i : i+4])
		if brand == "avif" || brand == "avis" {
			return true
		}
	}
	return false
}

// DecodeAVIFConfig extracts width and height from an AVIF file without decoding pixels.
func DecodeAVIFConfig(r io.Reader) (image.Config, error) {
	// Read first 12 bytes to verify ftyp
	header := make([]byte, 12)
	if _, err := io.ReadFull(r, header); err != nil {
		return image.Config{}, fmt.Errorf("reading AVIF header: %w", err)
	}
	if string(header[4:8]) != "ftyp" {
		return image.Config{}, errNotAVIF
	}

	ftypSize := binary.BigEndian.Uint32(header[0:4])
	if ftypSize < 12 {
		return image.Config{}, errors.New("invalid ftyp box size")
	}

	// Read remaining bytes of ftyp box
	remainingFtyp := make([]byte, ftypSize-12)
	if _, err := io.ReadFull(r, remainingFtyp); err != nil {
		return image.Config{}, fmt.Errorf("reading ftyp contents: %w", err)
	}

	fullFtyp := append(header, remainingFtyp...)
	if !IsAVIF(fullFtyp) {
		return image.Config{}, errNotAVIF
	}

	// Now scan top-level boxes to find 'meta'
	var maxWidth, maxHeight int
	foundISPE := false

	for {
		boxHeader := make([]byte, 8)
		if _, err := io.ReadFull(r, boxHeader); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				break
			}
			return image.Config{}, err
		}

		bSize := uint64(binary.BigEndian.Uint32(boxHeader[0:4]))
		bType := string(boxHeader[4:8])
		headerLen := uint64(8)

		if bSize == 1 {
			// 64-bit largesize
			largeBytes := make([]byte, 8)
			if _, err := io.ReadFull(r, largeBytes); err != nil {
				return image.Config{}, err
			}
			bSize = binary.BigEndian.Uint64(largeBytes)
			headerLen = 16
		} else if bSize == 0 {
			// Box extends to EOF, we cannot know total size easily without seeking
			bSize = 0
		}

		if bType == "meta" {
			// 'meta' is a FullBox: 1 byte version + 3 bytes flags
			metaFullHeader := make([]byte, 4)
			if _, err := io.ReadFull(r, metaFullHeader); err != nil {
				return image.Config{}, err
			}
			metaPayloadLen := bSize
			if metaPayloadLen > 0 {
				metaPayloadLen -= headerLen + 4
			}

			var metaReader io.Reader = r
			if metaPayloadLen > 0 {
				metaReader = io.LimitReader(r, int64(metaPayloadLen))
			}

			w, h, err := parseMetaBox(metaReader)
			if err == nil && w > 0 && h > 0 {
				if w*h > maxWidth*maxHeight {
					maxWidth = w
					maxHeight = h
					foundISPE = true
				}
			}
			break
		} else {
			// Skip box payload
			if bSize > headerLen {
				skipLen := int64(bSize - headerLen)
				if _, err := io.CopyN(io.Discard, r, skipLen); err != nil {
					break
				}
			} else if bSize == 0 {
				break
			}
		}
	}

	if !foundISPE || maxWidth <= 0 || maxHeight <= 0 {
		return image.Config{}, errInvalidISPE
	}

	return image.Config{
		ColorModel: color.RGBAModel,
		Width:      maxWidth,
		Height:     maxHeight,
	}, nil
}

func parseMetaBox(r io.Reader) (int, int, error) {
	var maxWidth, maxHeight int
	found := false

	for {
		header := make([]byte, 8)
		if _, err := io.ReadFull(r, header); err != nil {
			break
		}
		bSize := uint64(binary.BigEndian.Uint32(header[0:4]))
		bType := string(header[4:8])
		headerLen := uint64(8)

		if bSize == 1 {
			largeBytes := make([]byte, 8)
			if _, err := io.ReadFull(r, largeBytes); err != nil {
				break
			}
			bSize = binary.BigEndian.Uint64(largeBytes)
			headerLen = 16
		}

		payloadLen := int64(bSize - headerLen)
		if bSize == 0 {
			payloadLen = -1
		}

		if bType == "iprp" {
			var iprpReader io.Reader = r
			if payloadLen > 0 {
				iprpReader = io.LimitReader(r, payloadLen)
			}
			w, h, err := parseIprpBox(iprpReader)
			if err == nil && w > 0 && h > 0 {
				if w*h > maxWidth*maxHeight {
					maxWidth = w
					maxHeight = h
					found = true
				}
			}
			break
		} else {
			if payloadLen > 0 {
				if _, err := io.CopyN(io.Discard, r, payloadLen); err != nil {
					break
				}
			}
		}
	}

	if found {
		return maxWidth, maxHeight, nil
	}
	return 0, 0, errInvalidISPE
}

func parseIprpBox(r io.Reader) (int, int, error) {
	var maxWidth, maxHeight int
	found := false

	for {
		header := make([]byte, 8)
		if _, err := io.ReadFull(r, header); err != nil {
			break
		}
		bSize := uint64(binary.BigEndian.Uint32(header[0:4]))
		bType := string(header[4:8])
		headerLen := uint64(8)

		if bSize == 1 {
			largeBytes := make([]byte, 8)
			if _, err := io.ReadFull(r, largeBytes); err != nil {
				break
			}
			bSize = binary.BigEndian.Uint64(largeBytes)
			headerLen = 16
		}

		payloadLen := int64(bSize - headerLen)
		if bType == "ipco" {
			var ipcoReader io.Reader = r
			if payloadLen > 0 {
				ipcoReader = io.LimitReader(r, payloadLen)
			}
			w, h, err := parseIpcoBox(ipcoReader)
			if err == nil && w > 0 && h > 0 {
				if w*h > maxWidth*maxHeight {
					maxWidth = w
					maxHeight = h
					found = true
				}
			}
			break
		} else {
			if payloadLen > 0 {
				if _, err := io.CopyN(io.Discard, r, payloadLen); err != nil {
					break
				}
			}
		}
	}

	if found {
		return maxWidth, maxHeight, nil
	}
	return 0, 0, errInvalidISPE
}

func parseIpcoBox(r io.Reader) (int, int, error) {
	var maxWidth, maxHeight int
	found := false

	for {
		header := make([]byte, 8)
		if _, err := io.ReadFull(r, header); err != nil {
			break
		}
		bSize := uint64(binary.BigEndian.Uint32(header[0:4]))
		bType := string(header[4:8])
		headerLen := uint64(8)

		if bSize == 1 {
			largeBytes := make([]byte, 8)
			if _, err := io.ReadFull(r, largeBytes); err != nil {
				break
			}
			bSize = binary.BigEndian.Uint64(largeBytes)
			headerLen = 16
		}

		payloadLen := int64(bSize - headerLen)

		if bType == "ispe" {
			// FullBox: 1 byte version, 3 bytes flags, 4 bytes width, 4 bytes height = 12 bytes payload
			if payloadLen >= 12 {
				ispeData := make([]byte, 12)
				if _, err := io.ReadFull(r, ispeData); err == nil {
					w := int(binary.BigEndian.Uint32(ispeData[4:8]))
					h := int(binary.BigEndian.Uint32(ispeData[8:12]))
					if w > 0 && h > 0 && w*h > maxWidth*maxHeight {
						maxWidth = w
						maxHeight = h
						found = true
					}
					// skip remaining bytes if any
					if payloadLen > 12 {
						io.CopyN(io.Discard, r, payloadLen-12)
					}
					continue
				}
			}
		}

		if payloadLen > 0 {
			if _, err := io.CopyN(io.Discard, r, payloadLen); err != nil {
				break
			}
		}
	}

	if found {
		return maxWidth, maxHeight, nil
	}
	return 0, 0, errInvalidISPE
}
