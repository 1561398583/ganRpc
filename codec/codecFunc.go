package codec

import (
	"io"
)

//EncDec的构造函数
type NewCodecFunc func(io.ReadWriteCloser) Codec


type CoderType string

const (
	GobType  CoderType = "application/gob"
	JsonType CoderType = "application/json" // not implemented
	XMLType CoderType = "application/xml" // not implemented
)

//一个EncoderType对应一个编解码器的构造函数。
var NewCodecFuncMap map[CoderType]NewCodecFunc

func init() {
	NewCodecFuncMap = make(map[CoderType]NewCodecFunc)
	//目前只实现了gob的编解码
	NewCodecFuncMap[GobType] = NewGobCodec	//GobCodec的构造函数
}
