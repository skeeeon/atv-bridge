package remote

import (
	"context"
	"log/slog"

	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/proto"
)

// logDebugProto logs a protobuf message at debug level. Prototext formatting
// is reflection-heavy, so skip it entirely unless debug logging is enabled.
func logDebugProto(logger *slog.Logger, action string, msg proto.Message) {
	if logger.Enabled(context.Background(), slog.LevelDebug) {
		logger.Debug(action, "msg", prototext.MarshalOptions{}.Format(msg))
	}
}
