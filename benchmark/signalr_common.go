package benchmark

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/gorilla/websocket"
	"github.com/teris-io/shortid"
	"github.com/vmihailenco/msgpack"
)

type SignalrServiceHandshake struct {
	ServiceUrl string `json:"url"`
	JwtBearer  string `json:"accessToken"`
}

type SignalrCoreCommon struct {
	WithCounter
	WithSessions
}

func (s *SignalrCoreCommon) SignalrCoreBaseConnect(protocol string) (session *Session, err error) {
	defer func() {
		if err != nil {
			s.counter.Stat("connection:inprogress", -1)
			s.counter.Stat("connection:error", 1)
		}
	}()

	id, err := shortid.Generate()
	if err != nil {
		log.Println("ERROR: failed to generate uid due to", err)
		return
	}

	s.counter.Stat("connection:inprogress", 1)
	wsURL := "ws://" + s.host
	c, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		s.LogError("connection:error", id, "Failed to connect to websocket", err)
		return nil, err
	}

	session = NewSession(id, s.received, s.counter, c)
	if session != nil {
		s.counter.Stat("connection:inprogress", -1)
		s.counter.Stat("connection:established", 1)

		session.Start()
		session.NegotiateProtocol(protocol)
		return
	}

	err = fmt.Errorf("Nil session")
	return
}

func (s *SignalrCoreCommon) SignalrCoreJsonConnect() (*Session, error) {
	return s.SignalrCoreBaseConnect("json")
}

func (s *SignalrCoreCommon) SignalrCoreMsgPackConnect() (session *Session, err error) {
	return s.SignalrCoreBaseConnect("messagepack")
}

func (s *SignalrCoreCommon) SignalrServiceBaseConnect(protocol string) (session *Session, err error) {
	defer func() {
		if err != nil {
			s.counter.Stat("connection:inprogress", -1)
			s.counter.Stat("connection:error", 1)
		}
	}()

	s.counter.Stat("connection:inprogress", 1)

	id, err := shortid.Generate()
	if err != nil {
		log.Println("ERROR: failed to generate uid due to", err)
		return
	}

	negotiateResponse, err := http.Get("http://" + s.host + "/negotiate")
	if err != nil {
		s.LogError("connection:error", id, "Failed to negotiate with the server", err)
		return
	}
	defer negotiateResponse.Body.Close()

	decoder := json.NewDecoder(negotiateResponse.Body)
	var handshake SignalrServiceHandshake
	err = decoder.Decode(&handshake)
	if err != nil {
		s.LogError("connection:error", id, "Failed to decode service URL and jwtBearer", err)
		return
	}

	var httpPrefix = regexp.MustCompile("^https?://")
	var ws string
	if s.useWss {
		ws = "wss://"
	} else {
		ws = "ws://"
	}
	baseURL := httpPrefix.ReplaceAllString(handshake.ServiceUrl, ws)
	wsURL := baseURL + "&access_token=" + handshake.JwtBearer

	c, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		s.LogError("connection:error", id, "Failed to connect to websocket", err)
		return
	}
	session = NewSession(id, s.received, s.counter, c)
	if session != nil {
		s.counter.Stat("connection:inprogress", -1)
		s.counter.Stat("connection:established", 1)

		session.Start()
		session.NegotiateProtocol(protocol)
		return
	}

	err = fmt.Errorf("Nil session")
	return
}

func (s *SignalrCoreCommon) SignalrServiceJsonConnect() (session *Session, err error) {
	return s.SignalrServiceBaseConnect("json")
}

func (s *SignalrCoreCommon) SignalrServiceMsgPackConnect() (session *Session, err error) {
	return s.SignalrServiceBaseConnect("messagepack")
}

var numBitsToShift = []uint{0, 7, 14, 21, 28}

func (s *SignalrCoreCommon) ParseBinaryMessage(bytes []byte) ([]byte, error) {
	moreBytes := true
	msgLen := 0
	numBytes := 0
	//fmt.Printf("%x %x\n", bytes[0], bytes[1])
	for moreBytes && numBytes < len(bytes) && numBytes < 5 {
		byteRead := bytes[numBytes]
		msgLen = msgLen | int(uint(byteRead&0x7F)<<numBitsToShift[numBytes])
		numBytes++
		moreBytes = (byteRead & 0x80) != 0
	}

	if msgLen+numBytes > len(bytes) {
		return nil, fmt.Errorf("Not enough data in message, message length = %d, length section bytes = %d, data length = %d", msgLen, numBytes, len(bytes))
	}

	return bytes[numBytes : numBytes+msgLen], nil
}

func (s *SignalrCoreCommon) ProcessJsonLatency(target string) {
	for msgReceived := range s.received {
		// Multiple json responses may be merged to be a list.
		// Split them and remove '0x1e' terminator.
		dataArray := bytes.Split(msgReceived.Content, []byte{0x1e})
		for _, msg := range dataArray {
			if len(msg) == 0 || len(msg) == 2 {
				// ignore the empty msg and handshake response
				continue
			}
			var common SignalRCommon
			err := json.Unmarshal(msg, &common)
			if err != nil {
				fmt.Printf("%s\n", msg)
				s.LogError("message:decode_error", msgReceived.ClientID, "Failed to decode incoming message common header", err)
				continue
			}

			// ignore ping
			if common.Type != 1 {
				continue
			}

			var content SignalRCoreInvocation
			err = json.Unmarshal(msg, &content)
			if err != nil {
				s.LogError("message:decode_error", msgReceived.ClientID, "Failed to decode incoming SignalR invocation message", err)
				continue
			}

			if content.Type == 1 && content.Target == target {
				sendStart, err := strconv.ParseInt(content.Arguments[1], 10, 64)
				if err != nil {
					s.LogError("message:decode_error", msgReceived.ClientID, "Failed to decode start timestamp", err)
					continue
				}
				s.LogLatency((time.Now().UnixNano() - sendStart) / 1000000)
			}
		}
	}
}

func (s *SignalrCoreCommon) ProcessMsgPackLatency(target string) {
	for msgReceived := range s.received {
		msg, err := s.ParseBinaryMessage(msgReceived.Content)
		if err != nil {
			s.LogError("message:decode_error", msgReceived.ClientID, "Failed to parse incoming message", err)
			continue
		}
		var content MsgpackInvocation
		err = msgpack.Unmarshal(msg, &content)
		if err != nil {
			s.LogError("message:decode_error", msgReceived.ClientID, "Failed to decode incoming message", err)
			continue
		}

		if content.Target == target {
			sendStart, err := strconv.ParseInt(content.Params[1], 10, 64)
			if err != nil {
				s.LogError("message:decode_error", msgReceived.ClientID, "Failed to decode start timestamp", err)
				continue
			}
			s.LogLatency((time.Now().UnixNano() - sendStart) / 1000000)
		}
	}
}
