package codec

import "io"

type Header struct {
	// format "Service.Method"
	ServiceMethod string
	//Seq 是请求的序号，也可以认为是某个请求的 ID，用来区分不同的请求。
	Seq           uint64
	//Error 是错误信息，客户端置为空，服务端如果如果发生错误，将错误信息置于 Error 中。
	Error         string
}

//抽象出对消息体进行编解码的接口 EncDec，抽象出接口是为了实现不同的 EncDec 实例
type Codec interface {
	io.Closer
	ReadHeader(*Header) error
	ReadBody(interface{}) error
	Write(*Header, interface{}) error
}

