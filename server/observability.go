package server

import (
	"context"
	"log/slog"
	"time"

	"github.com/gitslice-io/gitslice/internal/metrics"
	"github.com/gitslice-io/gitslice/internal/requestid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

var (
	grpcRequestsTotal = metrics.NewCounter(
		"gitslice_grpc_requests_total",
		"gRPC requests by method and final status code.",
		"method",
		"code",
	)
	grpcRequestLatencySeconds = metrics.NewHistogram(
		"gitslice_grpc_request_latency_seconds",
		"gRPC request latency by method and final status code.",
		[]float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30},
		"method",
		"code",
	)
)

func requestIDUnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		id := requestid.New()
		_ = grpc.SetHeader(ctx, metadata.Pairs("x-request-id", id))
		return handler(requestid.With(ctx, id), req)
	}
}

func requestIDStreamInterceptor() grpc.StreamServerInterceptor {
	return func(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		id := requestid.New()
		_ = stream.SetHeader(metadata.Pairs("x-request-id", id))
		return handler(srv, &contextServerStream{
			ServerStream: stream,
			ctx:          requestid.With(stream.Context(), id),
		})
	}
}

func grpcMetricsUnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		start := time.Now()
		resp, err := handler(ctx, req)
		recordGRPCRequest(ctx, info.FullMethod, status.Code(err), time.Since(start), err)
		return resp, err
	}
}

func grpcMetricsStreamInterceptor() grpc.StreamServerInterceptor {
	return func(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		start := time.Now()
		err := handler(srv, stream)
		recordGRPCRequest(stream.Context(), info.FullMethod, status.Code(err), time.Since(start), err)
		return err
	}
}

func recordGRPCRequest(ctx context.Context, method string, code codes.Code, duration time.Duration, err error) {
	codeLabel := code.String()
	labels := metrics.Labels{"method": method, "code": codeLabel}
	grpcRequestsTotal.Inc(labels)
	grpcRequestLatencySeconds.Observe(duration.Seconds(), labels)

	id, _ := requestid.From(ctx)
	args := []any{
		"request_id", id,
		"grpc_method", method,
		"grpc_code", codeLabel,
		"duration_ms", duration.Milliseconds(),
	}
	if err != nil {
		args = append(args, "error", err)
		slog.WarnContext(ctx, "grpc request failed", args...)
		return
	}
	slog.InfoContext(ctx, "grpc request completed", args...)
}
