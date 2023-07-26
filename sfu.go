package sfu

import (
	"context"
	"errors"
	"io"
	"log"
	"strconv"
	"sync"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/interceptor/pkg/intervalpli"
	"github.com/pion/webrtc/v3"
)

type SFU struct {
	clients                   map[string]*Client
	context                   context.Context
	cancel                    context.CancelFunc
	callbacksOnTrackPublished []func(map[string]*webrtc.TrackLocalStaticRTP)
	callbacksOnClientRemoved  []func(*Client)
	callbacksOnClientAdded    []func(*Client)
	Counter                   int
	publicDataChannels        map[string]map[string]*webrtc.DataChannel
	privateDataChannels       map[string]map[string]*webrtc.DataChannel
	idleTimer                 *time.Timer
	idleChan                  chan bool
	mutex                     sync.RWMutex
	mux                       *UDPMux
	turnServer                TurnServer
	onStop                    func()
}

type TurnServer struct {
	Host     string
	Port     int
	Username string
	Password string
}

type PublishedTrack struct {
	ClientID string
	Track    webrtc.TrackLocal
}

func DefaultTurnServer() TurnServer {
	return TurnServer{
		Port:     3478,
		Host:     "turn.inlive.app",
		Username: "inlive",
		Password: "inlivesdkturn",
	}
}

// @Param muxPort: port for udp mux
func New(ctx context.Context, turnServer TurnServer, mux *UDPMux) *SFU {
	localCtx, cancel := context.WithCancel(ctx)

	sfu := &SFU{
		clients:             make(map[string]*Client),
		Counter:             0,
		context:             localCtx,
		cancel:              cancel,
		publicDataChannels:  make(map[string]map[string]*webrtc.DataChannel),
		privateDataChannels: make(map[string]map[string]*webrtc.DataChannel),
		mutex:               sync.RWMutex{},
		mux:                 mux,
		turnServer:          turnServer,
	}

	go func() {
	Out:
		for {
			select {
			case isIdle := <-sfu.idleChan:
				if isIdle {
					break Out
				}
			case <-sfu.context.Done():
				break Out
			}
		}

		cancel()
		sfu.Stop()
	}()

	return sfu
}

func (s *SFU) addClient(client *Client, direction webrtc.RTPTransceiverDirection) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	if _, ok := s.clients[client.ID]; ok {
		panic("client already exists")
	}

	s.clients[client.ID] = client

	s.onClientAdded(client)
}

func (s *SFU) createClient(id string, peerConnectionConfig *webrtc.Configuration, opts ClientOptions) *Client {
	m := &webrtc.MediaEngine{}
	if err := m.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000, Channels: 0, SDPFmtpLine: "", RTCPFeedback: nil},
		PayloadType:        96,
	}, webrtc.RTPCodecTypeVideo); err != nil {
		panic(err)
	}

	if err := m.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 0, SDPFmtpLine: "", RTCPFeedback: nil},
		PayloadType:        111,
	}, webrtc.RTPCodecTypeAudio); err != nil {
		panic(err)
	}

	// Enable simulcast in SDP
	for _, extension := range []string{
		"urn:ietf:params:rtp-hdrext:sdes:mid",
		"urn:ietf:params:rtp-hdrext:sdes:rtp-stream-id",
		"urn:ietf:params:rtp-hdrext:sdes:repaired-rtp-stream-id",
	} {
		if err := m.RegisterHeaderExtension(webrtc.RTPHeaderExtensionCapability{URI: extension}, webrtc.RTPCodecTypeVideo); err != nil {
			panic(err)
		}
	}

	// // Create a InterceptorRegistry. This is the user configurable RTP/RTCP Pipeline.
	// // This provides NACKs, RTCP Reports and other features. If you use `webrtc.NewPeerConnection`
	// // this is enabled by default. If you are manually managing You MUST create a InterceptorRegistry
	// // for each PeerConnection.
	i := &interceptor.Registry{}

	// Use the default set of Interceptors
	if err := webrtc.RegisterDefaultInterceptors(m, i); err != nil {
		panic(err)
	}

	// Register a intervalpli factory
	// This interceptor sends a PLI every 3 seconds. A PLI causes a video keyframe to be generated by the sender.
	// This makes our video seekable and more error resilent, but at a cost of lower picture quality and higher bitrates
	// A real world application should process incoming RTCP packets from viewers and forward them to senders
	intervalPliFactory, err := intervalpli.NewReceiverInterceptor()
	if err != nil {
		panic(err)
	}

	i.Add(intervalPliFactory)

	settingEngine := webrtc.SettingEngine{}

	if s.mux != nil {
		settingEngine.SetICEUDPMux(s.mux.mux)
	}

	// Create a new RTCPeerConnection
	peerConnection, err := webrtc.NewAPI(webrtc.WithMediaEngine(m), webrtc.WithSettingEngine(settingEngine), webrtc.WithInterceptorRegistry(i)).NewPeerConnection(*peerConnectionConfig)
	if err != nil {
		panic(err)
	}

	s.setupDataChannelBroadcaster(peerConnection, id)

	// add other clients tracks before generate the answer
	// s.addOtherClientTracksBeforeSendAnswer(peerConnection)

	localCtx, cancel := context.WithCancel(s.context)

	client := &Client{
		ID:                     id,
		Context:                localCtx,
		Cancel:                 cancel,
		mutex:                  sync.RWMutex{},
		PeerConnection:         peerConnection,
		State:                  ClientStateNew,
		tracks:                 make(map[string]*webrtc.TrackLocalStaticRTP),
		options:                opts,
		pendingReceivedTracks:  make(map[string]*webrtc.TrackLocalStaticRTP),
		pendingPublishedTracks: make(map[string]*webrtc.TrackLocalStaticRTP),
		publishedTracks:        make(map[string]*webrtc.TrackLocalStaticRTP),
	}

	peerConnection.OnICEConnectionStateChange(func(connectionState webrtc.ICEConnectionState) {
		log.Println("client: ice connection state changed ", connectionState)
	})

	// TODOL: replace this with callback
	peerConnection.OnConnectionStateChange(func(connectionState webrtc.PeerConnectionState) {
		log.Println("client: connection state changed ", connectionState)
		if client.State != ClientStateEnded && client.onConnectionStateChanged != nil {
			client.onConnectionStateChanged(connectionState)
		}

		for _, callback := range client.onConnectionStateChangedCallbacks {
			callback(connectionState)
		}
	})

	// Set a handler for when a new remote track starts, this just distributes all our packets
	// to connected peers
	peerConnection.OnTrack(func(remoteTrack *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		trackCtx, trackCancel := context.WithCancel(client.Context)
		log.Println("client: on track ", remoteTrack.ID(), remoteTrack.StreamID(), remoteTrack.Kind())
		client.State = ClientStateActive
		// Create a local track, all our SFU clients will be fed via this track
		localTrack, newTrackErr := webrtc.NewTrackLocalStaticRTP(remoteTrack.Codec().RTPCodecCapability, remoteTrack.ID(), remoteTrack.StreamID())
		if newTrackErr != nil {
			panic(newTrackErr)
		}

		client.mutex.Lock()
		client.tracks[localTrack.ID()] = localTrack
		client.mutex.Unlock()

		rtpBuf := make([]byte, 1400)

		go func() {
			defer trackCancel()

			for {
				select {
				case <-trackCtx.Done():

					return
				default:
					i, _, readErr := remoteTrack.Read(rtpBuf)
					if readErr == io.EOF {
						client.removeTrack(localTrack.ID())
						s.removeTrack(localTrack.StreamID(), localTrack.ID())
						return
					}

					if readErr != nil {
						log.Println("client: remote track read error ", readErr)
					}

					// ErrClosedPipe means we don't have any subscribers, this is ok if no peers have connected yet
					if _, err = localTrack.Write(rtpBuf[:i]); err != nil && !errors.Is(err, io.ErrClosedPipe) {
						log.Println("client: local track write error ", err)
					}
				}
			}
		}()

		client.onTrack(trackCtx, localTrack)
	})

	peerConnection.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		// only sending candidate when the local description is set, means expecting the remote peer already has the remote description
		if candidate != nil {
			if client.canAddCandidate {
				go client.onIceCandidateCallback(candidate)

				return
			}

			client.pendingLocalCandidates = append(client.pendingLocalCandidates, candidate)
		}
	})

	// Get the LocalDescription and take it to base64 so we can paste in browser
	return client
}

func (s *SFU) NewClient(id string, opts ClientOptions) *Client {
	s.Counter++

	// iceServers := []webrtc.ICEServer{{URLs: []string{
	// 	"stun:stun.l.google.com:19302",
	// }}}

	iceServers := []webrtc.ICEServer{}

	if s.turnServer.Host != "" {
		iceServers = append(iceServers,
			webrtc.ICEServer{
				URLs:           []string{"turn:" + s.turnServer.Host + ":" + strconv.Itoa(s.turnServer.Port)},
				Username:       s.turnServer.Username,
				Credential:     s.turnServer.Password,
				CredentialType: webrtc.ICECredentialTypePassword,
			},
			webrtc.ICEServer{
				URLs: []string{"stun:" + s.turnServer.Host + ":" + strconv.Itoa(s.turnServer.Port)},
			})
	}

	peerConnectionConfig := webrtc.Configuration{
		ICEServers: iceServers,
	}

	client := s.createClient(id, &peerConnectionConfig, opts)

	client.onConnectionStateChanged = func(connectionState webrtc.PeerConnectionState) {
		switch connectionState {
		case webrtc.PeerConnectionStateConnected:
			needRenegotiation := false

			if len(client.pendingReceivedTracks) > 0 {
				client.processPendingTracks()

				needRenegotiation = true
			}

			if opts.Direction == webrtc.RTPTransceiverDirectionRecvonly || opts.Direction == webrtc.RTPTransceiverDirectionSendrecv {
				// get the tracks from other clients if the direction is receiving track
				isNeedRenegotiation := s.SyncTrack(client)
				if !needRenegotiation && isNeedRenegotiation {
					needRenegotiation = true
				}
			}

			if needRenegotiation {
				log.Println("call renegotiate after sync ", client.ID)

				go client.renegotiate()
			}

		case webrtc.PeerConnectionStateClosed:
			for _, track := range client.tracks {
				client.removeTrack(track.ID())
				s.removeTrack(track.StreamID(), track.ID())
			}

			client.afterClosed()
		case webrtc.PeerConnectionStateFailed:
			client.startIdleTimeout()
		case webrtc.PeerConnectionStateConnecting:
			client.cancelIdleTimeout()
		}
	}

	client.onTrack = func(ctx context.Context, localTrack *webrtc.TrackLocalStaticRTP) {
		client.tracks[localTrack.ID()] = localTrack
		client.pendingPublishedTracks[localTrack.StreamID()+"-"+localTrack.ID()] = localTrack

		// don't publish track when not all the tracks are received
		if client.GetType() == ClientTypePeer && client.initialTracksCount > len(client.pendingPublishedTracks) {
			return
		}

		s.publishTracks(client.ID, client.pendingPublishedTracks)
	}

	// request keyframe from new client for existing clients
	client.requestKeyFrame()

	s.addClient(client, client.GetDirection())

	return client
}

func (s *SFU) publishTracks(clientID string, tracks map[string]*webrtc.TrackLocalStaticRTP) {
	pendingTracks := []PublishedTrack{}

	s.mutex.Lock()

	for _, track := range tracks {
		// only publish track if it's not published yet

		newTrack := PublishedTrack{
			ClientID: clientID,
			Track:    track,
		}
		pendingTracks = append(pendingTracks, newTrack)

	}

	s.mutex.Unlock()

	s.broadcastTracks(pendingTracks)

	// request keyframe from existing client
	for _, client := range s.clients {
		client.requestKeyFrame()
	}

	s.onTrackPublished(tracks)
}

func (s *SFU) broadcastTracks(tracks []PublishedTrack) {
	for _, client := range s.clients {
		renegotiate := false

		for _, track := range tracks {
			if client.ID != track.ClientID {
				renegotiate = client.addTrack(track.Track.(*webrtc.TrackLocalStaticRTP))
			}
		}

		if renegotiate {
			go client.renegotiate()
		}
	}
}

func (s *SFU) removeTrack(streamID, trackID string) bool {
	trackRemoved := false
	for _, client := range s.clients {
		trackRemoved = client.removePublishedTrack(streamID, trackID)
	}

	return trackRemoved
}

// Syncs track from connected client to other clients
// returns true if need renegotiation
func (s *SFU) SyncTrack(client *Client) bool {
	currentTracks := client.GetCurrentTracks()

	needRenegotiation := false

	for _, clientPeer := range s.clients {
		for _, track := range clientPeer.tracks {
			if client.ID != clientPeer.ID {
				if _, ok := currentTracks[track.StreamID()+"-"+track.ID()]; !ok {
					client.addTrack(track)

					// request the keyframe from track publisher after added
					s.requestKeyFrameFromClient(clientPeer.ID)

					needRenegotiation = true
				}
			}
		}
	}

	return needRenegotiation
}

func (s *SFU) GetTracks() map[string]*webrtc.TrackLocalStaticRTP {
	tracks := make(map[string]*webrtc.TrackLocalStaticRTP)
	for _, client := range s.clients {
		for _, track := range client.tracks {
			tracks[track.StreamID()+"-"+track.ID()] = track
		}
	}

	return tracks
}

func (s *SFU) Stop() {
	for _, client := range s.clients {
		client.PeerConnection.Close()
	}

	if s.onStop != nil {
		s.onStop()
	}

	s.cancel()

}

func (s *SFU) OnStopped(callback func()) {
	s.onStop = callback
}

func (s *SFU) renegotiateAllClients() {
	for _, client := range s.clients {
		go client.renegotiate()
	}
}

func (s *SFU) requestKeyFrameFromClient(clientID string) {
	if client, ok := s.clients[clientID]; ok {
		client.requestKeyFrame()
	}
}

func (s *SFU) startIdleTimeout() {
	go func() {
		localCtx, cancel := context.WithCancel(s.context)
		defer cancel()

		for {
			select {
			case <-localCtx.Done():
				return
			case <-time.After(50 * time.Minute):
				s.idleChan <- true
				s.Stop()
			}
		}
	}()
}

func (s *SFU) cancelIdleTimeout() {
	s.idleChan <- false
}

func (s *SFU) OnTrackPublished(callback func(map[string]*webrtc.TrackLocalStaticRTP)) {
	s.callbacksOnTrackPublished = append(s.callbacksOnTrackPublished, callback)
}

func (s *SFU) OnClientAdded(callback func(*Client)) {
	s.callbacksOnClientAdded = append(s.callbacksOnClientAdded, callback)
}

func (s *SFU) OnClientRemoved(callback func(*Client)) {
	s.callbacksOnClientRemoved = append(s.callbacksOnClientRemoved, callback)
}

func (s *SFU) onClientAdded(client *Client) {
	for _, callback := range s.callbacksOnClientAdded {
		callback(client)
	}
}

func (s *SFU) onClientRemoved(client *Client) {
	for _, callback := range s.callbacksOnClientRemoved {
		callback(client)
	}
}

func (s *SFU) onTrackPublished(tracks map[string]*webrtc.TrackLocalStaticRTP) {
	for _, callback := range s.callbacksOnTrackPublished {
		callback(tracks)
	}
}

func (s *SFU) GetClients() map[string]*Client {
	return s.clients
}

func (s *SFU) GetClient(id string) (*Client, error) {
	if client, ok := s.clients[id]; ok {
		return client, nil
	}

	return nil, ErrClientNotFound
}
