package GanRPC

import (
	"log"
	"net"
	"testing"
)

func TestServer_Accept(t *testing.T) {
	var foo Foo
	err := RegisterService(foo) //注册服务到DefaultServer中
	if err != nil {
		log.Fatal(err)
	}
	l, err := net.Listen("tcp", ":9000")
	if err != nil {
		log.Fatal(err)
	}

	log.Println("start rpc server on "+l.Addr().String())
	Accept(l)
}

func TestServer_HandleHTTP(t *testing.T) {
	HandleHTTP() //注册http服务

}

