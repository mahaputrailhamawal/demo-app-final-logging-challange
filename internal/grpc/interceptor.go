package grpc

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/logging"
	"github.com/rs/zerolog/log"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func Logger() logging.Logger {
	return logging.LoggerFunc(func(ctx context.Context, lvl logging.Level, msg string, fields ...any) {
		l := log.Ctx(ctx).With().Fields(fields).Logger()
		switch lvl {
		case logging.LevelDebug:
			l.Debug().Msg(msg)
		case logging.LevelInfo:
			l.Info().Msg(msg)
		case logging.LevelWarn:
			l.Warn().Msg(msg)
		case logging.LevelError:
			l.Error().Msg(msg)
		default:
			panic(fmt.Sprintf("unknown level %v", lvl))
		}
	})
}

var loggingOpts = []logging.Option{
	logging.WithLogOnEvents(
		logging.StartCall,
		logging.PayloadReceived,
		logging.PayloadSent,
		logging.FinishCall,
	),
}

func StreamServerGRPCLoggerInterceptor(opts ...logging.Option) grpc.StreamServerInterceptor {
	options := loggingOpts
	if len(opts) > 0 {
		options = opts
	}
	return logging.StreamServerInterceptor(Logger(), options...)
}

func UnaryServerGRPCLoggerInterceptor(opts ...logging.Option) grpc.UnaryServerInterceptor {
	options := loggingOpts
	if len(opts) > 0 {
		options = opts
	}
	return logging.UnaryServerInterceptor(Logger(), options...)
}

func UnaryClientGRPCLoggerInterceptor(opts ...logging.Option) grpc.UnaryClientInterceptor {
	options := loggingOpts
	if len(opts) > 0 {
		options = opts
	}
	return logging.UnaryClientInterceptor(Logger(), options...)
}

func StreamClientGRPCLoggerInterceptor(opts ...logging.Option) grpc.StreamClientInterceptor {
	options := loggingOpts
	if len(opts) > 0 {
		options = opts
	}
	return logging.StreamClientInterceptor(Logger(), options...)
}

func UnaryServerAppLoggerInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		log := log.With().Str("request_id", uuid.New().String()).Logger()
		return handler(log.WithContext(ctx), req)
	}
}

func StreamServerAppLoggerInterceptor() grpc.StreamServerInterceptor {
	return func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		err := handler(srv, newWrappedStream(ss))
		if err != nil {
			log.Error().Err(err).Msgf("Error: %v", err)
			return err
		}
		return nil
	}
}

type wrappedStream struct {
	grpc.ServerStream
}

func (w *wrappedStream) Context() context.Context {
	log := log.With().Str("request_id", uuid.New().String()).
		Logger()
	return log.WithContext(context.Background())
}

func newWrappedStream(s grpc.ServerStream) grpc.ServerStream {
	return &wrappedStream{s}
}

func UnaryServerErrorInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler) (interface{}, error) {
		resp, err := handler(ctx, req)
		if err != nil {
			return nil, convertToGRPCError(err)
		}
		return resp, nil
	}
}

func convertToGRPCError(err error) error {
	// Check if error already has gRPC status
	if _, ok := status.FromError(err); ok {
		return err
	}

	// Unwrap and check context errors more aggressively
	unwrappedErr := err
	for unwrappedErr != nil {
		// Check context.Canceled
		if errors.Is(unwrappedErr, context.Canceled) {
			return status.Error(codes.Canceled, "request was canceled")
		}
		// Check context.DeadlineExceeded
		if errors.Is(unwrappedErr, context.DeadlineExceeded) {
			return status.Error(codes.DeadlineExceeded, "request deadline exceeded")
		}
		// Unwrap one level
		unwrappedErr = errors.Unwrap(unwrappedErr)
	}

	// Also check error message as fallback (for deeply wrapped errors)
	errMsg := err.Error()
	log.Debug().
		Str("error_type", fmt.Sprintf("%T", err)).
		Str("error_msg", errMsg).
		Msg("converting error to gRPC status")
	if strings.Contains(errMsg, "context canceled") {
		return status.Error(codes.Canceled, "request was canceled")
	}
	if strings.Contains(errMsg, "context deadline exceeded") {
		return status.Error(codes.DeadlineExceeded, "request deadline exceeded")
	}

	// Handle database connection errors
	if strings.Contains(errMsg, "driver: bad connection") ||
		strings.Contains(errMsg, "connection refused") ||
		strings.Contains(errMsg, "connection reset") ||
		strings.Contains(errMsg, "broken pipe") {
		return status.Error(codes.Unavailable, "database connection unavailable")
	}

	// Handle booking-specific errors by message
	if strings.Contains(errMsg, "booking already expired") {
		return status.Error(codes.FailedPrecondition, "booking already expired")
	}
	if strings.Contains(errMsg, "reservation max retry exceeded") {
		return status.Error(codes.ResourceExhausted, "reservation max retry exceeded")
	}
	if strings.Contains(errMsg, "booking release max retry exceeded") {
		return status.Error(codes.ResourceExhausted, "booking release max retry exceeded")
	}

	// Handle seat availability errors
	if strings.Contains(errMsg, "class is sold out") ||
		strings.Contains(errMsg, "no seat available") {
		return status.Error(codes.ResourceExhausted, "seats are not available")
	}
	if strings.Contains(errMsg, "class is not available for sale") {
		return status.Error(codes.FailedPrecondition, "class is not available for sale")
	}

	// Handle PostgreSQL UUID errors
	if strings.Contains(errMsg, "invalid input syntax for type uuid") {
		return status.Error(codes.InvalidArgument, "invalid UUID format")
	}

	// Default to Internal error for unexpected errors
	return status.Error(codes.Internal, err.Error())
}
