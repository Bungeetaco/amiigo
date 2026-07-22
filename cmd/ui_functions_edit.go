package main

import (
	"fmt"

	"github.com/malc0mn/amiigo/amiibo"
)

// modifyAmiibo applies a mutation to the active amiibo. It works on a copy which is decrypted
// when needed, mutated, re-encrypted and then published as the new active amiibo, so the result
// is always ready to save to disk or write to a token. The original amiibo is never touched, so
// nothing is lost when a step fails.
func modifyAmiibo(am *amb, log chan<- []byte, verb string, mutate func(a amiibo.Amiidump) error) bool {
	if am == nil || am.a == nil {
		log <- encodeStringCell("Cannot " + verb + ": no amiibo data!")
		return false
	}
	if conf.retailKey == nil {
		log <- encodeStringCell("Cannot " + verb + ": no retail key loaded!")
		return false
	}

	cp, err := amiibo.NewAmiidump(am.a.Raw(), am.a.Type())
	if err != nil {
		log <- encodeStringCell("Cannot " + verb + ": " + err.Error())
		return false
	}

	dec := cp
	if !am.dec {
		if dec, err = amiibo.Decrypt(conf.retailKey, cp); err != nil {
			log <- encodeStringCell("Cannot " + verb + ", decryption error: " + err.Error())
			return false
		}
	}

	if err := mutate(dec); err != nil {
		log <- encodeStringCell("Cannot " + verb + ": " + err.Error())
		return false
	}

	log <- encodeStringCell("Amiibo " + verb + " successful, re-encrypted and ready to save or write")
	amiiboChan <- newAmiibo(amiibo.Encrypt(conf.retailKey, dec), false)

	return true
}

// setNickname sets a new nickname on the active amiibo.
func setNickname(name string, am *amb, log chan<- []byte) bool {
	if name == "" {
		log <- encodeStringCell("Please provide a nickname!")
		return false
	}

	return modifyAmiibo(am, log, "nickname change", func(a amiibo.Amiidump) error {
		return amiibo.SetNickname(a, name)
	})
}

// applyAppData replaces the gameplay data of the active amiibo with the given block.
func applyAppData(data []byte, am *amb, log chan<- []byte) bool {
	return modifyAmiibo(am, log, "gameplay data edit", func(a amiibo.Amiidump) error {
		if err := amiibo.SetAppData(a, data); err != nil {
			return err
		}
		if amiibo.HasSSBUData(a) {
			log <- encodeStringCell("SSBU data detected: gameplay data checksum updated")
		}
		return nil
	})
}

// applyFPEdit replaces the register info and settings of the active amiibo with the state built
// by the figure player editor, given in the amiitool (internal) layout, and refreshes the SSBU
// application data checksum.
func applyFPEdit(internal []byte, am *amb, log chan<- []byte) bool {
	return modifyAmiibo(am, log, "figure player edit", func(a amiibo.Amiidump) error {
		t, err := amiibo.NewAmiitool(internal, nil)
		if err != nil {
			return err
		}

		var src amiibo.Amiidump = t
		if a.Type() == amiibo.TypeAmiibo {
			src = amiibo.AmiitoolToAmiibo(t)
		}
		a.SetRegisterInfo(src.RegisterInfoRaw())
		a.SetSettings(src.SettingsRaw())
		amiibo.FixAppDataChecksum(a)

		return nil
	})
}

// resetAppData wipes all gameplay data from the active amiibo after confirmation. It is an
// optionsSubmitHandler, the return value ends up in the token write channel which ignores nil.
func resetAppData(_ int, am *amb, log chan<- []byte) []byte {
	modifyAmiibo(am, log, "gameplay data reset", func(a amiibo.Amiidump) error {
		if amiibo.HasSSBUData(a) {
			log <- encodeStringCell(fmt.Sprintf("Wiping %d bytes of SSBU gameplay data", amiibo.AppDataSize))
		}
		amiibo.ClearAppData(a)
		return nil
	})

	return nil
}
