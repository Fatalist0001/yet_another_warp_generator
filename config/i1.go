package config

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	crand "crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math/rand"
	"sort"
	"strings"
)

// I1 profile names. I1 in AmneziaWG is a chain of keep/randomize directives that
// makes the handshake look like another protocol (`<b 0x...>` keeps bytes, `<r n>`
// inserts n random bytes).
const (
	I1ProfileRandom = "random"
	I1ProfileQUIC   = "quic"
	I1ProfileDNS    = "dns"
	I1ProfileSIP    = "sip"
	I1ProfileSTUN   = "stun"
)

// I1DefaultHosts are the SNI/host names used when the caller does not pick one.
var I1DefaultHosts = []string{
	"www.apple.com",
	"www.google.com",
	"www.microsoft.com",
	"cdn.jsdelivr.net",
}

// i1StaticDefault is a fixed, always-valid DNS-mimicking chain. Used as a fallback
// when generation fails, and as the NewConfig() default so a Config is usable
// without a generator call.
const i1StaticDefault = "<r 2><b 0x858000010001000000000669636c6f756403636f6d0000010001c00c000100010000105a00044d583737>"

var i1Generators = map[string]func(host string) (string, error){
	I1ProfileQUIC: func(host string) (string, error) {
		chain, _, err := genQUICI1(host)
		return chain, err
	},
	I1ProfileDNS:  genDNSI1,
	I1ProfileSIP:  genSIPI1,
	I1ProfileSTUN: genSTUNI1,
}

// I1Profiles returns the sorted list of available I1 profile names.
func I1Profiles() []string {
	names := make([]string, 0, len(i1Generators))
	for name := range i1Generators {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// IsI1Profile reports whether name is a known I1 profile.
func IsI1Profile(name string) bool {
	if name == I1ProfileRandom {
		return true
	}
	_, ok := i1Generators[name]
	return ok
}

// GenI1 builds an I1 chain for the given profile. An empty profile or
// I1ProfileRandom picks one at random; an empty host picks a random host from
// I1DefaultHosts. label is a human-readable description ("dns(xbox.com)").
func GenI1(profile, host string) (chain, label string, err error) {
	if profile == "" || profile == I1ProfileRandom {
		names := I1Profiles()
		profile = names[randRange(0, len(names)-1)]
	}
	gen, ok := i1Generators[profile]
	if !ok {
		return "", "", fmt.Errorf("неизвестный профиль I1 %q (доступны: %s)", profile, strings.Join(I1Profiles(), "|"))
	}
	if host == "" {
		host = I1DefaultHosts[randRange(0, len(I1DefaultHosts)-1)]
	}
	chain, err = gen(host)
	if err != nil {
		return "", "", err
	}
	return chain, fmt.Sprintf("%s(%s)", profile, host), nil
}

// DefaultI1 returns a freshly generated QUIC-mimicking I1 chain, falling back to
// the static DNS chain if generation fails.
func DefaultI1() string {
	if chain, _, err := GenI1(I1ProfileQUIC, ""); err == nil && chain != "" {
		return chain
	}
	return i1StaticDefault
}

func genDNSI1(host string) (string, error) {
	qname, err := dnsName(host)
	if err != nil {
		return "", err
	}
	question := append(qname, 0, 1, 0, 1)

	padLen := randRange(dnsPadMin, dnsPadMax)
	opt := []byte{0}                               // root name
	opt = binary.BigEndian.AppendUint16(opt, 41)   // OPT
	opt = binary.BigEndian.AppendUint16(opt, 4096) // UDP payload size
	opt = append(opt, 0, 0, 0, 0)                  // extended rcode and flags
	opt = binary.BigEndian.AppendUint16(opt, uint16(padLen+4))
	opt = binary.BigEndian.AppendUint16(opt, 12) // padding option
	opt = binary.BigEndian.AppendUint16(opt, uint16(padLen))

	c := &cpsChain{}
	c.tag("r", 2) // transaction id
	c.bytes([]byte{0x01, 0x00, 0, 1, 0, 0, 0, 0, 0, 1})
	c.bytes(question)
	c.bytes(opt)
	c.tag("r", padLen)
	return c.String(), nil
}

func dnsName(host string) ([]byte, error) {
	var out []byte
	for _, label := range strings.Split(strings.Trim(host, "."), ".") {
		if label == "" || len(label) > dnsLabelMax {
			return nil, fmt.Errorf("плохая DNS-метка %q в %q", label, host)
		}
		out = append(out, byte(len(label)))
		out = append(out, label...)
	}
	return append(out, 0), nil
}

func genSIPI1(host string) (string, error) {
	c := &cpsChain{}
	c.text("OPTIONS sip:" + host + " SIP/2.0\r\n")
	c.text(fmt.Sprintf("Via: SIP/2.0/UDP 192.168.%d.%d:5060;branch=z9hG4bK", randRange(0, 255), randRange(2, 254)))
	c.tag("rc", 10)
	c.text("\r\nFrom: <sip:")
	c.tag("rc", 8)
	c.text("@" + host + ">;tag=")
	c.tag("rd", 6)
	c.text("\r\nTo: <sip:" + host + ">\r\nCall-ID: ")
	c.tag("rc", 16)
	c.text("@" + host + "\r\nCSeq: ")
	c.tag("rd", 4)
	c.text(" OPTIONS\r\nMax-Forwards: 70\r\nUser-Agent: PJSIP/2.13\r\nContent-Length: 0\r\n\r\n")
	return c.String(), nil
}

func genSTUNI1(host string) (string, error) {
	softwareLen := randRange(stunSoftwareMin/4, stunSoftwareMax/4) * 4
	attrLen := 4 + softwareLen

	c := &cpsChain{}
	header := binary.BigEndian.AppendUint16(nil, 0x0001) // Binding Request
	header = binary.BigEndian.AppendUint16(header, uint16(attrLen))
	header = binary.BigEndian.AppendUint32(header, 0x2112A442) // magic cookie
	c.bytes(header)
	c.tag("r", 12) // transaction id
	software := binary.BigEndian.AppendUint16(nil, 0x8022)
	software = binary.BigEndian.AppendUint16(software, uint16(softwareLen))
	c.bytes(software)
	c.tag("rc", softwareLen)
	return c.String(), nil
}

const (
	dnsLabelMax     = 63
	dnsPadMin       = 60
	dnsPadMax       = 180
	stunSoftwareMin = 16
	stunSoftwareMax = 32
)

// cpsChain renders AmneziaWG client-packet smuggle directives.
type cpsChain struct {
	sb strings.Builder
}

func (c *cpsChain) bytes(b []byte) {
	if len(b) > 0 {
		fmt.Fprintf(&c.sb, "<b 0x%x>", b)
	}
}

func (c *cpsChain) text(s string) {
	c.bytes([]byte(s))
}

func (c *cpsChain) tag(name string, n int) {
	fmt.Fprintf(&c.sb, "<%s %d>", name, n)
}

func (c *cpsChain) String() string {
	return c.sb.String()
}

func randRange(lo, hi int) int {
	if hi <= lo {
		return lo
	}
	return lo + rand.Intn(hi-lo+1)
}

// --- QUIC Initial packet mimicry (ported from warpscout, MIT) ---

const (
	quicVersion1     = 1
	quicTagLen       = 16
	quicHelloRandLen = 32
	quicSampleEnd    = 20
	tlsHandshakeHead = 6
)

var quicInitialSalt = []byte{
	0x38, 0x76, 0x2c, 0xf7, 0xf5, 0x59, 0x34, 0xb3, 0x4d, 0x17,
	0x9a, 0xe6, 0xa4, 0xc8, 0x0c, 0xad, 0xcc, 0xbb, 0x7f, 0x0a,
}

func genQUICI1(sni string) (string, []byte, error) {
	dcid := randomBytes(1)
	pkn := []byte{0}
	hello := tlsClientHello(sni)
	payload := quicCryptoFrame(hello)

	packet, headerLen, err := quicInitialPacket(dcid, pkn, payload)
	if err != nil {
		return "", nil, err
	}
	parts := quicCutParts(len(payload)-len(hello), len(hello), len(pkn), headerLen)
	return quicToCPS(packet, parts), packet, nil
}

func quicInitialPacket(dcid, pkn, payload []byte) ([]byte, int, error) {
	header := []byte{0xC0 | byte(len(pkn)-1)}
	header = binary.BigEndian.AppendUint32(header, quicVersion1)
	header = append(header, quicStr8(dcid)...)
	header = append(header, 0, 0) // empty SCID, empty token
	header = append(header, quicVarint(len(pkn)+len(payload)+quicTagLen)...)
	header = append(header, pkn...)
	headerLen := len(header)

	key, iv, hp := quicInitialKeys(dcid)
	for i := range pkn {
		iv[len(iv)-len(pkn)+i] ^= pkn[i]
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, 0, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, 0, err
	}
	ciphertext := gcm.Seal(nil, iv, payload, header)

	mask, err := quicHeaderMask(hp, ciphertext[4-len(pkn):quicSampleEnd-len(pkn)])
	if err != nil {
		return nil, 0, err
	}
	header[0] ^= mask[0] & 0x0f
	for i := range pkn {
		header[headerLen-len(pkn)+i] ^= mask[1+i]
	}
	return append(header, ciphertext...), headerLen, nil
}

func quicInitialKeys(dcid []byte) (key, iv, hp []byte) {
	initial := quicHMAC(quicInitialSalt, dcid)
	client := quicExpandLabel(initial, "client in", sha256.Size)
	return quicExpandLabel(client, "quic key", 16),
		quicExpandLabel(client, "quic iv", 12),
		quicExpandLabel(client, "quic hp", 16)
}

func quicExpandLabel(secret []byte, label string, length int) []byte {
	info := binary.BigEndian.AppendUint16(nil, uint16(length))
	info = append(info, quicStr8([]byte("tls13 "+label))...)
	info = append(info, 0, 1)
	return quicHMAC(secret, info)[:length]
}

func quicHMAC(key, data []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(data)
	return m.Sum(nil)
}

func quicHeaderMask(hp, sample []byte) ([]byte, error) {
	block, err := aes.NewCipher(hp)
	if err != nil {
		return nil, err
	}
	mask := make([]byte, aes.BlockSize)
	block.Encrypt(mask, sample)
	return mask, nil
}

func quicCryptoFrame(data []byte) []byte {
	frame := []byte{0x06}
	frame = append(frame, quicVarint(0)...)
	frame = append(frame, quicVarint(len(data))...)
	return append(frame, data...)
}

func tlsClientHello(sni string) []byte {
	name := []byte(sni)
	serverName := binary.BigEndian.AppendUint16(nil, uint16(len(name)+3))
	serverName = append(serverName, 0)
	serverName = binary.BigEndian.AppendUint16(serverName, uint16(len(name)))
	serverName = append(serverName, name...)

	ext := binary.BigEndian.AppendUint16(nil, 0) // server_name
	ext = binary.BigEndian.AppendUint16(ext, uint16(len(serverName)))
	ext = append(ext, serverName...)

	body := []byte{0x03, 0x03}
	body = append(body, randomBytes(quicHelloRandLen)...)
	body = append(body, 0, 0, 0, 0) // no session id, no cipher suites, no compression
	body = binary.BigEndian.AppendUint16(body, uint16(len(ext)))
	body = append(body, ext...)

	hello := []byte{0x01, byte(len(body) >> 16), byte(len(body) >> 8), byte(len(body))}
	return append(hello, body...)
}

func quicCutParts(frameHeaderLen, helloLen, pknLen, headerLen int) []int {
	keep := frameHeaderLen + tlsHandshakeHead
	randomized := quicHelloRandLen
	if short := quicSampleEnd - pknLen - keep; short > 0 {
		keep += short
		randomized -= short
	}
	return []int{headerLen + keep, randomized, helloLen - tlsHandshakeHead - quicHelloRandLen, quicTagLen}
}

func quicToCPS(packet []byte, parts []int) string {
	c := &cpsChain{}
	offset, keep := 0, true
	for _, n := range parts {
		if n > 0 && keep {
			c.bytes(packet[offset : offset+n])
		} else if n > 0 {
			c.tag("r", n)
		}
		offset += n
		keep = !keep
	}
	return c.String()
}

func quicVarint(x int) []byte {
	switch {
	case x < 0x40:
		return []byte{byte(x)}
	case x < 0x4000:
		b := binary.BigEndian.AppendUint16(nil, uint16(x))
		b[0] |= 0x40
		return b
	default:
		b := binary.BigEndian.AppendUint32(nil, uint32(x))
		b[0] |= 0x80
		return b
	}
}

func quicStr8(b []byte) []byte {
	return append([]byte{byte(len(b))}, b...)
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := crand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand: %v", err))
	}
	return b
}
