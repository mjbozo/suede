package deflate

import (
	"bytes"
	"compress/flate"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/mjbozo/suede/debug"
)

const (
	maxWindowBits = 15
)

type DeflateConfig struct {
	serverNoContextTakeover bool
	clientNoContextTakeover bool
}

func DefaultDeflateConfig() *DeflateConfig {
	return &DeflateConfig{
		serverNoContextTakeover: true,
		clientNoContextTakeover: true,
	}
}

func (c *DeflateConfig) Header() []byte {
	if c == nil {
		return nil
	}

	var header []byte
	header = append(header, []byte("Sec-WebSocket-Extensions: permessage-deflate;")...)

	if c.serverNoContextTakeover {
		header = append(header, []byte("server_no_context_takeover;")...)
	}

	if c.clientNoContextTakeover {
		header = append(header, []byte("client_no_context_takeover;")...)
	}

	header = fmt.Appendf(header, "server_max_window_bits=%d;client_max_window_bits=%d;", maxWindowBits, maxWindowBits)

	return header
}

func Negotiate(extensionHeader string) *DeflateConfig {
	extensionHeader = strings.ReplaceAll(extensionHeader, " ", "")
	extensionOptions := strings.SplitSeq(extensionHeader, ",")

	for option := range extensionOptions {
		if !strings.Contains(option, "permessage-deflate") {
			continue
		}

		config := DefaultDeflateConfig()

		if strings.Contains(option, "client_max_window_bits=") {
			clientBits := parseWindowBits(option, "client_max_window_bits")
			if clientBits < maxWindowBits {
				continue // can't support < 15 bits, decline extension negotiation
			}
		}

		if strings.Contains(option, "server_max_window_bits=") {
			serverBits := parseWindowBits(option, "server_max_window_bits")
			if serverBits < maxWindowBits {
				continue // can't support < 15 bits, decline extension negotiation
			}
		}

		return config
	}

	return nil
}

func Parse(extensionHeader string) (*DeflateConfig, error) {
	var config *DeflateConfig

	extensionHeader = strings.ReplaceAll(extensionHeader, " ", "")
	extensionOptions := strings.SplitSeq(extensionHeader, ",")

	for option := range extensionOptions {
		validationErr := validateHeaderOption(option)
		if validationErr == nil {
			config = &DeflateConfig{}
			config.clientNoContextTakeover = true
			config.serverNoContextTakeover = true
			break
		}
	}

	if config == nil {
		return nil, errors.New("No agreed extension, declining connection")
	}

	return config, nil
}

func validateHeaderOption(option string) error {
	serverNoContextTakeover := false
	clientNoContextTakeover := false

	headerParts := strings.SplitSeq(option, ";")
	for part := range headerParts {
		if after, ok := strings.CutPrefix(part, "client_max_window_bits="); ok {
			bits, err := strconv.Atoi(after)
			if err != nil {
				return err
			}

			if bits != maxWindowBits {
				return errors.New("Can't support < 15 window bits")
			}
		}

		if after, ok := strings.CutPrefix(part, "server_max_window_bits="); ok {
			bits, err := strconv.Atoi(after)
			if err != nil {
				return err
			}

			if bits != maxWindowBits {
				return errors.New("Can't support < 15 window bits")
			}
		}

		if strings.Contains(part, "client_no_context_takeover") {
			clientNoContextTakeover = true
		}

		if strings.Contains(part, "server_no_context_takeover") {
			serverNoContextTakeover = true
		}
	}

	if !clientNoContextTakeover {
		return errors.New("client context takeover not supported")
	}

	if !serverNoContextTakeover {
		return errors.New("server context takeover not supported")
	}

	return nil
}

func (c *DeflateConfig) Deflate(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return data, nil
	}

	debug.Printf("Data to deflate: %v\n", data)
	var writer bytes.Buffer
	compressor, err := flate.NewWriter(&writer, flate.DefaultCompression)
	if err != nil {
		return nil, err
	}

	n, err := compressor.Write(data)
	if err != nil {
		return nil, err
	}

	err = compressor.Close()
	if err != nil {
		return nil, err
	}

	compressedData := writer.Bytes()
	compressedData = compressedData[:len(compressedData)-4]
	debug.Printf("Wrote %d bytes: %v\n", n, compressedData)
	return compressedData, nil
}

func (c *DeflateConfig) Inflate(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return data, nil
	}

	data = append(data, 0x00, 0x00, 0xFF, 0xFF)
	debug.Printf("Data to inflate: %v\n", data)
	reader := bytes.NewReader(data)
	decompressor := flate.NewReader(reader)
	defer decompressor.Close()

	decompressed, err := io.ReadAll(decompressor)
	if err != nil {
		return nil, err
	}

	return decompressed, nil
}

func parseWindowBits(header, param string) int {
	parts := strings.SplitSeq(header, ";")

	for part := range parts {
		part = strings.TrimSpace(part)
		if after, ok := strings.CutPrefix(part, param+"="); ok {
			value, err := strconv.Atoi(strings.TrimSpace(after))
			if err == nil {
				return value
			}
		}
	}

	return maxWindowBits
}
