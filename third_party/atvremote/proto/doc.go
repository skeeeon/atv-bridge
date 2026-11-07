// Package proto contains the generated protobuf code for the Android TV
// remote v2 protocol (pairing and remote messages).
package proto

//go:generate protoc --proto_path=. --go_out=. --go_opt=paths=source_relative pairingmessage.proto remotemessage.proto
