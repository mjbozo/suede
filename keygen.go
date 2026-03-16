package suede

import (
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"io"
)

const WEB_SOCKET_GUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

func generateWSKey() string {
	key := make([]byte, 16)
	rand.Read(key)
	encoded := base64.StdEncoding.EncodeToString(key)
	return encoded
}

func generateWSAccept(key string) []byte {
	hash := sha1.New()
	io.WriteString(hash, key+WEB_SOCKET_GUID)
	sha := hash.Sum(nil)
	return_key := base64Encode(sha)
	return return_key
}

func base64Encode(input []byte) []byte {
	encodeBuffer := make([]byte, base64.StdEncoding.EncodedLen(len(input)))
	base64.StdEncoding.Encode(encodeBuffer, input)
	return encodeBuffer
}
