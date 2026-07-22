package amiibo

import (
	"bytes"
	"encoding/binary"
	"errors"
	"unicode/utf16"
)

// AppDataSize is the size of the application (gameplay) data block in bytes.
const AppDataSize = 216

// Offsets within the 32 byte register info block.
const (
	riFlags        = 0
	riNickname     = 12
	riNicknameSize = 20
)

// Offsets within the 360 byte settings block.
const (
	setTitleID  = 96
	setAppID    = 106
	setAppData  = 144
)

// appDataInitialized is the register info flag bit indicating the amiibo holds game data.
const appDataInitialized = 1 << 5

// ssbuAppID is the application ID Super Smash Bros. Ultimate stores on an amiibo.
var ssbuAppID = []byte{0x34, 0xf8, 0x02, 0x00}

// SetNickname writes a new nickname to a DECRYPTED amiibo. The name is stored as UTF-16 big
// endian in a 20 byte field, so a maximum of 10 basic characters.
// Note that the settings CRC is deliberately not touched: it is calculated with a console unique
// hash and the console rewrites it on the next appdata write when it does not match.
func SetNickname(a Amiidump, name string) error {
	buf := &bytes.Buffer{}
	if err := binary.Write(buf, binary.BigEndian, utf16.Encode([]rune(name))); err != nil {
		return err
	}
	if buf.Len() > riNicknameSize {
		return errors.New("amiibo: nickname too long, max 10 characters")
	}

	ri := a.RegisterInfoRaw()
	nick := ri[riNickname : riNickname+riNicknameSize]
	for i := range nick {
		nick[i] = 0
	}
	copy(nick, buf.Bytes())
	a.SetRegisterInfo(ri)

	return nil
}

// SetAppData replaces the application (gameplay) data block of a DECRYPTED amiibo. When the
// amiibo holds Super Smash Bros. Ultimate data, the checksum SSBU keeps in the first four bytes
// of the block is recalculated so the game will accept the modified data.
func SetAppData(a Amiidump, data []byte) error {
	if len(data) != AppDataSize {
		return errors.New("amiibo: app data must be exactly 216 bytes")
	}

	s := a.SettingsRaw()
	copy(s[setAppData:setAppData+AppDataSize], data)
	if bytes.Equal(s[setAppID:setAppID+4], ssbuAppID) {
		fixSSBUChecksum(s)
	}
	a.SetSettings(s)

	return nil
}

// HasSSBUData returns true when the DECRYPTED amiibo holds Super Smash Bros. Ultimate game data.
func HasSSBUData(a Amiidump) bool {
	s := a.SettingsRaw()
	return bytes.Equal(s[setAppID:setAppID+4], ssbuAppID)
}

// AppData returns the application (gameplay) data block of a DECRYPTED amiibo.
func AppData(a Amiidump) []byte {
	return a.Settings().ApplicationData()
}

// ClearAppData wipes all application (gameplay) data from a DECRYPTED amiibo: the title ID,
// application ID and application data are zeroed and the 'appdata initialized' flag is cleared,
// so a game will treat the amiibo as brand new. The owner Mii, nickname and register info are
// left untouched.
func ClearAppData(a Amiidump) {
	s := a.SettingsRaw()
	for i := setTitleID; i < setTitleID+8; i++ {
		s[i] = 0
	}
	for i := setAppID; i < setAppID+4; i++ {
		s[i] = 0
	}
	for i := setAppData; i < setAppData+AppDataSize; i++ {
		s[i] = 0
	}
	a.SetSettings(s)

	ri := a.RegisterInfoRaw()
	ri[riFlags] &^= appDataInitialized
	a.SetRegisterInfo(ri)
}

// fixSSBUChecksum recalculates the CRC32 Super Smash Bros. Ultimate keeps in the first four
// bytes of the application data, covering the remaining 212 bytes.
func fixSSBUChecksum(s []byte) {
	crc := legacyCrc32(s[setAppData+4 : setAppData+AppDataSize])
	binary.LittleEndian.PutUint32(s[setAppData:setAppData+4], crc)
}

// legacyCrc32 implements the CRC32 variant used for SSBU amiibo application data: the reflected
// 0xEDB88320 polynomial with a zero initial value and a final complement. Note that this differs
// from the standard IEEE CRC32 which starts from 0xFFFFFFFF, so hash/crc32 cannot be used.
func legacyCrc32(data []byte) uint32 {
	var table [256]uint32
	for i := 1; i < 256; i++ {
		t := uint32(i)
		for j := 0; j < 8; j++ {
			if t&1 == 1 {
				t = t>>1 ^ 0xEDB88320
			} else {
				t >>= 1
			}
		}
		table[i] = t
	}

	var t uint32
	for _, k := range data {
		t = t>>8 ^ table[byte(t)^k]
	}

	return ^t
}
