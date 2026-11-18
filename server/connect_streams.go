package server

import (
	"context"
	"errors"
	"io"

	"connectrpc.com/connect"
	"gitslice.io/gitslice/internal/metrics"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type connectGRPCServerStream struct {
	ctx context.Context
}

func (s connectGRPCServerStream) SetHeader(metadata.MD) error {
	return nil
}

func (s connectGRPCServerStream) SendHeader(metadata.MD) error {
	return nil
}

func (s connectGRPCServerStream) SetTrailer(metadata.MD) {}

func (s connectGRPCServerStream) Context() context.Context {
	return s.ctx
}

func (s connectGRPCServerStream) SendMsg(any) error {
	return status.Error(codes.Unimplemented, "SendMsg is not implemented by connect stream adapter")
}

func (s connectGRPCServerStream) RecvMsg(any) error {
	return status.Error(codes.Unimplemented, "RecvMsg is not implemented by connect stream adapter")
}

// connectServerStream adapts a Connect ServerStream[M] to a gRPC server-streaming
// interface (grpc.ServerStream + Send(*M) error).
type connectServerStream[M any] struct {
	connectGRPCServerStream
	stream *connect.ServerStream[M]
}

func newConnectServerStream[M any](ctx context.Context, stream *connect.ServerStream[M]) connectServerStream[M] {
	return connectServerStream[M]{
		connectGRPCServerStream: connectGRPCServerStream{ctx: ctx},
		stream:                  stream,
	}
}

func (s connectServerStream[M]) Send(msg *M) error {
	return s.stream.Send(msg)
}

// connectClientStream adapts a Connect ClientStream[Req] to a gRPC client-streaming
// interface (grpc.ServerStream + Recv() (*Req, error) + SendAndClose(*Resp) error).
type connectClientStream[Req, Resp any] struct {
	connectGRPCServerStream
	stream   *connect.ClientStream[Req]
	response *Resp
}

func newConnectClientStream[Req, Resp any](ctx context.Context, stream *connect.ClientStream[Req]) *connectClientStream[Req, Resp] {
	return &connectClientStream[Req, Resp]{
		connectGRPCServerStream: connectGRPCServerStream{ctx: ctx},
		stream:                  stream,
	}
}

func (s *connectClientStream[Req, Resp]) Recv() (*Req, error) {
	if !s.stream.Receive() {
		if err := s.stream.Err(); err != nil {
			return nil, connectError(err)
		}
		return nil, io.EOF
	}
	return s.stream.Msg(), nil
}

func (s *connectClientStream[Req, Resp]) SendAndClose(msg *Resp) error {
	s.response = msg
	return nil
}

func (s *connectClientStream[Req, Resp]) finish() (*connect.Response[Resp], error) {
	if s.response == nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("stream completed without response"))
	}
	return connect.NewResponse(s.response), nil
}

// connectBidiStream adapts a Connect BidiStream[Req, Resp] to a gRPC bidirectional
// streaming interface (grpc.ServerStream + Send(*Resp) error + Recv() (*Req, error)).
type connectBidiStream[Req, Resp any] struct {
	connectGRPCServerStream
	stream *connect.BidiStream[Req, Resp]
}

func newConnectBidiStream[Req, Resp any](ctx context.Context, stream *connect.BidiStream[Req, Resp]) connectBidiStream[Req, Resp] {
	return connectBidiStream[Req, Resp]{
		connectGRPCServerStream: connectGRPCServerStream{ctx: ctx},
		stream:                  stream,
	}
}

func (s connectBidiStream[Req, Resp]) Send(msg *Resp) error {
	return s.stream.Send(msg)
}

func (s connectBidiStream[Req, Resp]) Recv() (*Req, error) {
	msg, err := s.stream.Receive()
	if err != nil {
		return nil, connectError(err)
	}
	return msg, nil
}

func connectTriggerRunFailureInterceptor(limiters *serverLimiters) connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			resp, err := next(ctx, req)
			if err != nil && limiters != nil && limiters.triggerFail != nil {
				switch connectStatusCode(err) {
				case codes.Unauthenticated, codes.PermissionDenied:
					if ipKey, ok := clientIPKeyFromContext(ctx); ok && !limiters.triggerFail.Allow(ipKey) {
						ratelimitRejectedTotal.Inc(metrics.Labels{"transport": "http"})
						return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("trigger run authentication failure rate limit exceeded"))
					}
				}
			}
			return resp, err
		}
	}
}
