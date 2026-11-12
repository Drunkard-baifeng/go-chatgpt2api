package backup

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"io"
)

// 加密格式沿用 OpenSSL enc：Salted__、8字节随机盐、AES-256-CBC、
// PBKDF2-HMAC-SHA256/10000和PKCS#7填充。恢复时显式指定-saltlen 8。
func encrypt(ctx context.Context, destination io.Writer, source io.Reader, passphrase string) error {
	if passphrase == "" {
		return ErrInput
	}
	salt := make([]byte, 8)
	if _, err := rand.Read(salt); err != nil {
		return ErrDump
	}
	key, err := pbkdf2.Key(sha256.New, passphrase, salt, 10000, 48)
	if err != nil {
		return ErrDump
	}
	block, err := aes.NewCipher(key[:32])
	if err != nil {
		return ErrDump
	}
	mode := cipher.NewCBCEncrypter(block, key[32:])
	if _, err = destination.Write(append([]byte("Salted__"), salt...)); err != nil {
		return err
	}
	reader := &contextReader{ctx: ctx, reader: source}
	buffer := make([]byte, 64<<10)
	for {
		n, readErr := io.ReadFull(reader, buffer)
		if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
			return readErr
		}
		if readErr != nil {
			padding := aes.BlockSize - (n % aes.BlockSize)
			for i := 0; i < padding; i++ {
				buffer[n+i] = byte(padding)
			}
			n += padding
		}
		mode.CryptBlocks(buffer[:n], buffer[:n])
		if _, err = destination.Write(buffer[:n]); err != nil {
			return err
		}
		if readErr != nil {
			return nil
		}
	}
}
