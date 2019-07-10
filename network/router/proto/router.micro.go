// Code generated by protoc-gen-micro. DO NOT EDIT.
// source: router.proto

package router

import (
	fmt "fmt"
	proto "github.com/golang/protobuf/proto"
	math "math"
)

import (
	context "context"
	client "github.com/micro/go-micro/client"
	server "github.com/micro/go-micro/server"
)

// Reference imports to suppress errors if they are not otherwise used.
var _ = proto.Marshal
var _ = fmt.Errorf
var _ = math.Inf

// This is a compile-time assertion to ensure that this generated file
// is compatible with the proto package it is being compiled against.
// A compilation error at this line likely means your copy of the
// proto package needs to be updated.
const _ = proto.ProtoPackageIsVersion3 // please upgrade the proto package

// Reference imports to suppress errors if they are not otherwise used.
var _ context.Context
var _ client.Option
var _ server.Option

// Client API for Router service

type RouterService interface {
	Watch(ctx context.Context, in *WatchRequest, opts ...client.CallOption) (Router_WatchService, error)
	Lookup(ctx context.Context, in *LookupRequest, opts ...client.CallOption) (*LookupResponse, error)
	List(ctx context.Context, in *ListRequest, opts ...client.CallOption) (*ListResponse, error)
}

type routerService struct {
	c    client.Client
	name string
}

func NewRouterService(name string, c client.Client) RouterService {
	if c == nil {
		c = client.NewClient()
	}
	if len(name) == 0 {
		name = "router"
	}
	return &routerService{
		c:    c,
		name: name,
	}
}

func (c *routerService) Watch(ctx context.Context, in *WatchRequest, opts ...client.CallOption) (Router_WatchService, error) {
	req := c.c.NewRequest(c.name, "Router.Watch", &WatchRequest{})
	stream, err := c.c.Stream(ctx, req, opts...)
	if err != nil {
		return nil, err
	}
	if err := stream.Send(in); err != nil {
		return nil, err
	}
	return &routerServiceWatch{stream}, nil
}

type Router_WatchService interface {
	SendMsg(interface{}) error
	RecvMsg(interface{}) error
	Close() error
	Recv() (*TableEvent, error)
}

type routerServiceWatch struct {
	stream client.Stream
}

func (x *routerServiceWatch) Close() error {
	return x.stream.Close()
}

func (x *routerServiceWatch) SendMsg(m interface{}) error {
	return x.stream.Send(m)
}

func (x *routerServiceWatch) RecvMsg(m interface{}) error {
	return x.stream.Recv(m)
}

func (x *routerServiceWatch) Recv() (*TableEvent, error) {
	m := new(TableEvent)
	err := x.stream.Recv(m)
	if err != nil {
		return nil, err
	}
	return m, nil
}

func (c *routerService) Lookup(ctx context.Context, in *LookupRequest, opts ...client.CallOption) (*LookupResponse, error) {
	req := c.c.NewRequest(c.name, "Router.Lookup", in)
	out := new(LookupResponse)
	err := c.c.Call(ctx, req, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *routerService) List(ctx context.Context, in *ListRequest, opts ...client.CallOption) (*ListResponse, error) {
	req := c.c.NewRequest(c.name, "Router.List", in)
	out := new(ListResponse)
	err := c.c.Call(ctx, req, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Server API for Router service

type RouterHandler interface {
	Watch(context.Context, *WatchRequest, Router_WatchStream) error
	Lookup(context.Context, *LookupRequest, *LookupResponse) error
	List(context.Context, *ListRequest, *ListResponse) error
}

func RegisterRouterHandler(s server.Server, hdlr RouterHandler, opts ...server.HandlerOption) error {
	type router interface {
		Watch(ctx context.Context, stream server.Stream) error
		Lookup(ctx context.Context, in *LookupRequest, out *LookupResponse) error
		List(ctx context.Context, in *ListRequest, out *ListResponse) error
	}
	type Router struct {
		router
	}
	h := &routerHandler{hdlr}
	return s.Handle(s.NewHandler(&Router{h}, opts...))
}

type routerHandler struct {
	RouterHandler
}

func (h *routerHandler) Watch(ctx context.Context, stream server.Stream) error {
	m := new(WatchRequest)
	if err := stream.Recv(m); err != nil {
		return err
	}
	return h.RouterHandler.Watch(ctx, m, &routerWatchStream{stream})
}

type Router_WatchStream interface {
	SendMsg(interface{}) error
	RecvMsg(interface{}) error
	Close() error
	Send(*TableEvent) error
}

type routerWatchStream struct {
	stream server.Stream
}

func (x *routerWatchStream) Close() error {
	return x.stream.Close()
}

func (x *routerWatchStream) SendMsg(m interface{}) error {
	return x.stream.Send(m)
}

func (x *routerWatchStream) RecvMsg(m interface{}) error {
	return x.stream.Recv(m)
}

func (x *routerWatchStream) Send(m *TableEvent) error {
	return x.stream.Send(m)
}

func (h *routerHandler) Lookup(ctx context.Context, in *LookupRequest, out *LookupResponse) error {
	return h.RouterHandler.Lookup(ctx, in, out)
}

func (h *routerHandler) List(ctx context.Context, in *ListRequest, out *ListResponse) error {
	return h.RouterHandler.List(ctx, in, out)
}
