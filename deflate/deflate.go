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
	decompressor            io.ReadCloser
	compressor              *flate.Writer
}

func DefaultDeflateConfig() *DeflateConfig {
	flateWriter, _ := flate.NewWriter(nil, flate.BestSpeed)

	return &DeflateConfig{
		serverNoContextTakeover: true,
		clientNoContextTakeover: true,
		decompressor:            flate.NewReader(nil),
		compressor:              flateWriter,
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

	header = fmt.Appendf(header, "server_max_window_bits=%d;client_max_window_bits=%d", maxWindowBits, maxWindowBits)

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
	extensionHeader = strings.ReplaceAll(strings.TrimSpace(extensionHeader), " ", "")
	extensionOptions := strings.SplitSeq(extensionHeader, ",")

	for option := range extensionOptions {
		validConfig, validationErr := validateExtensionOption(option)
		if validationErr == nil {
			flateWriter, _ := flate.NewWriter(nil, flate.BestSpeed)
			validConfig.compressor = flateWriter
			validConfig.decompressor = flate.NewReader(nil)
			return validConfig, nil
		} else {
			debug.Println(validationErr)
		}
	}

	return nil, errors.New("No agreed extension, declining connection")
}

func validateExtensionOption(option string) (*DeflateConfig, error) {
	potentialConfig := &DeflateConfig{
		clientNoContextTakeover: false,
		serverNoContextTakeover: false,
	}

	debug.Printf("Server option: %s\n", option)

	clientDeflateEnabled := true
	serverDeflateEnabled := true

	headerParts := strings.SplitSeq(option, ";")
	for part := range headerParts {
		if after, ok := strings.CutPrefix(part, "client_max_window_bits="); ok {
			bits, err := strconv.Atoi(after)
			if err != nil {
				return nil, err
			}

			if bits < maxWindowBits {
				clientDeflateEnabled = false
			}
		}

		if after, ok := strings.CutPrefix(part, "server_max_window_bits="); ok {
			bits, err := strconv.Atoi(after)
			if err != nil {
				return nil, err
			}

			if bits < maxWindowBits {
				serverDeflateEnabled = false
			}
		}

		if strings.Contains(part, "client_no_context_takeover") {
			potentialConfig.clientNoContextTakeover = true
		}

		if strings.Contains(part, "server_no_context_takeover") {
			potentialConfig.serverNoContextTakeover = true
		}
	}

	if !clientDeflateEnabled {
		potentialConfig.clientNoContextTakeover = false
	}

	if !serverDeflateEnabled {
		potentialConfig.serverNoContextTakeover = false
	}

	return potentialConfig, nil
}

func (c *DeflateConfig) Deflate(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return data, nil
	}

	debug.Printf("Data to deflate: %v\n", data)
	var writer bytes.Buffer
	writer.Grow(len(data))
	c.compressor.Reset(&writer)

	n, err := c.compressor.Write(data)
	if err != nil {
		return nil, err
	}

	err = c.compressor.Close()
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
	if flateReader, ok := c.decompressor.(flate.Resetter); ok {
		flateReader.Reset(reader, nil)
	}

	decompressed, err := io.ReadAll(c.decompressor)
	if err != nil && err != io.ErrUnexpectedEOF {
		return nil, err
	}

	return decompressed, nil
}

func (c *DeflateConfig) Enabled() (bool, bool) {
	return c.serverNoContextTakeover, c.clientNoContextTakeover
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
