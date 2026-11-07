package remote

import "sync"

// VoiceStream is a high-level voice streaming session wrapper.
//
// Obtained from AndroidTVRemote.StartVoice().
type VoiceStream struct {
	proto     *remoteProtocol
	sessionID int32

	mu     sync.Mutex
	closed bool
}

// SessionID returns the voice session id.
func (v *VoiceStream) SessionID() int32 { return v.sessionID }

// SendChunk sends a chunk of audio data.
//
//   - The audio data must be 16-bit PCM, mono, 8000 Hz.
//   - The chunk size should be at least 8 KB. Smaller chunks will be padded
//     with zeros.
//   - Chunk sizes larger than 20 KB will be split into multiple chunks.
//
// Returns false if the voice session is already closed. Returns a
// ConnectionClosed error if the connection is lost.
func (v *VoiceStream) SendChunk(chunk []byte) (bool, error) {
	v.mu.Lock()
	closed := v.closed
	v.mu.Unlock()
	if closed {
		v.proto.logger.Debug("VoiceStream already closed")
		return false, nil
	}
	if err := v.proto.sendVoiceChunk(chunk, v.sessionID); err != nil {
		return false, err
	}
	return true, nil
}

// End ends the voice stream.
func (v *VoiceStream) End() error {
	v.mu.Lock()
	if v.closed {
		v.mu.Unlock()
		return nil
	}
	v.closed = true
	v.mu.Unlock()
	return v.proto.endVoice(v.sessionID)
}
