package fsplug

import (
	"errors"
	"fmt"
	"net"
	"sync"

	"github.com/pion/rtp"
	"github.com/shridarpatil/tring/internal/media/room"
)

// mediaSession owns the plain-RTP UDP socket for one call leg. Inbound Opus
// packets are queued for ReadRTP; telephone-event packets are decoded to DTMF.
// Outbound room audio is re-stamped onto one clean stream via write. It
// implements room.ParticipantSource.
type mediaSession struct {
	id     room.ParticipantID
	conn   *net.UDPConn
	opusPT uint8
	dtmfPT uint8

	in    chan *rtp.Packet // inbound Opus, drained by ReadRTP
	dtmfC chan string
	doneC chan struct{}

	mu     sync.Mutex
	remote *net.UDPAddr
	closed bool

	// outbound re-stamp state
	outSSRC uint32
	outSeq  uint16
	outTS   uint32
}

func newMediaSession(id room.ParticipantID, listenIP string, portMin, portMax int, opusPT, dtmfPT uint8) (*mediaSession, error) {
	var conn *net.UDPConn
	var err error
	for p := portMin; p <= portMax; p++ {
		conn, err = net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP(listenIP), Port: p})
		if err == nil {
			break
		}
	}
	if conn == nil {
		return nil, fmt.Errorf("fsplug: no free RTP port in %d-%d: %w", portMin, portMax, err)
	}
	m := &mediaSession{
		id: id, conn: conn, opusPT: opusPT, dtmfPT: dtmfPT,
		in: make(chan *rtp.Packet, 256), dtmfC: make(chan string, 8), doneC: make(chan struct{}),
		outSSRC: 0x5052494e, // "PRIN" — stable per-session SSRC
	}
	go m.readLoop()
	return m, nil
}

func (m *mediaSession) localPort() int         { return m.conn.LocalAddr().(*net.UDPAddr).Port }
func (m *mediaSession) ID() room.ParticipantID { return m.id }
func (m *mediaSession) dtmf() <-chan string    { return m.dtmfC }
func (m *mediaSession) done() <-chan struct{}  { return m.doneC }

func (m *mediaSession) setRemote(ip string, port int) {
	m.mu.Lock()
	m.remote = &net.UDPAddr{IP: net.ParseIP(ip), Port: port}
	m.mu.Unlock()
}

func (m *mediaSession) readLoop() {
	dec := &dtmfDecoder{}
	for {
		buf := make([]byte, 1500)
		n, err := m.conn.Read(buf)
		if err != nil {
			return // socket closed → Close() already fired doneC
		}
		pkt := &rtp.Packet{}
		if err := pkt.Unmarshal(buf[:n]); err != nil {
			continue
		}
		switch pkt.PayloadType {
		case m.dtmfPT:
			if d, ok := dec.push(pkt.Timestamp, pkt.Payload); ok {
				select {
				case m.dtmfC <- d:
				default:
				}
			}
		case m.opusPT:
			select {
			case m.in <- pkt:
			default: // queue full → drop this packet (newest); fine for RTP
			}
		}
	}
}

// ReadRTP yields the next inbound Opus packet. It blocks until one is available
// or the session closes.
func (m *mediaSession) ReadRTP() (*rtp.Packet, error) {
	select {
	case pkt := <-m.in:
		return pkt, nil
	case <-m.doneC:
		return nil, errors.New("fsplug: media session closed")
	}
}

// write re-stamps a room packet onto this session's clean outbound stream and
// sends it to the remote. No-op until setRemote is called.
func (m *mediaSession) write(pkt *rtp.Packet) error {
	m.mu.Lock()
	remote := m.remote
	if remote == nil || m.closed {
		m.mu.Unlock()
		return nil
	}
	out := &rtp.Packet{
		Header:  rtp.Header{Version: 2, PayloadType: m.opusPT, SequenceNumber: m.outSeq, Timestamp: m.outTS, SSRC: m.outSSRC},
		Payload: pkt.Payload,
	}
	m.mu.Unlock()

	raw, err := out.Marshal()
	if err != nil {
		return err
	}

	m.mu.Lock()
	m.outSeq++
	m.outTS += 960 // 20 ms @ 48 kHz
	m.mu.Unlock()

	_, err = m.conn.WriteToUDP(raw, remote)
	return err
}

func (m *mediaSession) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	m.mu.Unlock()
	close(m.doneC)
	return m.conn.Close()
}
