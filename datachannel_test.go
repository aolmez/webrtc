package webrtc

import (
	"crypto/rand"
	"encoding/binary"
	"io"
	"math/big"
	"testing"
	"time"

	sugar "github.com/pions/webrtc/pkg/datachannel"

	"github.com/pions/transport/test"
	"github.com/stretchr/testify/assert"
)

func closePair(t *testing.T, pc1, pc2 io.Closer, done chan bool) {
	var err error
	select {
	case <-time.After(10 * time.Second):
		t.Fatalf("Datachannel Send Test Timeout")
	case <-done:
		err = pc1.Close()
		if err != nil {
			t.Fatalf("Failed to close offer PC")
		}
		err = pc2.Close()
		if err != nil {
			t.Fatalf("Failed to close answer PC")
		}
	}
}

func TestGenerateDataChannelID(t *testing.T) {
	api := NewAPI()

	testCases := []struct {
		client bool
		c      *PeerConnection
		result uint16
	}{
		{true, &PeerConnection{sctpTransport: api.NewSCTPTransport(nil), dataChannels: map[uint16]*DataChannel{}, api: api}, 0},
		{true, &PeerConnection{sctpTransport: api.NewSCTPTransport(nil), dataChannels: map[uint16]*DataChannel{1: nil}, api: api}, 0},
		{true, &PeerConnection{sctpTransport: api.NewSCTPTransport(nil), dataChannels: map[uint16]*DataChannel{0: nil}, api: api}, 2},
		{true, &PeerConnection{sctpTransport: api.NewSCTPTransport(nil), dataChannels: map[uint16]*DataChannel{0: nil, 2: nil}, api: api}, 4},
		{true, &PeerConnection{sctpTransport: api.NewSCTPTransport(nil), dataChannels: map[uint16]*DataChannel{0: nil, 4: nil}, api: api}, 2},
		{false, &PeerConnection{sctpTransport: api.NewSCTPTransport(nil), dataChannels: map[uint16]*DataChannel{}, api: api}, 1},
		{false, &PeerConnection{sctpTransport: api.NewSCTPTransport(nil), dataChannels: map[uint16]*DataChannel{0: nil}, api: api}, 1},
		{false, &PeerConnection{sctpTransport: api.NewSCTPTransport(nil), dataChannels: map[uint16]*DataChannel{1: nil}, api: api}, 3},
		{false, &PeerConnection{sctpTransport: api.NewSCTPTransport(nil), dataChannels: map[uint16]*DataChannel{1: nil, 3: nil}, api: api}, 5},
		{false, &PeerConnection{sctpTransport: api.NewSCTPTransport(nil), dataChannels: map[uint16]*DataChannel{1: nil, 5: nil}, api: api}, 3},
	}

	for _, testCase := range testCases {
		id, err := testCase.c.generateDataChannelID(testCase.client)
		if err != nil {
			t.Errorf("failed to generate id: %v", err)
			return
		}
		if id != testCase.result {
			t.Errorf("Wrong id: %d expected %d", id, testCase.result)
		}
	}
}

func TestDataChannel_Send(t *testing.T) {
	report := test.CheckRoutines(t)
	defer report()

	api := NewAPI()
	offerPC, answerPC, err := api.newPair()

	if err != nil {
		t.Fatalf("Failed to create a PC pair for testing")
	}

	done := make(chan bool)

	dc, err := offerPC.CreateDataChannel("data", nil)

	if err != nil {
		t.Fatalf("Failed to create a PC pair for testing")
	}

	assert.True(t, dc.Ordered, "Ordered should be set to true")

	dc.OnOpen(func() {
		e := dc.Send(sugar.PayloadString{Data: []byte("Ping")})
		if e != nil {
			t.Fatalf("Failed to send string on data channel")
		}
	})
	dc.OnMessage(func(payload sugar.Payload) {
		done <- true
	})

	answerPC.OnDataChannel(func(d *DataChannel) {
		assert.True(t, d.Ordered, "Ordered should be set to true")

		d.OnMessage(func(payload sugar.Payload) {
			e := d.Send(sugar.PayloadBinary{Data: []byte("Pong")})
			if e != nil {
				t.Fatalf("Failed to send string on data channel")
			}
		})
	})

	err = signalPair(offerPC, answerPC)

	if err != nil {
		t.Fatalf("Failed to signal our PC pair for testing")
	}

	closePair(t, offerPC, answerPC, done)
}

func TestDataChannel_EventHandlers(t *testing.T) {
	to := test.TimeOut(time.Second * 20)
	defer to.Stop()

	report := test.CheckRoutines(t)
	defer report()

	api := NewAPI()
	dc := &DataChannel{api: api}

	onOpenCalled := make(chan bool)
	onMessageCalled := make(chan bool)

	// Verify that the noop case works
	assert.NotPanics(t, func() { dc.onOpen() })
	assert.NotPanics(t, func() { dc.onMessage(nil) })

	dc.OnOpen(func() {
		onOpenCalled <- true
	})

	dc.OnMessage(func(p sugar.Payload) {
		go func() {
			onMessageCalled <- true
		}()
	})

	// Verify that the handlers deal with nil inputs
	assert.NotPanics(t, func() { dc.onMessage(nil) })

	// Verify that the set handlers are called
	assert.NotPanics(t, func() { dc.onOpen() })
	assert.NotPanics(t, func() { dc.onMessage(&sugar.PayloadString{Data: []byte("o hai")}) })

	allTrue := func(vals []bool) bool {
		for _, val := range vals {
			if !val {
				return false
			}
		}
		return true
	}

	assert.True(t, allTrue([]bool{
		<-onOpenCalled,
		<-onMessageCalled,
	}))
}

func TestDataChannel_MessagesAreOrdered(t *testing.T) {
	report := test.CheckRoutines(t)
	defer report()

	api := NewAPI()
	dc := &DataChannel{api: api}

	max := 512
	out := make(chan int)
	inner := func(payload sugar.Payload) {
		// randomly sleep
		// NB: The big.Int/crypto.Rand is overkill but makes the linter happy
		randInt, err := rand.Int(rand.Reader, big.NewInt(int64(max)))
		if err != nil {
			t.Fatalf("Failed to get random sleep duration: %s", err)
		}
		time.Sleep(time.Duration(randInt.Int64()) * time.Microsecond)
		p, ok := payload.(*sugar.PayloadBinary)
		if ok {
			s, _ := binary.Varint(p.Data)
			out <- int(s)
		}
	}
	dc.OnMessage(func(p sugar.Payload) {
		inner(p)
	})

	go func() {
		for i := 1; i <= max; i++ {
			buf := make([]byte, 8)
			binary.PutVarint(buf, int64(i))
			dc.onMessage(&sugar.PayloadBinary{Data: buf})
			// Change the registered handler a couple of times to make sure
			// that everything continues to work, we don't lose messages, etc.
			if i%2 == 0 {
				hdlr := func(p sugar.Payload) {
					inner(p)
				}
				dc.OnMessage(hdlr)
			}
		}
	}()

	values := make([]int, 0, max)
	for v := range out {
		values = append(values, v)
		if len(values) == max {
			close(out)
		}
	}

	expected := make([]int, max)
	for i := 1; i <= max; i++ {
		expected[i-1] = i
	}
	assert.EqualValues(t, expected, values)
}

func setUpReliabilityParamTest(t *testing.T, options *DataChannelInit) (*PeerConnection, *PeerConnection, *DataChannel, chan bool) {
	api := NewAPI()
	offerPC, answerPC, err := api.newPair()
	if err != nil {
		t.Fatalf("Failed to create a PC pair for testing")
	}
	done := make(chan bool)

	dc, err := offerPC.CreateDataChannel("data", options)
	if err != nil {
		t.Fatalf("Failed to create a PC pair for testing")
	}

	return offerPC, answerPC, dc, done
}

func closeReliabilityParamTest(t *testing.T, pc1, pc2 *PeerConnection, done chan bool) {
	err := signalPair(pc1, pc2)
	if err != nil {
		t.Fatalf("Failed to signal our PC pair for testing")
	}

	closePair(t, pc1, pc2, done)
}

func TestDataChannelParamters(t *testing.T) {
	report := test.CheckRoutines(t)
	defer report()

	t.Run("MaxPacketLifeTime exchange", func(t *testing.T) {
		var ordered = true
		var maxPacketLifeTime uint16 = 3
		options := &DataChannelInit{
			Ordered:           &ordered,
			MaxPacketLifeTime: &maxPacketLifeTime,
		}

		offerPC, answerPC, dc, done := setUpReliabilityParamTest(t, options)

		// Check if parameters are correctly set
		assert.True(t, dc.Ordered, "Ordered should be set to true")
		if assert.NotNil(t, dc.MaxPacketLifeTime, "should not be nil") {
			assert.Equal(t, maxPacketLifeTime, *dc.MaxPacketLifeTime, "should match")
		}

		answerPC.OnDataChannel(func(d *DataChannel) {
			// Check if parameters are correctly set
			assert.True(t, d.Ordered, "Ordered should be set to true")
			if assert.NotNil(t, d.MaxPacketLifeTime, "should not be nil") {
				assert.Equal(t, maxPacketLifeTime, *d.MaxPacketLifeTime, "should match")
			}
			done <- true
		})

		closeReliabilityParamTest(t, offerPC, answerPC, done)
	})

	t.Run("MaxRetransmits exchange", func(t *testing.T) {
		var ordered = false
		var maxRetransmits uint16 = 3000
		options := &DataChannelInit{
			Ordered:        &ordered,
			MaxRetransmits: &maxRetransmits,
		}

		offerPC, answerPC, dc, done := setUpReliabilityParamTest(t, options)

		// Check if parameters are correctly set
		assert.False(t, dc.Ordered, "Ordered should be set to false")
		if assert.NotNil(t, dc.MaxRetransmits, "should not be nil") {
			assert.Equal(t, maxRetransmits, *dc.MaxRetransmits, "should match")
		}

		answerPC.OnDataChannel(func(d *DataChannel) {
			// Check if parameters are correctly set
			assert.False(t, d.Ordered, "Ordered should be set to false")
			if assert.NotNil(t, d.MaxRetransmits, "should not be nil") {
				assert.Equal(t, maxRetransmits, *d.MaxRetransmits, "should match")
			}
			done <- true
		})

		closeReliabilityParamTest(t, offerPC, answerPC, done)
	})
}
