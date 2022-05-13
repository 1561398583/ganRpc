package codec

import (
	"bufio"
	"encoding/gob"
	"io"
	"log"
)

type GobCodec struct {
	conn io.ReadWriteCloser
	buf  *bufio.Writer
	dec  *gob.Decoder
	enc  *gob.Encoder
}

func (ged *GobCodec) Close() error {
	return ged.conn.Close()
}
func (ged *GobCodec) ReadHeader(h *Header) error {
	return ged.dec.Decode(h)
}
func (ged *GobCodec) ReadBody(body interface{}) error {
	return ged.dec.Decode(body)
}
func (ged *GobCodec) Write(h *Header, body interface{}) (err error) {
	if err = ged.enc.Encode(h); err != nil{
		log.Println(err)
		return
	}

	if err = ged.enc.Encode(body); err != nil{
		log.Println(err)
		return
	}

	err = ged.buf.Flush()
	if err != nil {
		_ =ged.Close()
		return err
	}
	return
}


func NewGobCodec(conn io.ReadWriteCloser) Codec {
	buf := bufio.NewWriter(conn)
	ged := GobCodec{
		conn: conn,
		buf: buf,
		dec: gob.NewDecoder(conn),
		enc: gob.NewEncoder(buf),
	}
	return &ged
}
