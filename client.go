package GanRPC

import (
	"GanRPC/codec"
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

/*
远程调用的func的格式：
func (t *T) MethodName(argType T1, replyType *T2) error
*/

//封装了结构体 Call 来承载一次 RPC 调用所需要的信息。
type Call struct {
	Seq           uint64
	ServiceMethod string      // format "<service>.<method>"
	Args          interface{} // arguments to the function
	Reply         interface{} // reply from the function
	Error         error       // if error occurs, it will be set
	Done          chan *Call  // Strobes when call is complete.
}

func (call *Call) done() {
	call.Done <- call
}

// Client represents an RPC Client.
// There may be multiple outstanding Calls associated
// with a single Client, and a Client may be used by
// multiple goroutines simultaneously.
type Client struct {
	cc      codec.Codec
	opt      *Option
	sending  sync.Mutex // protect following
	header   codec.Header
	mu       sync.Mutex // protect following
	seq      uint64
	pending  map[uint64]*Call
	closing  bool // user has called Close
	shutdown bool // server has told us to stop
}

type createClientResult struct {
	client *Client
	err    error
}

var ErrShutdown = errors.New("connection is shut down")

type newClientFunc func(conn net.Conn, opt *Option) (client *Client, err error)

// Dial connects to an RPC server at the specified network address
func dialTimeout(f newClientFunc,network, address string, opt *Option) (client *Client, err error) {
	//连接服务器，加超时
	var conn net.Conn
	conn, err = net.DialTimeout(network, address, opt.ConnectTimeout)
	if err != nil {
		return
	}
	defer func() {
		if err != nil {
			_ = conn.Close()
		}
	}()

	//连接成功，接下来沟通协议
	ch := make(chan createClientResult)
	//另外开个协程来处理，本协程负责等通知。
	go func() {
		client, err := f(conn, opt)
		ch <- createClientResult{client: client, err: err}
	}()

	//如果没设置超时，那么就一直等。
	if opt.ConnectTimeout == 0 {
		r := <-ch
		return r.client, nil
	}

	//设置了超时，那就还要等超时的通知，如果超时的通知先到，那么就超时了。
	select {
	case <-time.After(opt.ConnectTimeout):	//timeout
		err = errors.New("create client time out")
		return
	case r := <-ch:	//结果
		client = r.client
		err = r.err
		return
	}
}


func NewClient(conn net.Conn, opt *Option) (*Client, error) {
	//把opt发给服务器，以确定编解码器
	err := json.NewEncoder(conn).Encode(opt)
	if err != nil {
		return nil, errors.New("create client error : " + err.Error())
	}
	//获取server的回复
	r := ConnResult{}
	err = json.NewDecoder(conn).Decode(&r)
	if err != nil {
		return nil, errors.New("create client error : " + err.Error())
	}
	if r.Status != "OK" {
		return nil, errors.New(r.Error)
	}

	//和server成功沟通好编解码器，于是构建client
	newFunc, ok := codec.NewCodecFuncMap[opt.CodecType]
	if !ok {
		return nil, errors.New("can not find coderType : " + string(opt.CodecType))
	}
	cc := newFunc(conn)

	client := &Client{
		cc: cc,
		opt: opt,
		seq: 1,	// seq starts with 1, 0 means invalid call
		pending: make(map[uint64]*Call),
	}

	//开一个协程，用于接收回应，处理完后通知对应的协程
	go client.receive()

	return client, nil
}

// Dial connects to an RPC server at the specified network address
func Dial(network, address string, opt *Option) (*Client, error) {
	return dialTimeout(NewClient, network, address, opt)
}

// NewHTTPClient new a Client instance via HTTP as transport protocol
func NewHTTPClient(conn net.Conn, opt *Option) (*Client, error) {
	_, _ = io.WriteString(conn, fmt.Sprintf("CONNECT %s HTTP/1.0\n\n", defaultRPCPath))

	// Require successful HTTP response
	// before switching to RPC protocol.
	resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: "CONNECT"})
	if err == nil && resp.Status == connected {
		return NewClient(conn, opt)
	}
	if err == nil {
		err = errors.New("unexpected HTTP response: " + resp.Status)
	}
	return nil, err
}

// DialHTTP connects to an HTTP RPC server at the specified network address
// listening on the default HTTP RPC path.
func DialHTTP(network, address string, opt *Option) (*Client, error) {
	return dialTimeout(NewHTTPClient, network, address, opt)
}

// XDial calls different functions to connect to a RPC server
// according the first parameter rpcAddr.
// rpcAddr is a general format (protocol@addr) to represent a rpc server
// eg, http@10.0.0.1:7001, tcp@10.0.0.1:9999, unix@/tmp/geerpc.sock
func XDial(rpcAddr string, opt *Option) (*Client, error) {
	parts := strings.Split(rpcAddr, "@")
	if len(parts) != 2 {
		return nil, fmt.Errorf("rpc client err: wrong format '%s', expect protocol@addr", rpcAddr)
	}
	protocol, addr := parts[0], parts[1]
	switch protocol {
	case "http":
		return DialHTTP("tcp", addr, opt)
	default:
		// tcp, unix or other transport protocol
		return Dial(protocol, addr, opt)
	}
}


/*
Go 和 Call 是客户端暴露给用户的两个 RPC 服务调用接口，Go 是一个异步接口，返回 call 实例。
Call 是对 Go 的封装，阻塞 call.Done，等待响应返回，是一个同步接口。
*/

// Call invokes the named function, waits for it to complete,
// and returns its error status.
func (client *Client) Call(serviceMethod string, args, reply interface{}) error{
	done := make(chan *Call) //当执行receive的协程收到本协程的回应时，就是向done写入*call，用以通知本协程
	call := client.Go(serviceMethod, args, reply, done)

	//如果没设置超时，那么就一直等done的消息
	if client.opt.HandleTimeout == 0 {
		<-done
		return call.Error
	}

	//设置了超时，那就看done和time.After谁先来消息；time.After的通知先来，那就说明超时了。
	select {
	//阻塞并等待执行receive的协程的通知;收到执行receive的协程的通知，那么就说明1、请求的回应来了；2、出错了
	case <-done :
		return call.Error
	case <-time.After(time.Second * 10):	//超时
		return errors.New("timeout")
	}
}

func (client *Client) Go(serviceMethod string, args, reply interface{}, done chan *Call) *Call {
	if done == nil {
		done = make(chan *Call)
	}

	c := &Call{
		ServiceMethod: serviceMethod,
		Args: args,
		Reply: reply,
		Done: done,
	}
	client.send(c)
	return c
}


func (client *Client) send(call *Call) {
	//因为是并发的，先加锁，发送完整个header和body再解锁，避免不同call的header和body交叉，导致混乱.
	//串行并不影响效率，毕竟io本就是串行的。
	client.sending.Lock()
	defer client.sending.Unlock()

	// register this call.
	seq, err := client.registerCall(call)
	if err != nil{
		call.Error = err
		call.done()
		return
	}

	// prepare request header
	herder := codec.Header{
		Seq: seq,
		ServiceMethod: call.ServiceMethod,
		Error: "",
	}

	//把header和body编码成二进制流，然后发送到server
	err = client.cc.Write(&herder, call.Args)
	if err != nil{
		call := client.removeCall(seq)
		// call may be nil, it usually means that Write partially failed,
		// client has received the response and handled
		if call != nil {
			call.Error = err
			call.done()
		}
	}
}

func (client *Client) receive() {
	var breakErr error
	for breakErr == nil {	//不能正常读取header或body，那说明编码信息错乱了，所以就没必要继续下去了。
		h := codec.Header{}
		breakErr = client.cc.ReadHeader(&h)
		if breakErr != nil {
			break
		}

		//拿到header后，根据header的请求序号seq从挂起的calls中取出对应的call
		call := client.removeCall(h.Seq)
		//拿到的call有这几种情况
		switch  {
		case call == nil:
			// it usually means that Write partially failed
			// and call was already removed.
			breakErr = client.cc.ReadBody(nil)		//消耗掉对应的body（一个header对应一个body）。
		case h.Error != "":
			//服务端返回了错误信息，于是通知对应的处理协程
			call.Error = errors.New(h.Error)
			breakErr = client.cc.ReadBody(nil)		//消耗掉对应的body（一个header对应一个body）。
			//唤醒阻塞协程
			call.done()
		default:	//正常处理
			//返回的body解码到call指定的对象中(Reply)
			breakErr = client.cc.ReadBody(call.Reply)
			if breakErr != nil {
				call.Error = errors.New("read body error : " + breakErr.Error())
			}
			//唤醒阻塞协程
			call.done()
		}
	}

	// error occurs, so terminateCalls pending calls
	client.terminateCalls(breakErr)
}


func (client *Client) registerCall(call *Call) (uint64, error) {
	client.mu.Lock()
	defer client.mu.Unlock()

	if client.closing || client.shutdown {
		return 0, ErrShutdown
	}
	call.Seq = client.seq
	client.seq ++
	client.pending[call.Seq] = call
	return call.Seq, nil
}

func (client *Client) removeCall(seq uint64) *Call {
	client.mu.Lock()
	defer client.mu.Unlock()
	if call, ok := client.pending[seq]; ok{
		delete(client.pending, seq)
		return call
	}
	return nil
}

//服务端或客户端发生错误时调用，将 shutdown 设置为 true，且将错误信息通知所有 pending 状态的 call。
func (client *Client) terminateCalls(err error) {
	client.sending.Lock()
	defer client.sending.Unlock()
	client.mu.Lock()
	defer client.mu.Unlock()
	client.shutdown = true
	for _, call := range client.pending {
		call.Error = err
		call.done()
	}
}

// Close the connection
func (client *Client) Close() error {
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.closing {
		return ErrShutdown
	}
	client.closing = true
	return client.cc.Close()
}

// IsAvailable return true if the client does work
func (client *Client) IsAvailable() bool {
	client.mu.Lock()
	defer client.mu.Unlock()
	return !client.shutdown && !client.closing
}

