package sfu

import (
	"context"
	"math/rand"
	"net/url"
	"sync"
	"time"

	"github.com/pion/sdp/v3"
	"github.com/pion/webrtc/v3"

	"github.com/pion/ion-sfu/pkg/log"
)

// ICEServerConfig defines parameters for ice servers
type ICEServerConfig struct {
	URLs       []string `mapstructure:"urls"`
	Username   string   `mapstructure:"username"`
	Credential string   `mapstructure:"credential"`
}

// WebRTCConfig defines parameters for ice
type WebRTCConfig struct {
	ICEPortRange []uint16          `mapstructure:"portrange"`
	ICEServers   []ICEServerConfig `mapstructure:"iceserver"`
	NAT1To1IPs   []string          `mapstructure:"nat1to1"`
}

// Config for base SFU
type Config struct {
	WebRTC WebRTCConfig `mapstructure:"webrtc"`
	Log    log.Config   `mapstructure:"log"`
	Router RouterConfig `mapstructure:"router"`
}

// SFU represents an sfu instance
type SFU struct {
	ctx      context.Context
	cancel   context.CancelFunc
	webrtc   WebRTCTransportConfig
	router   RouterConfig
	mu       sync.RWMutex
	sessions map[string]*Session
}

// NewSFU creates a new sfu instance
func NewSFU(c Config) *SFU {
	// Init random seed
	rand.Seed(time.Now().UnixNano())

	ctx, cancel := context.WithCancel(context.Background())
	// Configure required extensions for simulcast
	sdes, _ := url.Parse(sdp.SDESRTPStreamIDURI)
	sdedMid, _ := url.Parse(sdp.SDESMidURI)
	exts := []sdp.ExtMap{
		{
			URI: sdes,
		},
		{
			URI: sdedMid,
		},
	}
	se := webrtc.SettingEngine{}
	se.AddSDPExtensions(webrtc.SDPSectionVideo, exts)

	w := WebRTCTransportConfig{
		configuration: webrtc.Configuration{
			SDPSemantics: webrtc.SDPSemanticsUnifiedPlan,
		},
		setting: se,
		router:  c.Router,
	}
	log.Init(c.Log.Level, c.Log.Fix)

	var icePortStart, icePortEnd uint16

	if len(c.WebRTC.ICEPortRange) == 2 {
		icePortStart = c.WebRTC.ICEPortRange[0]
		icePortEnd = c.WebRTC.ICEPortRange[1]
	}

	if icePortStart != 0 || icePortEnd != 0 {
		if err := w.setting.SetEphemeralUDPPortRange(icePortStart, icePortEnd); err != nil {
			panic(err)
		}
	}

	var iceServers []webrtc.ICEServer
	for _, iceServer := range c.WebRTC.ICEServers {
		s := webrtc.ICEServer{
			URLs:       iceServer.URLs,
			Username:   iceServer.Username,
			Credential: iceServer.Credential,
		}
		iceServers = append(iceServers, s)
	}

	w.configuration.ICEServers = iceServers

	if len(c.WebRTC.NAT1To1IPs) > 0 {
		w.setting.SetNAT1To1IPs(c.WebRTC.NAT1To1IPs, webrtc.ICECandidateTypeHost)
	}

	// Configure bandwidth estimation support
	if c.Router.Video.TCCCycle > 0 {
		rtcpfb = append(rtcpfb, webrtc.RTCPFeedback{Type: webrtc.TypeRTCPFBTransportCC})
		transportCCURL, _ := url.Parse(sdp.TransportCCURI)
		exts := []sdp.ExtMap{
			{
				Value: 3,
				URI:   transportCCURL,
			},
		}
		w.setting.AddSDPExtensions(webrtc.SDPSectionVideo, exts)
	}

	if c.Router.Video.REMBCycle > 0 {
		rtcpfb = append(rtcpfb, webrtc.RTCPFeedback{Type: webrtc.TypeRTCPFBGoogREMB})
	}

	s := &SFU{
		ctx:      ctx,
		cancel:   cancel,
		webrtc:   w,
		sessions: make(map[string]*Session),
	}

	if c.Log.Stats {
		go s.stats()
	}

	return s
}

// NewSession creates a new session instance
func (s *SFU) newSession(id string) *Session {
	session := NewSession(id)
	session.OnClose(func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.sessions, id)
	})

	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[id] = session
	return session
}

// GetSession by id
func (s *SFU) getSession(id string) *Session {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sessions[id]
}

// NewWebRTCTransport creates a new WebRTCTransport that is a member of a session
func (s *SFU) NewWebRTCTransport(sid string, me MediaEngine) (*WebRTCTransport, error) {
	session := s.getSession(sid)

	if session == nil {
		session = s.newSession(sid)
	}

	t, err := NewWebRTCTransport(s.ctx, session, me, s.webrtc)
	if err != nil {
		return nil, err
	}

	return t, nil
}

// Stop the sfu
func (s *SFU) Stop() {
	s.cancel()
}

func (s *SFU) stats() {
	t := time.NewTicker(statCycle)
	for {
		select {
		case <-t.C:
			info := "\n----------------stats-----------------\n"

			s.mu.RLock()
			sessions := s.sessions
			s.mu.RUnlock()

			if len(sessions) == 0 {
				continue
			}

			for _, session := range sessions {
				info += session.stats()
			}
			log.Infof(info)
		case <-s.ctx.Done():
			return
		}
	}
}
