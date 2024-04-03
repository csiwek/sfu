package sfu

import (
	"time"

	"github.com/pion/webrtc/v3"
)

type StatTracks struct {
	Audio int `json:"audio"`
	Video int `json:"video"`
}

type TrackSentStats struct {
	ID             string              `json:"id"`
	StreamID       string              `json:"stream_id"`
	Kind           webrtc.RTPCodecType `json:"kind"`
	PacketsLost    int64               `json:"packets_lost"`
	PacketSent     uint64              `json:"packets_sent"`
	FractionLost   float64             `json:"fraction_lost"`
	BytesSent      uint64              `json:"bytes_sent"`
	CurrentBitrate uint32              `json:"current_bitrate"`
	Source         string              `json:"source"`
	Quality        QualityLevel        `json:"quality"`
}

type TrackReceivedStats struct {
	ID              string `json:"id"`
	StreamID        string `json:"stream_id"`
	RID             string `json:"rid"`
	Kind            string `json:"kind"`
	Codec           string `json:"codec"`
	CurrentBitrate  uint32 `json:"current_bitrate"`
	PacketsLost     int64  `json:"packets_lost"`
	PacketsReceived uint64 `json:"packets_received"`
	BytesReceived   int64  `json:"bytes_received"`
}

type ClientTrackStats struct {
	ID                       string               `json:"id"`
	Name                     string               `json:"name"`
	PublisherBandwidth       uint32               `json:"publisher_bandwidth"`
	ConsumerBandwidth        uint32               `json:"consumer_bandwidth"`
	CurrentConsumerBitrate   uint32               `json:"current_bitrate"`
	CurrentPublishLimitation string               `json:"current_publish_limitation"`
	Sents                    []TrackSentStats     `json:"sent_track_stats"`
	Receives                 []TrackReceivedStats `json:"received_track_stats"`
	// in milliseconds
	VoiceActivityDuration uint32 `json:"voice_activity_duration"`
}

type RoomStats struct {
	ActiveSessions     int                          `json:"active_sessions"`
	ClientsCount       int                          `json:"clients_count"`
	PacketSentLost     int64                        `json:"packet_sent_lost"`
	PacketReceivedLost int64                        `json:"packet_received_lost"`
	PacketReceived     uint64                       `json:"packet_received"`
	PacketSent         uint64                       `json:"packet_sent"`
	ByteSent           uint64                       `json:"bytes_sent"`
	BytesReceived      uint64                       `json:"bytes_received"`
	Tracks             StatTracks                   `json:"tracks"`
	Timestamp          time.Time                    `json:"timestamp"`
	ClientStats        map[string]*ClientTrackStats `json:"client_stats"`
}
