package main

import (
	"bufio"
	"bytes"
	_ "embed"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// The region definitions and ability list are taken from the MIT licensed smash-amiibo-editor
// project: https://github.com/jozz024/smash-amiibo-editor
// The offsets in the region file are relative to the amiitool (internal) data layout.
//
//go:embed resources/ssbu_regions.json
var ssbuRegionsJson []byte

//go:embed resources/ssbu_abilities.txt
var ssbuAbilitiesTxt []byte

//go:embed resources/ssbu_defaults.json
var ssbuDefaultsJson []byte

// ssbuOption is a single named value for an enum or ability region.
type ssbuOption struct {
	name  string
	value uint64
}

// ssbuRegion describes one editable parameter of an SSBU figure player.
type ssbuRegion struct {
	name        string
	typ         string
	description string
	start       int // Byte offset in the amiitool (internal) layout.
	bitStart    int // Bit offset in the start byte, 0 being the most significant bit.
	length      int // Length in bits.
	options     []ssbuOption
	legalMin    *int64 // Overrides the storage minimum with the minimum legal in game.
	legalMax    *int64 // Overrides the storage maximum with the maximum legal in game.
	def         int64  // The raw value of a brand new in game figure player.
}

// ssbuRegions holds all editable region definitions, parsed once on first use.
var ssbuRegions []ssbuRegion

// ssbuAbilityCost holds the support slot cost per ability value. A figure player has
// ssbuAbilitySlots support slots in total and every equipped ability occupies 1 to 3 of them.
var ssbuAbilityCost map[uint64]int64

// ssbuAbilitySlots is the total number of support slots of a figure player.
const ssbuAbilitySlots = 3

// Stat budgets: the total of the attack and defense stats a figure player can legally reach is
// 5000 with no abilities equipped and 4200 with all three slots filled. The exact budget for one
// or two occupied slots is not reliably documented, so the conservative 4200 is used as soon as
// any ability is equipped: that is always legal.
const (
	ssbuStatBudgetFree      = 5000
	ssbuStatBudgetAbilities = 4200
)

// ssbuLevelExpMax is the experience at level 50, the level cap of a figure player.
const ssbuLevelExpMax = 3912

// loadSSBURegions parses the embedded region and ability definitions. Region types that are
// covered by other amiigo features or are display only derivations are skipped: text (see the
// nickname action), character (identity transplants) and implicitsum (derived values).
func loadSSBURegions() error {
	if ssbuRegions != nil {
		return nil
	}

	abilities := []ssbuOption{{name: "None", value: 0}}
	ssbuAbilityCost = map[uint64]int64{0: 0}
	s := bufio.NewScanner(bytes.NewReader(ssbuAbilitiesTxt))
	for i := uint64(1); s.Scan(); i++ {
		name := strings.TrimSpace(s.Text())
		abilities = append(abilities, ssbuOption{name: name, value: i})
		ssbuAbilityCost[i] = abilityCost(name)
	}

	var raw struct {
		Regions []struct {
			Name        string            `json:"name"`
			Type        string            `json:"type"`
			Description string            `json:"description"`
			Start       string            `json:"start"`
			BitStart    int               `json:"bit_start_location"`
			Length      int               `json:"length"`
			Options     map[string]uint64 `json:"options"`
		} `json:"regions"`
	}
	if err := json.Unmarshal(ssbuRegionsJson, &raw); err != nil {
		return err
	}

	for _, r := range raw.Regions {
		switch r.Type {
		case "percentage", "enum", "ability", "signed", "unsigned":
		default:
			continue
		}

		start, err := strconv.ParseUint(strings.TrimPrefix(r.Start, "0x"), 16, 32)
		if err != nil {
			return fmt.Errorf("region %s: %s", r.Name, err)
		}

		reg := ssbuRegion{
			name:        titleCase(r.Name),
			typ:         r.Type,
			description: r.Description,
			start:       int(start),
			bitStart:    r.BitStart,
			length:      r.Length,
		}

		switch r.Type {
		case "ability":
			reg.options = abilities
		case "enum":
			for n, v := range r.Options {
				reg.options = append(reg.options, ssbuOption{name: n, value: v})
			}
			sort.Slice(reg.options, func(i, j int) bool { return reg.options[i].value < reg.options[j].value })
		}

		// Ranges legal in game, narrower than what the fields can store.
		switch reg.name {
		case "Level Experience":
			reg.legalMin, reg.legalMax = i64(0), i64(ssbuLevelExpMax)
		case "Attack Stat", "Defense Stat":
			reg.legalMin, reg.legalMax = i64(0), i64(ssbuStatBudgetFree)
		}

		// Learning is on for a brand new figure player; everything else defaults to zero unless
		// the defaults template below says otherwise.
		if reg.name == "Learning" {
			reg.def = 1
		}

		ssbuRegions = append(ssbuRegions, reg)
	}

	return loadSSBUDefaults()
}

// loadSSBUDefaults parses the embedded defaults template, holding the behaviour percentages of a
// brand new in game figure player. The keys are section signatures in the form
// "percentage-<start>-<length>-<bit start>" with a percentage as value.
func loadSSBUDefaults() error {
	var defs map[string]string
	if err := json.Unmarshal(ssbuDefaultsJson, &defs); err != nil {
		return err
	}

	for i := range ssbuRegions {
		r := &ssbuRegions[i]
		if r.typ != "percentage" {
			continue
		}
		v, ok := defs[fmt.Sprintf("percentage-%d-%d-%d", r.start, r.length, r.bitStart)]
		if !ok {
			continue
		}
		pct, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return fmt.Errorf("default for %s: %s", r.name, err)
		}
		_, max := r.bounds()
		r.def = int64(pct/100*float64(max) + 0.5)
	}

	return nil
}

// i64 returns a pointer to the given value.
func i64(v int64) *int64 {
	return &v
}

// ssbuRegionByName returns the parsed region with the given name, or nil when not found.
func ssbuRegionByName(name string) *ssbuRegion {
	for i := range ssbuRegions {
		if ssbuRegions[i].name == name {
			return &ssbuRegions[i]
		}
	}
	return nil
}

// abilityCost extracts the support slot cost from an ability name, which carries a suffix like
// "(L 1)" or "(U 3)". An unknown cost ("?") is treated as the maximum to stay conservative.
func abilityCost(name string) int64 {
	open := strings.LastIndex(name, "(")
	if open == -1 {
		return ssbuAbilitySlots
	}
	f := strings.Fields(strings.Trim(name[open:], "()"))
	if len(f) != 2 {
		return ssbuAbilitySlots
	}
	c, err := strconv.ParseInt(f[1], 10, 8)
	if err != nil || c < 1 || c > ssbuAbilitySlots {
		return ssbuAbilitySlots
	}
	return c
}

// titleCase uppercases the first letter of every space separated word.
func titleCase(s string) string {
	words := strings.Fields(s)
	for i, w := range words {
		r := []rune(w)
		if r[0] >= 'a' && r[0] <= 'z' {
			r[0] -= 32
		}
		words[i] = string(r)
	}
	return strings.Join(words, " ")
}

// getBits reads length bits from d starting at byte index start, bit index bitStart where bit 0
// is the most significant bit of the byte. With reverse false the first bit read is the most
// significant bit of the result and reading continues towards the least significant bit of the
// byte. With reverse true the first bit read is the least significant bit of the result and
// reading continues towards the most significant bit, wrapping to the least significant bit of
// the next byte. This matches the two packings used by the SSBU region definitions.
func getBits(d []byte, start, bitStart, length int, reverse bool) uint64 {
	var out uint64
	i := bitStart
	b := start

	if reverse {
		for n := 0; n < length; n++ {
			out |= uint64(d[b]>>(7-i)&1) << n
			i--
			if i == -1 {
				i = 7
				b++
			}
		}
		return out
	}

	for n := 0; n < length; n++ {
		out = out<<1 | uint64(d[b]>>(7-i)&1)
		i++
		if i == 8 {
			i = 0
			b++
		}
	}
	return out
}

// setBits writes length bits of value into d using the same addressing as getBits.
func setBits(d []byte, start, bitStart, length int, value uint64, reverse bool) {
	i := bitStart
	b := start

	for n := 0; n < length; n++ {
		var bit byte
		if reverse {
			bit = byte(value >> n & 1)
		} else {
			bit = byte(value >> (length - 1 - n) & 1)
		}

		mask := byte(1) << (7 - i)
		d[b] = d[b]&^mask | bit<<(7-i)

		if reverse {
			i--
			if i == -1 {
				i = 7
				b++
			}
		} else {
			i++
			if i == 8 {
				i = 0
				b++
			}
		}
	}
}

// rawValue reads the current raw value of the region from the internal layout data.
func (r *ssbuRegion) rawValue(d []byte) int64 {
	switch r.typ {
	case "percentage":
		return int64(getBits(d, r.start, r.bitStart, r.length, true))
	case "enum", "ability":
		return int64(getBits(d, r.start, r.bitStart, r.length, false))
	case "unsigned":
		return int64(binary.LittleEndian.Uint16(d[r.start : r.start+2]))
	case "signed":
		return int64(int16(binary.LittleEndian.Uint16(d[r.start : r.start+2])))
	}
	return 0
}

// setRawValue writes a raw value for the region into the internal layout data, clamped to the
// region bounds.
func (r *ssbuRegion) setRawValue(d []byte, v int64) {
	min, max := r.bounds()
	if v < min {
		v = min
	}
	if v > max {
		v = max
	}

	switch r.typ {
	case "percentage":
		setBits(d, r.start, r.bitStart, r.length, uint64(v), true)
	case "enum", "ability":
		setBits(d, r.start, r.bitStart, r.length, uint64(v), false)
	case "unsigned":
		binary.LittleEndian.PutUint16(d[r.start:r.start+2], uint16(v))
	case "signed":
		binary.LittleEndian.PutUint16(d[r.start:r.start+2], uint16(int16(v)))
	}
}

// bounds returns the minimum and maximum raw value for the region. When the region has known
// legal limits narrower than what the field can store, those are returned instead.
func (r *ssbuRegion) bounds() (int64, int64) {
	var min, max int64
	switch r.typ {
	case "signed":
		min, max = -(1 << (r.length - 1)), 1<<(r.length-1)-1
	case "enum", "ability":
		min, max = 0, int64(r.options[len(r.options)-1].value)
	default:
		min, max = 0, 1<<r.length-1
	}

	if r.legalMin != nil && *r.legalMin > min {
		min = *r.legalMin
	}
	if r.legalMax != nil && *r.legalMax < max {
		max = *r.legalMax
	}

	return min, max
}

// display formats the current value of the region for the UI.
func (r *ssbuRegion) display(d []byte) string {
	v := r.rawValue(d)

	switch r.typ {
	case "percentage":
		_, max := r.bounds()
		return fmt.Sprintf("%.1f%%", float64(v)/float64(max)*100)
	case "enum", "ability":
		for _, o := range r.options {
			if int64(o.value) == v {
				return o.name
			}
		}
		return fmt.Sprintf("0x%02X", v)
	}
	return strconv.FormatInt(v, 10)
}

// step changes the region value by n steps. Enums and abilities cycle through their options,
// numeric regions add n.
func (r *ssbuRegion) step(d []byte, n int64) {
	v := r.rawValue(d)

	if r.typ == "enum" || r.typ == "ability" {
		i := 0
		for j, o := range r.options {
			if int64(o.value) == v {
				i = j
				break
			}
		}
		i += int(n)
		for i < 0 {
			i += len(r.options)
		}
		r.setRawValue(d, int64(r.options[i%len(r.options)].value))
		return
	}

	r.setRawValue(d, v+n)
}
