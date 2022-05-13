package GanRPC

import (
	"GanRPC/codec"
	"encoding/json"
	"github.com/pkg/errors"
	"io"
	"log"
	"net"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"time"
)

/*通信格式
| Option{MagicNumber: xxx, EcType: xxx} | Header{ServiceMethod ...} | Body interface{} |
| <------      json      ------>  | <-------   编码方式由 EcType 决定   ------->|

在一次连接中，Option 固定在报文的最开始，Header 和 Body 可以有多个，即报文可能是这样的：
| Option | Header1 | Body1 | Header2 | Body2 | ...
*/

//ganrpc request标志
const MagicNumber = 0x3bef5c

type Option struct {
	MagicNumber    int           // MagicNumber marks this's a geerpc request
	CodecType      codec.CoderType    // client may choose different Codec to encode body
	ConnectTimeout time.Duration // 0 means no limit
	HandleTimeout  time.Duration
}

var DefaultOption = &Option{
	MagicNumber:    MagicNumber,
	CodecType:      codec.GobType,
	ConnectTimeout: time.Second * 10,
}

// Server represents an RPC Server.
type Server struct{
	services map[string]*service
}

// NewServer returns a new Server.
func NewServer() *Server {
	server :=  &Server{}
	server.services = make(map[string]*service)
	return server
}

func (server *Server) RegisterService(rcvr interface{}) error{
	service := newService(rcvr)
	server.services[service.name] = service
	return nil
}

func RegisterService(rcvr interface{})  error{
	return	DefaultServer.RegisterService(rcvr)
}

func (server *Server) findService(serviceMethod string) (svc *service, mtype *methodType, err error) {
	parts := strings.Split(serviceMethod, ".")
	if len(parts) != 2 {
		err = errors.New("rpc server : expect format service.method but : " + serviceMethod)
		return
	}
	serviceName, methodName := parts[0], parts[1]
	svc, ok := server.services[serviceName]
	if !ok {
		err = errors.New("rpc server : can't find service : " + serviceName)
		return
	}
	mtype, ok = svc.method[methodName]
	if !ok{
		err = errors.New("rpc server : " + serviceName + " not have method : " + methodName)
		return
	}
	return
}



// request stores all information of a call
type request struct {
	h            *codec.Header // header of request
	argv, replyv reflect.Value // argv封装客户端的请求入参；service计算出结果后，把结果封装进replyv中，最后返回客户端reolyv中的信息。
	mtype        *methodType
	svc          *service
}

//一个默认的 Server 实例，主要为了用户使用方便。只需要一个server的情况下，就用这个DefaultServer即可，不用New一个server对象。
var DefaultServer = NewServer()
// Accept accepts connections on the listener and serves requests
// for each incoming connection.
func Accept(listener net.Listener) { DefaultServer.Accept(listener) }

// Accept accepts connections on the listener and serves requests
// for each incoming connection.
func (server *Server) Accept(listener net.Listener) {
	for  {
		conn, err := listener.Accept()
		if err != nil {
			log.Println(err)
			return
		}
		go server.ServeConn(conn)
	}
}

type ConnResult struct {
	Status string
	Error string
}

// ServeConn runs the server on a single connection.
// ServeConn blocks, serving the connection until the client hangs up.
func (server *Server) ServeConn(conn io.ReadWriteCloser) {
	//处理完，或者遇到错误，退出之前关conn
	defer func() {
		_ = conn.Close()
	}()

	//首先解析opt，和client沟通编码格式
	opt := Option{}
	r := ConnResult{}
	err := json.NewDecoder(conn).Decode(&opt)	//解析json字节流，并把解析出的信息写入opt
	if err != nil {
		r.Status = "error"
		r.Error = err.Error()
		_ = json.NewEncoder(conn).Encode(r)
		return
	}
	//是否是GanRPC标志
	if opt.MagicNumber != MagicNumber {
		r.Status = "error"
		r.Error = "invalid magic number"
		_ = json.NewEncoder(conn).Encode(r)
		return
	}

	//看是用什么编码的
	coderType := opt.CodecType
	//获取对应的编解码器
	newFunc, ok := codec.NewCodecFuncMap[coderType]
	if !ok {
		r.Status = "error"
		r.Error = "not have this coderType"
		_ = json.NewEncoder(conn).Encode(r)
		return
	}

	//回应client
	r.Status = "OK"
	r.Error = ""
	_ = json.NewEncoder(conn).Encode(r)

	//构建编解码器
	cc := newFunc(conn)
	//开始处理call（header+body）
	server.serveCodec(cc)
}

// invalidRequest is a placeholder for response argv when error occurs
var invalidRequest = struct{}{}

func (server *Server) serveCodec(edc codec.Codec){
	sending := new(sync.Mutex) // make sure to send a complete response
	wg := new(sync.WaitGroup)  // wait until all request are handled
	for  {
		req, err := server.createRequest(edc)
		if err != nil{
			if err == io.EOF {	//正常读取结束
				break
			}else {	//非正常结束，返回错误信息
				h := codec.Header{}
				h.Error = err.Error()
				server.sendResponse(edc, &h, invalidRequest, sending)
				break
			}
		}
		wg.Add(1)
		go server.handleRequest(edc, req, sending, wg)
	}

	//结束前要等所有处理都完成
	wg.Wait()
}


//构建一个request,解析编码后的二进制流，然后把请求信息封装进request中。
func (server *Server) createRequest(edc codec.Codec) (*request, error){
	//获取header
	header := codec.Header{}
	if err := edc.ReadHeader(&header); err != nil {
		if err != io.EOF && err != io.ErrUnexpectedEOF { //未知错误，打印信息
			log.Println("rpc server: read header error:", err)
		}
		return nil, err
	}

	req := request{}
	req.h = &header

	service, mType, err := server.findService(header.ServiceMethod)
	if err != nil {
		//消耗掉接下来的body
		edc.ReadBody(nil)
		return nil, err
	}

	req.svc = service
	req.mtype = mType

	//动态创建一个类型的变量。（在堆中创建变量，然后把这个变量的指针和类型信息封装进Value，返回这个Value）
	req.argv = mType.newArgv()
	req.replyv = mType.newReplyv()

	// make sure that argvi is a pointer, ReadBody need a pointer as parameter
	argvi := req.argv.Interface()
	if req.argv.Type().Kind() != reflect.Ptr {
		argvi = req.argv.Addr().Interface()
	}
	//将请求报文反序列化为第一个入参 argv
	if err := edc.ReadBody(argvi); err != nil {
		return nil, errors.New("rpc server: read argv err:" + err.Error())
	}

	return &req, nil
}

func (server *Server) handleRequest(edc codec.Codec, req *request, sending *sync.Mutex, wg *sync.WaitGroup) {
	defer wg.Done()

	err := req.svc.call(req.mtype, req.argv, req.replyv)
	if err != nil {
		req.h.Error = err.Error()
		server.sendResponse(edc, req.h, invalidRequest, sending)
	}
	server.sendResponse(edc, req.h, req.replyv.Interface(), sending)
}

func (server *Server) sendResponse(edc codec.Codec, h *codec.Header, body interface{}, sending *sync.Mutex){
	//发送一个header和body的完整组合，才能发送下一个
	sending.Lock()
	defer sending.Unlock()

	err := edc.Write(h, body)
	if err != nil{
		log.Println("rpc server: sendResponse err:" + err.Error())
	}
}

const (
	connected        = "200 Connected to Gee RPC"
	defaultRPCPath   = "/_geeprc_"
	defaultDebugPath = "/debug/geerpc"
)

// ServeHTTP implements an http.Handler that answers RPC requests.
func (server *Server) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method != "CONNECT" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusMethodNotAllowed)
		_, _ = io.WriteString(w, "405 must CONNECT\n")
		return
	}
	conn, _, err := w.(http.Hijacker).Hijack()
	if err != nil {
		log.Print("rpc hijacking ", req.RemoteAddr, ": ", err.Error())
		return
	}
	_, _ = io.WriteString(conn, "HTTP/1.0 "+connected+"\n\n")
	server.ServeConn(conn)
}

// HandleHTTP registers an HTTP handler for RPC messages on rpcPath.
// It is still necessary to invoke http.Serve(), typically in a go statement.
func (server *Server) HandleHTTP() {
	http.Handle(defaultRPCPath, server)
}

// HandleHTTP is a convenient approach for default server to register HTTP handlers
func HandleHTTP() {
	DefaultServer.HandleHTTP()
}

