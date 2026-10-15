package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/dynamicpb"
)

type rpcMethodOutput struct {
	Service         string `json:"service"`
	Method          string `json:"method"`
	FullMethod      string `json:"full_method"`
	InputType       string `json:"input_type"`
	OutputType      string `json:"output_type"`
	ClientStreaming bool   `json:"client_streaming"`
	ServerStreaming bool   `json:"server_streaming"`
}

func (r Runner) runRPCList(opts commandOptions) error {
	methods := generatedRPCMethods()
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, map[string]any{"methods": methods})
	}
	if opts.Quiet {
		return nil
	}
	for _, method := range methods {
		streaming := ""
		if method.ClientStreaming || method.ServerStreaming {
			streaming = " streaming"
		}
		fmt.Fprintf(r.Stdout, "%s%s\n", strings.TrimPrefix(method.FullMethod, "/"), streaming)
	}
	return nil
}

func (r Runner) runRPCCall(ctx context.Context, opts commandOptions, selector, rawRequest, serverAddr string, unauthenticated bool) error {
	method, err := findGeneratedRPCMethod(selector)
	if err != nil {
		return err
	}
	if method.IsStreamingClient() || method.IsStreamingServer() {
		return userError("unsupported_rpc", "streaming RPCs are not supported by gs rpc call", "Use a dedicated CLI command for streaming workflows.")
	}
	cfg := UserConfig{}
	if unauthenticated {
		if serverAddr == "" {
			partial, err := r.readPartialUserConfig()
			if err != nil {
				return err
			}
			serverAddr = partial.ServerAddr
		}
		if serverAddr == "" {
			serverAddr = defaultServerAddr()
		}
	} else {
		cfg, err = r.readUserConfig()
		if err != nil {
			return err
		}
		if serverAddr == "" {
			serverAddr = cfg.ServerAddr
		}
	}
	conn, err := dial(ctx, serverAddr)
	if err != nil {
		return err
	}
	defer conn.Close()

	req := dynamicpb.NewMessage(method.Input())
	unmarshaler := protojson.UnmarshalOptions{DiscardUnknown: false}
	if err := unmarshaler.Unmarshal([]byte(rawRequest), req); err != nil {
		return userError("invalid_rpc_request", "invalid RPC request JSON: "+err.Error(), "Use gs rpc list to find the method, then pass request JSON with --request.")
	}
	res := dynamicpb.NewMessage(method.Output())
	callCtx := ctx
	if !unauthenticated {
		callCtx = authContext(ctx, cfg)
	}
	fullMethod := "/" + string(method.Parent().FullName()) + "/" + string(method.Name())
	if err := conn.Invoke(callCtx, fullMethod, req, res); err != nil {
		return err
	}
	data, err := protojson.MarshalOptions{Indent: "  ", UseProtoNames: true}.Marshal(res)
	if err != nil {
		return err
	}
	if len(opts.JSONFields) > 0 {
		var obj map[string]any
		if err := json.Unmarshal(data, &obj); err != nil {
			return err
		}
		return r.writeJSONOutput(opts, obj)
	}
	_, err = fmt.Fprintln(r.Stdout, string(data))
	return err
}

func generatedRPCMethods() []rpcMethodOutput {
	methods := []rpcMethodOutput{}
	protoregistry.GlobalFiles.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		if fd.Package() != "gitslice.core.v1" {
			return true
		}
		services := fd.Services()
		for i := 0; i < services.Len(); i++ {
			service := services.Get(i)
			serviceName := string(service.FullName())
			serviceMethods := service.Methods()
			for j := 0; j < serviceMethods.Len(); j++ {
				method := serviceMethods.Get(j)
				methods = append(methods, rpcMethodOutput{
					Service:         serviceName,
					Method:          string(method.Name()),
					FullMethod:      "/" + serviceName + "/" + string(method.Name()),
					InputType:       string(method.Input().FullName()),
					OutputType:      string(method.Output().FullName()),
					ClientStreaming: method.IsStreamingClient(),
					ServerStreaming: method.IsStreamingServer(),
				})
			}
		}
		return true
	})
	sort.Slice(methods, func(i, j int) bool {
		return methods[i].FullMethod < methods[j].FullMethod
	})
	return methods
}

func findGeneratedRPCMethod(selector string) (protoreflect.MethodDescriptor, error) {
	selector = strings.TrimSpace(strings.TrimPrefix(selector, "/"))
	if selector == "" {
		return nil, userError("invalid_args", "RPC method is required", "Use gs rpc call <service>/<method>.")
	}
	parts := strings.Split(selector, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return nil, userError("invalid_args", "RPC method must be <service>/<method>", "Example: gs rpc call AuthService/GetAuthStatus --request '{}'.")
	}
	serviceName := parts[0]
	if !strings.Contains(serviceName, ".") {
		serviceName = "gitslice.core.v1." + serviceName
	}
	desc, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(serviceName))
	if err != nil {
		return nil, userError("unknown_rpc", "unknown RPC service "+parts[0], "Run gs rpc list to show generated core RPC methods.")
	}
	service, ok := desc.(protoreflect.ServiceDescriptor)
	if !ok {
		return nil, userError("unknown_rpc", "descriptor is not an RPC service "+parts[0], "Run gs rpc list to show generated core RPC methods.")
	}
	method := service.Methods().ByName(protoreflect.Name(parts[1]))
	if method == nil {
		return nil, userError("unknown_rpc", "unknown RPC method "+selector, "Run gs rpc list to show generated core RPC methods.")
	}
	return method, nil
}
