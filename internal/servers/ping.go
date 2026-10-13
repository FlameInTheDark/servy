package servers

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/rumblefrog/go-a2s"
)

// PingInfo is the result of a server list ping.
type PingInfo struct {
	Online    int      `json:"online"`
	Max       int      `json:"max"`
	Players   []string `json:"players"`
	Version   string   `json:"version"`
	Desc      string   `json:"desc"`
	LatencyMS float64  `json:"latency_ms"`
	Icon      string   `json:"icon,omitempty"`
	Favicon   string   `json:"-"`
}

// PingServer probes a game server by type. Returns nil when unreachable.
// queryPort is only meaningful for A2S (Source) games; pass 0 to use the
// default game_port + 1 convention.
func PingServer(serverType, host string, port, queryPort int) *PingInfo {
	if host == "" || host == "127.0.0.1" || host == "localhost" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	start := time.Now()
	var info *PingInfo
	switch serverType {
	case "minecraft-bedrock", "raknet":
		info = pingRakNet(host, port)
	case "minecraft-java", "hytale":
		info = pingJava(host, port)
	case "steam_cmd", "valheim":
		// Steam/Source games use A2S. Most follow game_port + 1, but some
		// (Abiotic Factor) declare an explicit query port.
		if queryPort <= 0 {
			queryPort = port + 1
		}
		info = pingA2S(host, queryPort)
	default:
		// Default to Java ping for custom/unknown
		info = pingJava(host, port)
	}
	if info != nil {
		info.LatencyMS = float64(time.Since(start).Microseconds()) / 1000.0
	}
	return info
}

// ---------------- Java edition (status ping over TCP) ----------------

// javaStatusResponse mirrors the JSON status payload.
type javaStatusResponse struct {
	Version struct {
		Name     string `json:"name"`
		Protocol int    `json:"protocol"`
	} `json:"version"`
	Players struct {
		Max    int `json:"max"`
		Online int `json:"online"`
		Sample []struct {
			Name string `json:"name"`
		} `json:"sample"`
	} `json:"players"`
	Description struct {
		Text  string `json:"text"`
		Extra []struct {
			Text string `json:"text"`
		} `json:"extra"`
	} `json:"description"`
	Favicon string `json:"favicon"`
}

// pingJava performs the Minecraft Java status handshake/ping.
func pingJava(host string, port int) *PingInfo {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, fmt.Sprint(port)), 3*time.Second)
	if err != nil {
		return nil
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(4 * time.Second))

	// handshake packet: protocol=-1, host, port, next state=1 (status)
	handshake := &bytes.Buffer{}
	writeVarInt(handshake, 0x00) // packet id
	writeVarInt(handshake, 754)  // protocol version (ignored by server)
	writeString(handshake, host) // server address (with port for SRV)
	writeUnsignedShort(handshake, uint16(port))
	writeVarInt(handshake, 0x01) // next state: status
	if err := writeFrame(conn, handshake.Bytes()); err != nil {
		return nil
	}
	// status request (empty payload, id 0x00)
	if err := writeFrame(conn, []byte{0x00}); err != nil {
		return nil
	}
	// read framed response
	body, err := readFrame(conn)
	if err != nil || len(body) == 0 || body[0] != 0x00 {
		return nil
	}
	jsonLen, off := readVarInt(body[1:])
	if off <= 0 || 1+off+jsonLen > len(body) {
		return nil
	}
	var st javaStatusResponse
	if err := json.Unmarshal(body[1+off:1+off+jsonLen], &st); err != nil {
		return nil
	}
	info := &PingInfo{
		Online:  st.Players.Online,
		Max:     st.Players.Max,
		Version: st.Version.Name,
		Icon:    st.Favicon,
	}
	for _, s := range st.Players.Sample {
		if s.Name != "" {
			info.Players = append(info.Players, s.Name)
		}
	}
	var desc strings.Builder
	desc.WriteString(st.Description.Text)
	for _, e := range st.Description.Extra {
		desc.WriteString(e.Text)
	}
	info.Desc = strings.TrimSpace(desc.String())
	return info
}

func writeFrame(conn net.Conn, payload []byte) error {
	hdr := &bytes.Buffer{}
	writeVarInt(hdr, len(payload))
	if _, err := conn.Write(append(hdr.Bytes(), payload...)); err != nil {
		return err
	}
	return nil
}

func readFrame(conn net.Conn) ([]byte, error) {
	// length varint (up to 5 bytes)
	length := 0
	numRead := 0
	for {
		b := make([]byte, 1)
		if _, err := io.ReadFull(conn, b); err != nil {
			return nil, err
		}
		value := int(b[0] & 0x7F)
		length |= value << uint(7*numRead)
		numRead++
		if b[0]&0x80 == 0 || numRead >= 5 {
			break
		}
	}
	if length <= 0 || length > 1<<21 {
		return nil, fmt.Errorf("bad frame length %d", length)
	}
	buf := make([]byte, length)
	if _, err := io.ReadFull(conn, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

func writeVarInt(buf *bytes.Buffer, v int) {
	for {
		b := byte(v & 0x7F)
		v >>= 7
		if v != 0 {
			b |= 0x80
		}
		buf.WriteByte(b)
		if v == 0 {
			break
		}
	}
}

func readVarInt(b []byte) (int, int) {
	numRead := 0
	result := 0
	for numRead < len(b) && numRead < 5 {
		value := int(b[numRead] & 0x7F)
		result |= value << uint(7*numRead)
		numRead++
		if b[numRead-1]&0x80 == 0 {
			break
		}
	}
	return result, numRead
}

func writeString(buf *bytes.Buffer, s string) {
	writeVarInt(buf, len(s))
	buf.WriteString(s)
}

func writeUnsignedShort(buf *bytes.Buffer, v uint16) {
	_ = binary.Write(buf, binary.BigEndian, v)
}

// ---------------- Bedrock edition (RakNet unconnected ping) ----------------

// pingRakNet performs a RakNet open-connect-request ping (bedrock servers).
func pingRakNet(host string, port int) *PingInfo {
	addr := &net.UDPAddr{IP: net.ParseIP(host), Port: port}
	if addr.IP == nil {
		ips, err := net.LookupIP(host)
		if err != nil || len(ips) == 0 {
			return nil
		}
		addr.IP = ips[0]
	}
	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		return nil
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))

	// unconnected ping packet
	// 0x01 | time (8) | magic (16) | client guid (8)
	pkt := &bytes.Buffer{}
	pkt.WriteByte(0x01)
	_ = binary.Write(pkt, binary.BigEndian, time.Now().Unix())
	pkt.Write(raknetMagic)
	_ = binary.Write(pkt, binary.BigEndian, time.Now().UnixNano()) // client guid (arbitrary; echoed back)
	if _, err := conn.Write(pkt.Bytes()); err != nil {
		return nil
	}
	buf := make([]byte, 1500)
	n, err := conn.Read(buf)
	if err != nil || n < 35 {
		return nil
	}
	if buf[0] != 0x1c { // unconnected pong
		return nil
	}
	// pong: id(1) time(8) magic(16) strlen(2) string ...
	off := 1 + 8 + 16
	if off+2 > n {
		return nil
	}
	strLen := int(binary.BigEndian.Uint16(buf[off : off+2]))
	off += 2
	if off+strLen > n {
		return nil
	}
	payload := string(buf[off : off+strLen])
	// MOTD format: Edition;MOTD line 1;Protocol;Version;Players;Max;ServerID;Sub MOTD;GameType;...
	parts := strings.Split(payload, ";")
	info := &PingInfo{Players: []string{}}
	if len(parts) >= 6 {
		info.Online = atoiSafe(parts[4])
		info.Max = atoiSafe(parts[5])
	}
	if len(parts) >= 4 {
		info.Version = parts[3]
	}
	if len(parts) >= 2 {
		info.Desc = parts[1]
	}
	return info
}

var raknetMagic = []byte{
	0x00, 0xff, 0xff, 0x00, 0xfe, 0xfe, 0xfe, 0xfe,
	0xfd, 0xfd, 0xfd, 0xfd, 0x12, 0x34, 0x56, 0x78,
}

func atoiSafe(s string) int {
	v := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return v
		}
		v = v*10 + int(c-'0')
	}
	return v
}

// ---------------- Steam A2S (Source Query) ----------------
// Used by Valheim, CS2, TF2, GMod, Rust, and other Steam/Source games.
// Query port = game port + 1 (e.g., Valheim 2456 -> 2457)

// pingA2S queries a Steam/Source game server using the go-a2s library.
func pingA2S(host string, queryPort int) *PingInfo {
	addr := net.JoinHostPort(host, fmt.Sprint(queryPort))
	client, err := a2s.NewClient(addr)
	if err != nil {
		return nil
	}
	defer client.Close()

	start := time.Now()

	info, err := client.QueryInfo()
	if err != nil {
		return nil
	}

	// Also try to get players
	var playerNames []string
	if players, err := client.QueryPlayer(); err == nil {
		for _, p := range players.Players {
			if p.Name != "" {
				playerNames = append(playerNames, p.Name)
			}
		}
	}

	// Build description from extended info (Valheim world data) if available
	desc := ""
	if info.ExtendedServerInfo != nil {
		desc = info.ExtendedServerInfo.Keywords
	}
	if desc == "" {
		desc = fmt.Sprintf("%s on %s", info.Game, info.Map)
	}

	return &PingInfo{
		Online:    int(info.Players),
		Max:       int(info.MaxPlayers),
		Players:   playerNames,
		Version:   info.Version,
		Desc:      desc,
		LatencyMS: float64(time.Since(start).Microseconds()) / 1000.0,
	}
}