package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/rpc/jsonrpc"
	"os"
	"os/signal"
	"syscall"
	"time"

	webrtc "github.com/keroserene/go-webrtc"
	"github.com/nobonobo/rtcdc-p2p/datachan"
	"github.com/nobonobo/rtcdc-p2p/signaling"
	"github.com/nobonobo/rtcdc-p2p/signaling/client"
)

var iceServers = []string{"stun:stun.l.google.com:19302"}

//var iceServers = []string{}

// Server ...
type Server struct {
	*client.Client
	members map[string]*datachan.Connection
}

// New ...
func New(room, id string) *Server {
	s := new(Server)
	s.Client = client.New(room, id, s.dispatch)
	s.members = map[string]*datachan.Connection{}
	return s
}

// Send ...
func (s *Server) Send(to string, v interface{}) error {
	log.Printf("send: %T to %s\n", v, to)
	m := signaling.New(s.ID(), to, v)
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	s.Client.Send(b)
	return nil
}

func (s *Server) dispatch(b []byte) {
	var m *signaling.Message
	if err := json.Unmarshal(b, &m); err != nil {
		log.Println(err)
		return
	}
	if m.Sender == s.ID() {
		return
	}
	if m.To != "" && m.To != s.ID() {
		return
	}
	value, err := m.Get()
	if err != nil {
		log.Println(err)
		return
	}
	log.Printf("recv: %T from %s\n", value, m.Sender)
	switch v := value.(type) {
	case *signaling.Request:
		if conn := s.members[m.Sender]; conn != nil {
			conn.Close()
		}
		conn, err := datachan.New(iceServers)
		if err != nil {
			log.Println("datachan new failed:", err)
			delete(s.members, m.Sender)
			return
		}
		conn.OnDataChannel = func(channel *webrtc.DataChannel) {
			go jsonrpc.ServeConn(datachan.NewConn(channel))
		}
		offer, err := conn.Offer()
		if err != nil {
			log.Println("offer failed:", err)
			delete(s.members, m.Sender)
			return
		}
		if err := s.Send(m.Sender, &signaling.Offer{Description: offer.Serialize()}); err != nil {
			log.Println("send failed:", err)
			delete(s.members, m.Sender)
			return
		}
		log.Println("offer completed:", m.Sender)
		s.members[m.Sender] = conn

	case *signaling.Offer:
	case *signaling.Answer:
		conn := s.members[m.Sender]
		if conn == nil {
			log.Println("connection failed:", m.Sender)
			return
		}
		sdp := webrtc.DeserializeSessionDescription(v.Description)
		/*
			sdp := &webrtc.SessionDescription{
				Type: "answer",
				Sdp:  v.Description,
			}
		*/
		if sdp == nil {
			log.Println("desirialize sdp failed", v.Description)
			return
		}
		if err := conn.SetRemoteDescription(sdp); err != nil {
			log.Println("answer set failed:", err)
			delete(s.members, m.Sender)
			conn.Close()
			return
		}
		ices := conn.IceCandidates()
		log.Println("ices:", len(ices))
		for _, ice := range ices {
			msg := &signaling.Candidate{
				Candidate:     ice.Candidate,
				SdpMid:        ice.SdpMid,
				SdpMLineIndex: ice.SdpMLineIndex,
			}
			log.Printf("candidate: %q\n", ice.Candidate)
			if err := s.Send(m.Sender, msg); err != nil {
				log.Println(err)
				return
			}
			time.Sleep(100 * time.Microsecond)
		}
	case *signaling.Candidate:
		conn := s.members[m.Sender]
		if conn == nil {
			log.Println("connection failed:", m.Sender)
			return
		}
		ice := webrtc.DeserializeIceCandidate(string(m.Value))
		if err := conn.AddIceCandidate(*ice); err != nil {
			log.Println("add ice failed:", err)
		}
	}
}

func main() {
	var room, id string
	flag.StringVar(&room, "room", "sample", "name of room")
	flag.StringVar(&id, "id", "**master**", "name of id")
	flag.Parse()
	if id == "" {
		log.Fatalln("id must set unique")
	}
	webrtc.SetLoggingVerbosity(0)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT)
	s := New(room, id)
	go func() {
		defer s.Bye()
		for {
			if err := s.Create(); err != nil {
				log.Fatalln(err)
			}
			time.Sleep(10 * time.Second)
		}
	}()
	s.Start()
	defer s.Stop()
	<-sig
}
